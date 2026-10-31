// Package agent 在 OpenVPN 容器内应用面板下发的控制包并执行准入。
//
// 控制包由面板写入共享控制目录；本包不接收网络控制请求，只读取控制包、
// 执行启停命令并回写状态。OpenVPN 管理接口只绑定容器内 Unix Socket，
// 准入由本代理完成。
package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dreamstation625/ovpanel/internal/admission"
	"github.com/dreamstation625/ovpanel/internal/agentproto"
	"github.com/dreamstation625/ovpanel/internal/agentusage"
	"github.com/dreamstation625/ovpanel/internal/connectpolicy"
	"github.com/dreamstation625/ovpanel/internal/netpolicy"
	"github.com/dreamstation625/ovpanel/internal/serverconfig"
	"github.com/dreamstation625/ovpanel/internal/telemetry"
	"github.com/dreamstation625/ovpanel/internal/traffic"
	"github.com/google/uuid"
)

const (
	bundleFile        = "bundle.json"
	commandFile       = "command.json"
	lastGoodFile      = "last-good.json"
	stoppedFile       = "stopped"
	connectPolicyFile = "connect-policy.json"
	defaultConfigDir  = "/etc/openvpn/ovpanel"
	managementDir     = "/run/ovpanel"
	managementSocket  = serverconfig.ManagementSocket
	openvpnLogFile    = "openvpn.log"
)

type sessionInfo struct {
	id          string
	profileID   string
	certSerial  string
	vpnIP       string
	sourceIP    string
	connectedAt time.Time
	upBytes     uint64
	downBytes   uint64
	upRate      float64
	downRate    float64
	seq         uint64
	lastSample  time.Time
	lastEvent   time.Time
	// eventUp/eventDown 是最近一次事件写入时的累计值，结束事件据此补足
	// 尚未写入事件日志的增量，避免日汇总丢失最后一个采样窗口。
	eventUp     uint64
	eventDown   uint64
	blocked     bool
	blockReason string
}

// Status 是运行代理当前应用状态。
type Status struct {
	Configured          bool       `json:"configured"`
	Stopped             bool       `json:"stopped,omitempty"`
	OpenVPNRunning      bool       `json:"openvpnRunning"`
	ManagementConnected bool       `json:"managementConnected"`
	ConfigHash          string     `json:"configHash,omitempty"`
	Sessions            int        `json:"sessions"`
	UsageGeneration     string     `json:"usageGeneration,omitempty"`
	UsageSeq            uint64     `json:"usageSeq"`
	UsageHealthy        bool       `json:"usageHealthy"`
	UsageError          string     `json:"usageError,omitempty"`
	LastError           string     `json:"lastError,omitempty"`
	RolledBack          bool       `json:"rolledBack,omitempty"`
	RollbackReason      string     `json:"rollbackReason,omitempty"`
	AppliedAt           *time.Time `json:"appliedAt,omitempty"`
}

// Runtime 管理控制包、OpenVPN 子进程与准入。
type Runtime struct {
	controlDir string
	stateDir   string
	configDir  string
	log        *slog.Logger
	now        func() time.Time

	mu                  sync.Mutex
	snapshot            admission.Snapshot
	bundleHash          string
	configHash          string
	failedHash          string
	lastGoodBundle      []byte
	lastGoodHash        string
	rolledBack          bool
	rollbackReason      string
	openvpnRunning      bool
	managementConnected bool
	configured          bool
	stopped             bool
	lastCommandID       string
	sessions            map[string]sessionInfo
	pendingAddr         map[string]string
	serverPolicy        netpolicy.Policy
	policyDirty         bool
	trafficDirty        bool
	trafficActive       bool
	usageHealthy        bool
	usageError          string
	lastError           string
	appliedAt           time.Time

	usage *agentusage.Store

	proc       *exec.Cmd
	procDone   chan struct{}
	mgmtCancel context.CancelFunc
	mgmtConn   net.Conn
	mgmtWriter *bufio.Writer
	writeMu    sync.Mutex

	// 测试替换点；生产环境使用下方真实实现。
	applyPolicyFn  func(netpolicy.Policy) error
	applyTrafficFn func([]traffic.Session) error
	startVPNFn     func(context.Context, string) error
}

// New 创建运行代理。每次进程启动生成新的运行代次，会话以代次 + CID 区分。
func New(controlDir, stateDir string, log *slog.Logger) *Runtime {
	generation := uuid.NewString()
	return &Runtime{
		controlDir:  controlDir,
		stateDir:    stateDir,
		configDir:   defaultConfigDir,
		log:         log,
		now:         func() time.Time { return time.Now().UTC() },
		sessions:    map[string]sessionInfo{},
		pendingAddr: map[string]string{},
		usage:       agentusage.New(stateDir, controlDir, generation, log),
	}
}

// Run 持续监听控制包，直到 ctx 结束。
func (r *Runtime) Run(ctx context.Context) {
	r.loadPersistedState()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastSave := r.now()
	for {
		if err := r.handleCommand(ctx); err != nil {
			r.setError(err)
			r.log.Warn("handle control command failed", "error", err)
		}
		if err := r.applyOnce(ctx); err != nil {
			r.setError(err)
			r.log.Warn("apply control bundle failed", "error", err)
		}
		// 到期与额度必须持续执行，不能只在控制包变化时检查。
		r.pruneSessions()
		r.mu.Lock()
		policyDirty := r.policyDirty
		trafficDirty := r.trafficDirty
		r.mu.Unlock()
		if policyDirty {
			if err := r.refreshPolicy(); err != nil {
				r.log.Warn("retry session policy failed", "error", err)
			}
		}
		if trafficDirty {
			if err := r.refreshTraffic(); err != nil {
				r.log.Warn("retry session shaping failed", "error", err)
			}
		}
		if r.now().Sub(lastSave) >= 5*time.Second {
			if err := r.usage.Save(); err != nil {
				r.log.Warn("persist usage ledger failed", "error", err)
			}
			lastSave = r.now()
		}
		r.writeStatus()
		r.writeUsageSnapshot()
		select {
		case <-ctx.Done():
			r.Stop()
			return
		case <-ticker.C:
		}
	}
}

// writeStatus 把状态写入控制目录，供面板展示；文件不含秘密材料。
func (r *Runtime) writeStatus() {
	payload, err := json.MarshalIndent(r.Status(), "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(r.controlDir, "status.json")
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".status-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(name, path)
}

type controlCommand struct {
	ID       string `json:"id"`
	Action   string `json:"action"`
	CID      string `json:"cid,omitempty"`
	IssuedAt string `json:"issuedAt"`
}

func (r *Runtime) handleCommand(ctx context.Context) error {
	data, err := os.ReadFile(filepath.Join(r.controlDir, commandFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取控制命令: %w", err)
	}
	var command controlCommand
	if err := json.Unmarshal(data, &command); err != nil {
		return fmt.Errorf("解析控制命令: %w", err)
	}
	r.mu.Lock()
	if command.ID == "" || command.ID == r.lastCommandID {
		r.mu.Unlock()
		return nil
	}
	r.lastCommandID = command.ID
	r.mu.Unlock()
	// 命令只执行一次：处理后立即删除，代理重启时不会重放上一条命令。
	_ = os.Remove(filepath.Join(r.controlDir, commandFile))

	switch command.Action {
	case "stop":
		r.stopProcess()
		if err := netpolicy.Delete(); err != nil {
			r.log.Warn("delete network policy failed", "error", err)
		}
		r.setStopped(true)
		r.log.Warn("OpenVPN stopped by panel command")
	case "disconnect":
		if command.CID == "" {
			return fmt.Errorf("断开命令缺少 CID")
		}
		r.finishSession(command.CID, "admin_disconnect", true)
		r.log.Warn("session disconnected by panel command", "cid", command.CID)
	case "start", "restart":
		r.setStopped(false)
		r.mu.Lock()
		r.bundleHash = ""
		r.failedHash = ""
		if command.Action == "restart" {
			// 配置未变化时也要重新拉起进程，不能只复用现有 OpenVPN。
			r.configHash = ""
		}
		r.mu.Unlock()
		if err := r.applyOnce(ctx); err != nil {
			return err
		}
		r.log.Warn("OpenVPN started by panel command", "action", command.Action)
	default:
		return fmt.Errorf("未知控制命令：%q", command.Action)
	}
	return nil
}

func (r *Runtime) applyOnce(ctx context.Context) error {
	r.mu.Lock()
	stopped := r.stopped
	r.mu.Unlock()
	if stopped {
		return nil
	}
	path := filepath.Join(r.controlDir, bundleFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取控制包: %w", err)
	}
	bundle, err := decodeBundle(data)
	if err != nil {
		return err
	}

	bundleHash := hashBytes(data)
	r.mu.Lock()
	applied := r.bundleHash == bundleHash && r.openvpnRunning && r.managementConnected
	failed := r.failedHash == bundleHash
	r.mu.Unlock()
	if applied || failed {
		// failedHash 防止同一份已经失败的控制包每秒重试；
		// 面板重新下发会产生新的控制包哈希，届时再尝试。
		return nil
	}

	if err := r.applyBundle(ctx, bundle, bundleHash); err != nil {
		r.mu.Lock()
		r.failedHash = bundleHash
		r.mu.Unlock()
		return r.rollback(ctx, bundleHash, err)
	}
	r.commitGood(data, bundleHash)
	r.clearError()
	r.log.Info("control bundle applied",
		"config_hash", bundle.ConfigHash,
		"profiles", len(bundle.Snapshot.Profiles),
		"users", len(bundle.Snapshot.Users))
	return nil
}

func decodeBundle(data []byte) (agentproto.Bundle, error) {
	var bundle agentproto.Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return bundle, fmt.Errorf("解析控制包: %w", err)
	}
	if bundle.Version != agentproto.Version {
		return bundle, fmt.Errorf("控制包版本不支持：%d", bundle.Version)
	}
	sum := sha256.Sum256([]byte(bundle.Config))
	if hex.EncodeToString(sum[:]) != bundle.ConfigHash {
		return bundle, fmt.Errorf("控制包配置哈希不匹配")
	}
	if bundle.CAPEM == "" || bundle.ServerCertPEM == "" || bundle.ServerKeyPEM == "" || bundle.TLSCryptKey == "" || bundle.CRLPEM == "" {
		return bundle, fmt.Errorf("控制包材料不完整")
	}
	if err := bundle.Snapshot.Validate(); err != nil {
		return bundle, fmt.Errorf("认证快照非法: %w", err)
	}
	return bundle, nil
}

// applyBundle 应用一份已校验的控制包；成功后 bundleHash 指向该控制包。
func (r *Runtime) applyBundle(ctx context.Context, bundle agentproto.Bundle, bundleHash string) error {
	if err := r.writeMaterials(bundle); err != nil {
		return err
	}
	if err := r.writeConnectPolicy(bundle); err != nil {
		return err
	}

	base := bundle.Policy
	base.Sessions = nil
	r.mu.Lock()
	r.snapshot = bundle.Snapshot
	r.serverPolicy = base
	needsRestart := r.configHash != bundle.ConfigHash || !r.openvpnRunning || !r.managementConnected
	r.mu.Unlock()
	// 面板手动重置额度会递增代次；账本把当时累计设为新基线。
	r.usage.SyncEpochs(bundle.Snapshot)
	if err := r.refreshPolicy(); err != nil {
		return err
	}
	if err := r.refreshTraffic(); err != nil {
		return err
	}
	r.mu.Lock()
	r.configured = true
	r.appliedAt = bundle.IssuedAt
	r.mu.Unlock()

	if needsRestart {
		if err := r.startVPN(ctx, bundle.ConfigHash); err != nil {
			return err
		}
	} else {
		r.pruneSessions()
	}

	r.mu.Lock()
	r.bundleHash = bundleHash
	r.mu.Unlock()
	return nil
}

// commitGood 记录并持久化最近一次成功应用的控制包，供后续失败回退。
func (r *Runtime) commitGood(data []byte, bundleHash string) {
	raw := append([]byte(nil), data...)
	r.mu.Lock()
	r.lastGoodBundle = raw
	r.lastGoodHash = bundleHash
	r.failedHash = ""
	r.rolledBack = false
	r.rollbackReason = ""
	r.mu.Unlock()
	if err := writeSecure(filepath.Join(r.stateDir, lastGoodFile), raw); err != nil {
		r.log.Warn("persist last good bundle failed", "error", err)
	}
}

// writeUsageSnapshot 把在线会话与配置用量写入控制目录，供面板实时展示。
func (r *Runtime) writeUsageSnapshot() {
	now := r.now()
	r.mu.Lock()
	sessions := make([]telemetry.LiveSession, 0, len(r.sessions))
	for _, info := range r.sessions {
		upRate, downRate := info.upRate, info.downRate
		if now.Sub(info.lastSample) > 5*time.Second {
			// 连接空闲时不再沿用最后一次采样速率。
			upRate, downRate = 0, 0
		}
		sessions = append(sessions, telemetry.LiveSession{
			ID:          info.id,
			CID:         strings.TrimPrefix(info.id, r.usage.Generation()+":"),
			ProfileID:   info.profileID,
			VPNIP:       info.vpnIP,
			SourceIP:    info.sourceIP,
			ConnectedAt: info.connectedAt,
			UpBytes:     info.upBytes,
			DownBytes:   info.downBytes,
			UpRate:      upRate,
			DownRate:    downRate,
			Blocked:     info.blocked,
			BlockReason: info.blockReason,
		})
	}
	r.mu.Unlock()
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].ConnectedAt.Equal(sessions[j].ConnectedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].ConnectedAt.Before(sessions[j].ConnectedAt)
	})

	snapshot := telemetry.Snapshot{
		Version:    telemetry.Version,
		Generation: r.usage.Generation(),
		UpdatedAt:  r.now(),
		EventSeq:   r.usage.CurrentSeq(),
		Profiles:   r.usage.ProfileStats(),
		Sessions:   sessions,
	}
	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(r.controlDir, telemetry.SnapshotFile)
	if err := os.MkdirAll(r.controlDir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(r.controlDir, ".usage-*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(name, path)
}

func (r *Runtime) loadPersistedState() {
	r.loadLastGood()
	if err := r.usage.Load(); err != nil {
		r.mu.Lock()
		r.usageHealthy = false
		r.usageError = err.Error()
		r.lastError = "用量账本不可用：" + err.Error()
		r.mu.Unlock()
		r.log.Error("usage ledger unavailable; new admissions will be denied", "error", err)
	} else {
		r.mu.Lock()
		r.usageHealthy = true
		r.usageError = ""
		r.mu.Unlock()
	}
	if _, err := os.Stat(filepath.Join(r.stateDir, stoppedFile)); err == nil {
		r.mu.Lock()
		r.stopped = true
		r.mu.Unlock()
	}
}

// setStopped 记录管理员停止意图，使代理或容器重启后不会自动拉起 OpenVPN。
func (r *Runtime) setStopped(stopped bool) {
	r.mu.Lock()
	r.stopped = stopped
	r.mu.Unlock()
	path := filepath.Join(r.stateDir, stoppedFile)
	if stopped {
		if err := writeSecure(path, []byte("stopped\n")); err != nil {
			r.log.Warn("persist stopped state failed", "error", err)
		}
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.log.Warn("clear stopped state failed", "error", err)
	}
}

func (r *Runtime) loadLastGood() {
	data, err := os.ReadFile(filepath.Join(r.stateDir, lastGoodFile))
	if err != nil {
		return
	}
	if _, err := decodeBundle(data); err != nil {
		r.log.Warn("ignore invalid last good bundle", "error", err)
		return
	}
	r.mu.Lock()
	r.lastGoodBundle = data
	r.lastGoodHash = hashBytes(data)
	r.mu.Unlock()
}

// rollback 在候选控制包应用失败后恢复上一份成功控制包。
// 面板仍保留失败的新修订，回退原因通过状态回传。
func (r *Runtime) rollback(ctx context.Context, failedHash string, cause error) error {
	r.mu.Lock()
	raw := append([]byte(nil), r.lastGoodBundle...)
	goodHash := r.lastGoodHash
	r.mu.Unlock()
	if len(raw) == 0 || goodHash == failedHash {
		return cause
	}
	bundle, err := decodeBundle(raw)
	if err != nil {
		return fmt.Errorf("应用新配置失败（%v），上一版本控制包无效：%w", cause, err)
	}
	if err := r.applyBundle(ctx, bundle, goodHash); err != nil {
		return fmt.Errorf("应用新配置失败（%v），回退上一版本也失败：%w", cause, err)
	}
	r.mu.Lock()
	r.rolledBack = true
	r.rollbackReason = cause.Error()
	r.lastError = "新配置应用失败，已回退到上一版本：" + cause.Error()
	r.mu.Unlock()
	r.log.Warn("rolled back to last good bundle", "reason", cause, "hash", goodHash)
	return nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (r *Runtime) writeMaterials(bundle agentproto.Bundle) error {
	if err := os.MkdirAll(r.configDir, 0o700); err != nil {
		return fmt.Errorf("创建证书目录: %w", err)
	}
	files := map[string]string{
		serverconfig.PathCA:                       bundle.CAPEM,
		serverconfig.PathServerCert:               bundle.ServerCertPEM,
		serverconfig.PathServerKey:                bundle.ServerKeyPEM,
		serverconfig.PathTLSCrypt:                 bundle.TLSCryptKey,
		serverconfig.PathCRL:                      bundle.CRLPEM,
		filepath.Join(r.configDir, "server.conf"): bundle.Config,
	}
	for path, content := range files {
		if err := writeSecure(path, []byte(content)); err != nil {
			return err
		}
	}
	return nil
}

// writeConnectPolicy 为 client-connect 助手生成身份到路由策略的映射。
// 文件只包含有效策略，不含证书或密码材料。
func (r *Runtime) writeConnectPolicy(bundle agentproto.Bundle) error {
	file, err := connectpolicy.Build(bundle.Snapshot, connectpolicy.Policy{
		GlobalExit:      bundle.Policy.GlobalExit,
		AllowedInternal: bundle.Policy.AllowedInternal,
	})
	if err != nil {
		return fmt.Errorf("生成连接路由策略: %w", err)
	}
	if err := connectpolicy.Write(filepath.Join(r.stateDir, connectPolicyFile), file); err != nil {
		return err
	}
	return nil
}

// refreshPolicy 依据当前在线会话重建逐连接 nftables 规则。
// 未取得地址或未登记的会话由地址池默认拒绝兜底。
func (r *Runtime) refreshPolicy() error {
	r.mu.Lock()
	base := r.serverPolicy
	snapshot := r.snapshot
	sessions := make([]netpolicy.Session, 0, len(r.sessions))
	for _, info := range r.sessions {
		if info.vpnIP == "" {
			continue
		}
		effective := connectpolicy.Policy{}
		if profile, ok := findProfile(snapshot, info.profileID); ok {
			effective = connectpolicy.Effective(profile, connectpolicy.Policy{
				GlobalExit:      base.GlobalExit,
				AllowedInternal: base.AllowedInternal,
			})
		}
		sessions = append(sessions, netpolicy.Session{
			IP:              info.vpnIP,
			AllowedInternal: effective.AllowedInternal,
			GlobalExit:      effective.GlobalExit,
		})
	}
	base.Sessions = sessions
	base.ProtectTargets = nil
	r.mu.Unlock()

	if err := r.applyPolicy(base); err != nil {
		r.mu.Lock()
		r.policyDirty = true
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	r.policyDirty = false
	r.mu.Unlock()
	return nil
}

// refreshTraffic 按当前在线会话重建 tc/IFB 限速。
// 每次会话、地址或限速变化都整体重建，避免地址复用后沿用旧 class。
func (r *Runtime) refreshTraffic() error {
	r.mu.Lock()
	tun := r.serverPolicy.TunDevice
	snapshot := r.snapshot
	active := r.trafficActive
	sessions := make([]traffic.Session, 0, len(r.sessions))
	for cid, info := range r.sessions {
		if info.vpnIP == "" {
			continue
		}
		profile, ok := findProfile(snapshot, info.profileID)
		if !ok || (profile.UploadBps <= 0 && profile.DownloadBps <= 0) {
			continue
		}
		sessions = append(sessions, traffic.Session{
			CID:         cid,
			IP:          info.vpnIP,
			UploadBps:   profile.UploadBps,
			DownloadBps: profile.DownloadBps,
		})
	}
	r.mu.Unlock()

	if r.applyTrafficFn != nil {
		if err := r.applyTrafficFn(sessions); err != nil {
			r.mu.Lock()
			r.trafficDirty = true
			r.mu.Unlock()
			return err
		}
		r.mu.Lock()
		r.trafficActive = len(sessions) > 0
		r.trafficDirty = false
		r.mu.Unlock()
		return nil
	}

	if len(sessions) == 0 {
		if !active {
			r.mu.Lock()
			r.trafficDirty = false
			r.mu.Unlock()
			return nil
		}
		if err := traffic.Apply(traffic.Clear(tun, "ifb0")); err != nil {
			r.mu.Lock()
			r.trafficDirty = true
			r.mu.Unlock()
			return err
		}
		r.mu.Lock()
		r.trafficActive = false
		r.trafficDirty = false
		r.mu.Unlock()
		return nil
	}

	if err := traffic.Apply(traffic.Clear(tun, "ifb0")); err != nil {
		r.mu.Lock()
		r.trafficDirty = true
		r.mu.Unlock()
		return err
	}
	cmds, err := traffic.Render(tun, "ifb0", sessions)
	if err != nil {
		r.mu.Lock()
		r.trafficDirty = true
		r.mu.Unlock()
		return err
	}
	if err := traffic.Apply(cmds); err != nil {
		r.mu.Lock()
		r.trafficDirty = true
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	r.trafficActive = true
	r.trafficDirty = false
	r.mu.Unlock()
	return nil
}

// clearTraffic 删除项目创建的限速队列。
func (r *Runtime) clearTraffic() {
	r.mu.Lock()
	tun := r.serverPolicy.TunDevice
	r.trafficActive = false
	r.mu.Unlock()
	if r.applyTrafficFn != nil {
		_ = r.applyTrafficFn(nil)
		return
	}
	if err := traffic.Apply(traffic.Clear(tun, "ifb0")); err != nil {
		r.log.Warn("clear traffic shaping failed", "error", err)
	}
}

func findProfile(snapshot admission.Snapshot, id string) (admission.Profile, bool) {
	for i := range snapshot.Profiles {
		if snapshot.Profiles[i].ID == id {
			return snapshot.Profiles[i], true
		}
	}
	return admission.Profile{}, false
}

func (r *Runtime) restartOpenVPN(ctx context.Context, configHash string) error {
	r.stopProcess()

	if err := os.MkdirAll(managementDir, 0o755); err != nil {
		return fmt.Errorf("创建管理目录: %w", err)
	}
	_ = os.Remove(managementSocket)

	if err := os.MkdirAll(r.stateDir, 0o700); err != nil {
		return fmt.Errorf("创建状态目录: %w", err)
	}
	logFile, err := os.OpenFile(filepath.Join(r.stateDir, openvpnLogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开 OpenVPN 日志: %w", err)
	}

	cmd := exec.Command("openvpn", "--config", filepath.Join(r.configDir, "server.conf"))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("启动 OpenVPN: %w", err)
	}

	done := make(chan struct{})
	r.mu.Lock()
	r.proc = cmd
	r.procDone = done
	r.openvpnRunning = true
	r.configHash = configHash
	r.sessions = map[string]sessionInfo{}
	r.pendingAddr = map[string]string{}
	r.mu.Unlock()
	if err := r.refreshPolicy(); err != nil {
		r.log.Warn("clear session policy after restart failed", "error", err)
	}

	go func() {
		err := cmd.Wait()
		logFile.Close()
		close(done)
		r.mu.Lock()
		if r.proc == cmd {
			r.proc = nil
			r.procDone = nil
			r.openvpnRunning = false
			r.managementConnected = false
			if err != nil {
				r.lastError = fmt.Sprintf("OpenVPN 退出: %v", err)
			}
		}
		r.mu.Unlock()
		r.log.Warn("openvpn exited", "error", err)
	}()

	if err := waitForSocket(ctx, managementSocket, done, 15*time.Second); err != nil {
		r.stopProcess()
		return err
	}

	mgmtCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.mgmtCancel = cancel
	r.mu.Unlock()
	go r.manage(mgmtCtx)
	return nil
}

func (r *Runtime) manage(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		conn, err := net.Dial("unix", managementSocket)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
				continue
			}
		}

		writer := bufio.NewWriter(conn)
		r.mu.Lock()
		r.mgmtConn = conn
		r.mgmtWriter = writer
		r.managementConnected = true
		r.mu.Unlock()
		r.log.Info("management connected")
		// 每秒推送每客户端累计字节；bytecount 不会被认证回调窗口阻塞。
		if err := r.send("bytecount 1"); err != nil {
			r.log.Warn("enable bytecount failed", "error", err)
		}

		r.readManagement(ctx, conn)

		r.mu.Lock()
		if r.mgmtConn == conn {
			r.mgmtConn = nil
			r.mgmtWriter = nil
			r.managementConnected = false
		}
		r.mu.Unlock()
		conn.Close()
		if ctx.Err() != nil {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func (r *Runtime) readManagement(ctx context.Context, conn net.Conn) {
	reader := bufio.NewReader(conn)
	var pending *pendingAuth
	for {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			return
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, ">") {
			continue
		}
		rest := strings.TrimPrefix(line, ">")
		if strings.HasPrefix(rest, "CLIENT:ENV,") {
			payload := rest[len("CLIENT:ENV,"):]
			if payload == "END" {
				if pending != nil {
					r.evaluate(pending)
					pending = nil
				}
				continue
			}
			if pending != nil {
				if idx := strings.Index(payload, "="); idx > 0 {
					pending.env[payload[:idx]] = payload[idx+1:]
				}
			}
			continue
		}
		// 用量采样通知带冒号分隔的 CID，与普通通知的逗号格式不同。
		if strings.HasPrefix(rest, "BYTECOUNT_CLI:") {
			fields := strings.Split(rest[len("BYTECOUNT_CLI:"):], ",")
			if len(fields) >= 3 {
				received, err1 := strconv.ParseUint(strings.TrimSpace(fields[1]), 10, 64)
				sent, err2 := strconv.ParseUint(strings.TrimSpace(fields[2]), 10, 64)
				if err1 == nil && err2 == nil {
					r.recordUsage(fields[0], received, sent)
				}
			}
			continue
		}

		parts := strings.Split(rest, ",")
		switch parts[0] {
		case "CLIENT:CONNECT", "CLIENT:REAUTH":
			if len(parts) >= 3 {
				kind := "connect"
				if parts[0] == "CLIENT:REAUTH" {
					kind = "reauth"
				}
				pending = &pendingAuth{cid: parts[1], kid: parts[2], kind: kind, env: map[string]string{}}
			}
		case "CLIENT:ADDRESS":
			if len(parts) >= 3 {
				r.setSessionAddress(parts[1], parts[2])
			}
		case "CLIENT:DISCONNECT":
			if len(parts) >= 2 {
				r.finishSession(parts[1], "client_disconnect", false)
			}
		}
	}
}

type pendingAuth struct {
	cid  string
	kid  string
	kind string
	env  map[string]string
}

// setSessionAddress 记录会话取得的 VPN 地址并立即重建逐连接规则。
func (r *Runtime) setSessionAddress(cid, address string) {
	address = strings.TrimSpace(address)
	now := r.now()
	var (
		info      sessionInfo
		ok        bool
		reusedCID string
		reused    sessionInfo
	)
	r.mu.Lock()
	info, ok = r.sessions[cid]
	if ok {
		// 断线重连可能在旧会话超时前重新分配到同一地址；
		// 移除持有该地址的旧记录，避免规则重复或沿用旧策略。
		for otherCID, other := range r.sessions {
			if otherCID != cid && other.vpnIP == address {
				reusedCID, reused = otherCID, other
				delete(r.sessions, otherCID)
				delete(r.pendingAddr, otherCID)
				break
			}
		}
		info.vpnIP = address
		r.sessions[cid] = info
	} else {
		r.pendingAddr[cid] = address
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	if reusedCID != "" {
		r.endSessionRecord(reusedCID, reused, "address_reused", now)
	}
	if _, err := r.usage.AppendEvent(telemetry.Event{
		Kind:        telemetry.EventSessionAddress,
		At:          now,
		SessionID:   info.id,
		CID:         cid,
		ProfileID:   info.profileID,
		VPNIP:       address,
		SourceIP:    info.sourceIP,
		UpBytes:     info.upBytes,
		DownBytes:   info.downBytes,
		ConnectedAt: timePtr(info.connectedAt),
	}); err != nil {
		r.log.Warn("append session address event failed", "error", err, "cid", cid)
	}
	r.mu.Lock()
	if current, exists := r.sessions[cid]; exists && current.id == info.id {
		current.lastEvent = now
		r.sessions[cid] = current
	}
	r.mu.Unlock()
	if err := r.refreshPolicy(); err != nil {
		r.log.Warn("refresh policy after address failed", "error", err, "cid", cid)
	}
	if err := r.refreshTraffic(); err != nil {
		r.log.Warn("refresh shaping after address failed", "error", err, "cid", cid)
	}
}

func (r *Runtime) evaluate(pending *pendingAuth) {
	r.mu.Lock()
	snapshot := r.snapshot
	usageHealthy := r.usageHealthy
	r.mu.Unlock()

	incoming := incomingFromEnv(pending.env)
	now := r.now()
	decision := admission.Decide(snapshot, incoming, now)
	if !decision.Allow {
		r.denyAuth(pending, decision.Code, decision.Reason)
		return
	}
	if !usageHealthy {
		r.denyAuth(pending, "usage_ledger_unavailable", "用量账本不可用，暂不接收新连接")
		return
	}

	profile, ok := findProfile(snapshot, decision.ProfileID)
	if !ok {
		r.denyAuth(pending, admission.CodeAmbiguousIdentity, "配置不存在")
		return
	}

	// 重认证通常复用已有会话、不新增占位；若会话已不在内存中
	// （例如代理刚恢复），按新连接重新预留并发名额。
	isNew := pending.kind != "reauth"
	if pending.kind == "reauth" {
		r.mu.Lock()
		_, exists := r.sessions[pending.cid]
		r.mu.Unlock()
		isNew = !exists
	}

	if isNew {
		// 额度耗尽时拒绝新认证，而不是先放行再阻断。
		if r.usage.Exhausted(profile.ID, profile.QuotaBytes) {
			r.denyAuth(pending, admission.CodeQuotaExhausted, "流量额度已用尽")
			return
		}
		if profile.ConcurrentLimit > 0 && r.profileSessionCount(profile.ID) >= profile.ConcurrentLimit {
			r.denyAuth(pending, admission.CodeConcurrencyLimit, "同时在线数已达上限")
			return
		}
	}

	if err := r.send(fmt.Sprintf("client-auth-nt %s %s", pending.cid, pending.kid)); err != nil {
		r.log.Warn("send client-auth-nt failed", "error", err)
		return
	}

	sessionID := r.usage.Generation() + ":" + pending.cid
	var (
		vpnIP     string
		reusedCID string
		reused    sessionInfo
	)
	r.mu.Lock()
	if !isNew {
		if current, exists := r.sessions[pending.cid]; exists {
			current.profileID = profile.ID
			current.certSerial = incoming.CertSerial
			current.sourceIP = incoming.SourceIP
			r.sessions[pending.cid] = current
		}
	} else {
		vpnIP = r.pendingAddr[pending.cid]
		delete(r.pendingAddr, pending.cid)
		if vpnIP != "" {
			for otherCID, other := range r.sessions {
				if otherCID != pending.cid && other.vpnIP == vpnIP {
					reusedCID, reused = otherCID, other
					delete(r.sessions, otherCID)
					delete(r.pendingAddr, otherCID)
					break
				}
			}
		}
		r.sessions[pending.cid] = sessionInfo{
			id:          sessionID,
			profileID:   profile.ID,
			certSerial:  incoming.CertSerial,
			vpnIP:       vpnIP,
			sourceIP:    incoming.SourceIP,
			connectedAt: now,
			lastSample:  now,
		}
	}
	r.mu.Unlock()

	if reusedCID != "" {
		r.endSessionRecord(reusedCID, reused, "address_reused", now)
	}
	if isNew {
		event := telemetry.Event{
			Kind:        telemetry.EventSessionStart,
			At:          now,
			SessionID:   sessionID,
			CID:         pending.cid,
			ProfileID:   profile.ID,
			VPNIP:       vpnIP,
			SourceIP:    incoming.SourceIP,
			UpBytes:     0,
			DownBytes:   0,
			ConnectedAt: &now,
		}
		if _, err := r.usage.AppendEvent(event); err != nil {
			r.log.Warn("append session start event failed", "error", err, "cid", pending.cid)
		} else {
			r.mu.Lock()
			if current, exists := r.sessions[pending.cid]; exists {
				current.lastEvent = now
				r.sessions[pending.cid] = current
			}
			r.mu.Unlock()
		}
	}

	if vpnIP != "" {
		if err := r.refreshPolicy(); err != nil {
			r.log.Warn("refresh policy after admission failed", "error", err)
		}
		if err := r.refreshTraffic(); err != nil {
			r.log.Warn("refresh shaping after admission failed", "error", err)
		}
	}
	r.log.Info("client admitted",
		"profile_id", decision.ProfileID,
		"cid", pending.cid,
		"auth", decision.Code,
		"reauth", pending.kind == "reauth")
}

func (r *Runtime) denyAuth(pending *pendingAuth, code, reason string) {
	message := escapeMgmtArg(reason)
	if err := r.send(fmt.Sprintf("client-deny %s %s %q %q", pending.cid, pending.kid, code, message)); err != nil {
		r.log.Warn("send client-deny failed", "error", err)
	}
	r.log.Warn("client denied",
		"cid", pending.cid,
		"code", code,
		"reason", reason)
}

func (r *Runtime) profileSessionCount(profileID string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	for _, info := range r.sessions {
		if info.profileID == profileID {
			count++
		}
	}
	return count
}

func preferredSourceIP(incoming admission.Incoming) string {
	return incoming.SourceIP
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// deltaSince 返回累计值相对上次事件高水位的新增量；计数回退时返回 0。
func deltaSince(current, previous uint64) uint64 {
	if current <= previous {
		return 0
	}
	return current - previous
}

// recordUsage 处理一次 bytecount 采样：更新速率、账本与额度判定。
func (r *Runtime) recordUsage(cid string, received, sent uint64) {
	now := r.now()
	var (
		info           sessionInfo
		ok             bool
		profile        admission.Profile
		hasProfile     bool
		quotaKill      bool
		eventDeltaUp   uint64
		eventDeltaDown uint64
	)
	r.mu.Lock()
	info, ok = r.sessions[cid]
	if !ok {
		r.mu.Unlock()
		return
	}
	elapsed := now.Sub(info.lastSample).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}
	if received >= info.upBytes {
		info.upRate = float64(received-info.upBytes) / elapsed
	} else {
		info.upRate = 0
	}
	if sent >= info.downBytes {
		info.downRate = float64(sent-info.downBytes) / elapsed
	} else {
		info.downRate = 0
	}
	info.upBytes = received
	info.downBytes = sent
	info.lastSample = now
	info.seq++

	// 高水位在会话被移除前更新；与断开操作共用同一把锁，避免重复累计。
	result := r.usage.Record(info.profileID, info.id, received, sent)
	if result.Rollback {
		r.log.Warn("usage counter went backwards", "cid", cid, "session_id", info.id)
	}
	profile, hasProfile = findProfile(r.snapshot, info.profileID)
	if hasProfile && profile.QuotaBytes > 0 && r.usage.Usage(profile.ID) >= profile.QuotaBytes {
		info.blocked = true
		info.blockReason = "quota_exhausted"
		quotaKill = true
	}
	eventDeltaUp = deltaSince(received, info.eventUp)
	eventDeltaDown = deltaSince(sent, info.eventDown)
	shouldEvent := eventDeltaUp+eventDeltaDown > 0 &&
		(now.Sub(info.lastEvent) >= time.Minute || eventDeltaUp+eventDeltaDown >= 1<<20)
	if shouldEvent {
		// 在会话锁内写入事件并推进事件高水位，保证结束事件不会重复或漏计。
		if _, err := r.usage.AppendEvent(telemetry.Event{
			Kind:        telemetry.EventUsage,
			At:          now,
			SessionID:   info.id,
			CID:         cid,
			ProfileID:   info.profileID,
			VPNIP:       info.vpnIP,
			SourceIP:    info.sourceIP,
			UpBytes:     info.upBytes,
			DownBytes:   info.downBytes,
			DeltaUp:     eventDeltaUp,
			DeltaDown:   eventDeltaDown,
			ConnectedAt: timePtr(info.connectedAt),
		}); err != nil {
			r.log.Warn("append usage event failed", "error", err, "cid", cid)
		} else {
			info.lastEvent = now
			info.eventUp = received
			info.eventDown = sent
		}
	}
	r.sessions[cid] = info
	r.mu.Unlock()

	if quotaKill {
		r.killProfile(info.profileID, "quota_exhausted")
	}
}

// finishSession 结束一条会话：先按需断开，再记录最终用量与结束事件。
func (r *Runtime) finishSession(cid, reason string, kill bool) {
	if kill {
		// 不附带自由文本：OpenVPN 会把附加文本当作控制通道消息发给客户端。
		if err := r.send(fmt.Sprintf("client-kill %s", cid)); err != nil {
			r.log.Warn("send client-kill failed", "error", err)
		}
	}
	now := r.now()
	r.mu.Lock()
	info, ok := r.sessions[cid]
	if ok {
		delete(r.sessions, cid)
		delete(r.pendingAddr, cid)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	r.endSessionRecord(cid, info, reason, now)
	if err := r.refreshPolicy(); err != nil {
		r.log.Warn("refresh policy after session end failed", "error", err, "cid", cid)
	}
	if err := r.refreshTraffic(); err != nil {
		r.log.Warn("refresh shaping after session end failed", "error", err, "cid", cid)
	}
	r.log.Warn("session ended", "cid", cid, "profile_id", info.profileID, "reason", reason)
}

// endSessionRecord 为已经移出在线映射的会话写入最终用量与结束事件。
func (r *Runtime) endSessionRecord(cid string, info sessionInfo, reason string, now time.Time) {
	// 账本记录最终高水位；日汇总使用相对上一条事件的增量，避免丢失窗口。
	r.usage.Record(info.profileID, info.id, info.upBytes, info.downBytes)
	if _, err := r.usage.AppendEvent(telemetry.Event{
		Kind:        telemetry.EventSessionEnd,
		At:          now,
		SessionID:   info.id,
		CID:         cid,
		ProfileID:   info.profileID,
		VPNIP:       info.vpnIP,
		SourceIP:    info.sourceIP,
		UpBytes:     info.upBytes,
		DownBytes:   info.downBytes,
		DeltaUp:     deltaSince(info.upBytes, info.eventUp),
		DeltaDown:   deltaSince(info.downBytes, info.eventDown),
		Reason:      reason,
		ConnectedAt: timePtr(info.connectedAt),
	}); err != nil {
		r.log.Warn("append session end event failed", "error", err, "cid", cid)
	}
	r.usage.RemoveSession(info.profileID, info.id)
	if err := r.usage.Save(); err != nil {
		r.log.Warn("persist usage ledger failed", "error", err)
	}
}

// killProfile 立即断开某配置的全部在线连接，用于额度耗尽或权限收紧。
func (r *Runtime) killProfile(profileID, reason string) {
	r.mu.Lock()
	cids := make([]string, 0, 4)
	for cid, info := range r.sessions {
		if info.profileID == profileID {
			cids = append(cids, cid)
		}
	}
	r.mu.Unlock()
	for _, cid := range cids {
		r.finishSession(cid, reason, true)
	}
}

// endAllSessions 在进程停止或重启前记录全部会话结束。
func (r *Runtime) endAllSessions(reason string) {
	now := r.now()
	r.mu.Lock()
	sessions := make(map[string]sessionInfo, len(r.sessions))
	for cid, info := range r.sessions {
		sessions[cid] = info
	}
	r.sessions = map[string]sessionInfo{}
	r.pendingAddr = map[string]string{}
	r.mu.Unlock()
	for cid, info := range sessions {
		r.endSessionRecord(cid, info, reason, now)
	}
}

func (r *Runtime) pruneSessions() {
	r.mu.Lock()
	snapshot := r.snapshot
	sessions := make(map[string]sessionInfo, len(r.sessions))
	for cid, info := range r.sessions {
		sessions[cid] = info
	}
	r.mu.Unlock()

	now := r.now()
	type kill struct {
		cid    string
		reason string
	}
	kills := make([]kill, 0)
	for cid, info := range sessions {
		profile, ok := findProfile(snapshot, info.profileID)
		if !ok {
			kills = append(kills, kill{cid: cid, reason: "policy_revoked"})
			continue
		}
		if profile.ExpiresAt != nil && !now.Before(*profile.ExpiresAt) {
			kills = append(kills, kill{cid: cid, reason: "profile_expired"})
			continue
		}
		if info.certSerial != "" &&
			(profile.CertSerial == "" || !admission.SerialMatches(profile.CertSerial, info.certSerial) ||
				profile.CertStatus != "active") {
			kills = append(kills, kill{cid: cid, reason: "certificate_changed"})
			continue
		}
		if profile.CertExpiresAt != nil && !now.Before(*profile.CertExpiresAt) {
			kills = append(kills, kill{cid: cid, reason: "certificate_expired"})
			continue
		}
		if !admission.ProfileUsable(snapshot, info.profileID, now) {
			kills = append(kills, kill{cid: cid, reason: "policy_revoked"})
			continue
		}
		if profile.QuotaBytes > 0 && r.usage.Exhausted(profile.ID, profile.QuotaBytes) {
			kills = append(kills, kill{cid: cid, reason: "quota_exhausted"})
		}
	}
	for _, item := range kills {
		r.finishSession(item.cid, item.reason, true)
	}
}

func (r *Runtime) send(command string) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.mu.Lock()
	writer := r.mgmtWriter
	r.mu.Unlock()
	if writer == nil {
		return errors.New("management not connected")
	}
	if _, err := writer.WriteString(command + "\n"); err != nil {
		return err
	}
	return writer.Flush()
}

func (r *Runtime) stopProcess() {
	r.mu.Lock()
	cancel := r.mgmtCancel
	conn := r.mgmtConn
	proc := r.proc
	done := r.procDone
	r.mgmtCancel = nil
	r.mgmtConn = nil
	r.mgmtWriter = nil
	r.managementConnected = false
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if conn != nil {
		conn.Close()
	}
	if proc != nil && proc.Process != nil {
		_ = proc.Process.Signal(syscall.SIGTERM)
		if done != nil {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = proc.Process.Kill()
			}
		}
	}

	// OpenVPN 退出后在线会话全部结束；先记录最终用量与结束事件。
	r.endAllSessions("runtime_stop")

	r.mu.Lock()
	if r.proc == proc {
		r.proc = nil
		r.procDone = nil
		r.openvpnRunning = false
	}
	r.sessions = map[string]sessionInfo{}
	r.mu.Unlock()
	if err := r.usage.Save(); err != nil {
		r.log.Warn("persist usage ledger failed", "error", err)
	}
}

// Stop 停止 OpenVPN、管理连接、限速队列与项目专用 nftables 表。
func (r *Runtime) Stop() {
	r.stopProcess()
	r.clearTraffic()
	if err := netpolicy.Delete(); err != nil {
		r.log.Warn("delete network policy failed", "error", err)
	}
	r.usage.Close()
}

func (r *Runtime) applyPolicy(policy netpolicy.Policy) error {
	if r.applyPolicyFn != nil {
		return r.applyPolicyFn(policy)
	}
	policy.ProtectTargets = append(policy.ProtectTargets, localProtectCIDRs(policy.VPNPool)...)
	script, err := netpolicy.Render(policy)
	if err != nil {
		return err
	}
	return netpolicy.Apply(script)
}

func (r *Runtime) startVPN(ctx context.Context, configHash string) error {
	if r.startVPNFn != nil {
		return r.startVPNFn(ctx, configHash)
	}
	return r.restartOpenVPN(ctx, configHash)
}

// localProtectCIDRs 保护容器自身的 Docker 网络与回环地址，
// 不因管理员授权了某个内网网段而暴露控制面。
func localProtectCIDRs(vpnPool string) []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	poolIP, poolNet, _ := net.ParseCIDR(vpnPool)
	_ = poolIP
	out := make([]string, 0)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		if strings.HasPrefix(iface.Name, "tun") || strings.HasPrefix(iface.Name, "ifb") {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip, network, err := net.ParseCIDR(addr.String())
			if err != nil || ip.To4() == nil {
				continue
			}
			if poolNet != nil && (poolNet.Contains(ip) || network.Contains(poolNet.IP)) {
				continue
			}
			out = append(out, network.String())
		}
	}
	return out
}

// Ready 表示 OpenVPN 与准入管理接口都已就绪。
func (r *Runtime) Ready() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.configured && !r.stopped && r.openvpnRunning && r.managementConnected
}

// Status 返回当前状态快照。
func (r *Runtime) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := Status{
		Configured:          r.configured,
		Stopped:             r.stopped,
		OpenVPNRunning:      r.openvpnRunning,
		ManagementConnected: r.managementConnected,
		ConfigHash:          r.configHash,
		Sessions:            len(r.sessions),
		UsageGeneration:     r.usage.Generation(),
		UsageSeq:            r.usage.CurrentSeq(),
		UsageHealthy:        r.usageHealthy,
		UsageError:          r.usageError,
		LastError:           r.lastError,
		RolledBack:          r.rolledBack,
		RollbackReason:      r.rollbackReason,
	}
	if !r.appliedAt.IsZero() {
		applied := r.appliedAt
		status.AppliedAt = &applied
	}
	return status
}

func (r *Runtime) setError(err error) {
	r.mu.Lock()
	r.lastError = err.Error()
	r.mu.Unlock()
}

func (r *Runtime) clearError() {
	r.mu.Lock()
	r.lastError = ""
	r.mu.Unlock()
}

func incomingFromEnv(env map[string]string) admission.Incoming {
	incoming := admission.Incoming{
		CommonName: env["common_name"],
		Username:   env["username"],
		Password:   env["password"],
	}
	switch {
	case env["trusted_ip"] != "":
		incoming.SourceIP = env["trusted_ip"]
	case env["untrusted_ip"] != "":
		incoming.SourceIP = env["untrusted_ip"]
	case env["remote_host"] != "":
		incoming.SourceIP = env["remote_host"]
	}
	serial := env["tls_serial_hex_0"]
	if serial == "" {
		serial = env["tls_serial_0"]
	}
	fingerprint := env["tls_digest_sha256_0"]
	if fingerprint == "" {
		fingerprint = env["tls_digest_0"]
	}
	if serial != "" || fingerprint != "" {
		incoming.CertPresent = true
		incoming.CertVerified = true
		incoming.CertSerial = serial
		incoming.CertFingerprint = fingerprint
	}
	return incoming
}

func writeSecure(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建目录 %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("创建临时文件 %s: %w", path, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入 %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("替换 %s: %w", path, err)
	}
	return os.Chmod(path, 0o600)
}

func waitForSocket(ctx context.Context, path string, done <-chan struct{}, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return errors.New("OpenVPN 在管理接口就绪前退出")
		case <-deadline.C:
			return fmt.Errorf("等待管理接口超时：%s", path)
		case <-ticker.C:
		}
	}
}

func escapeMgmtArg(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
