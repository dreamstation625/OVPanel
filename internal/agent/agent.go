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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dreamstation625/ovpanel/internal/admission"
	"github.com/dreamstation625/ovpanel/internal/agentproto"
	"github.com/dreamstation625/ovpanel/internal/netpolicy"
	"github.com/dreamstation625/ovpanel/internal/serverconfig"
)

const (
	bundleFile       = "bundle.json"
	commandFile      = "command.json"
	lastGoodFile     = "last-good.json"
	stoppedFile      = "stopped"
	defaultConfigDir = "/etc/openvpn/ovpanel"
	managementDir    = "/run/ovpanel"
	managementSocket = serverconfig.ManagementSocket
	openvpnLogFile   = "openvpn.log"
)

type sessionInfo struct {
	profileID  string
	certSerial string
}

// Status 是运行代理当前应用状态。
type Status struct {
	Configured          bool       `json:"configured"`
	Stopped             bool       `json:"stopped,omitempty"`
	OpenVPNRunning      bool       `json:"openvpnRunning"`
	ManagementConnected bool       `json:"managementConnected"`
	ConfigHash          string     `json:"configHash,omitempty"`
	Sessions            int        `json:"sessions"`
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
	lastError           string
	appliedAt           time.Time

	proc       *exec.Cmd
	procDone   chan struct{}
	mgmtCancel context.CancelFunc
	mgmtConn   net.Conn
	mgmtWriter *bufio.Writer
	writeMu    sync.Mutex

	// 测试替换点；生产环境使用下方真实实现。
	applyPolicyFn func(netpolicy.Policy) error
	startVPNFn    func(context.Context, string) error
}

// New 创建运行代理。
func New(controlDir, stateDir string, log *slog.Logger) *Runtime {
	return &Runtime{
		controlDir: controlDir,
		stateDir:   stateDir,
		configDir:  defaultConfigDir,
		log:        log,
		now:        func() time.Time { return time.Now().UTC() },
		sessions:   map[string]sessionInfo{},
	}
}

// Run 持续监听控制包，直到 ctx 结束。
func (r *Runtime) Run(ctx context.Context) {
	r.loadPersistedState()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := r.handleCommand(ctx); err != nil {
			r.setError(err)
			r.log.Warn("handle control command failed", "error", err)
		}
		if err := r.applyOnce(ctx); err != nil {
			r.setError(err)
			r.log.Warn("apply control bundle failed", "error", err)
		}
		r.writeStatus()
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
	if err := r.applyPolicy(bundle.Policy); err != nil {
		return err
	}

	r.mu.Lock()
	r.snapshot = bundle.Snapshot
	r.configured = true
	r.appliedAt = bundle.IssuedAt
	needsRestart := r.configHash != bundle.ConfigHash || !r.openvpnRunning || !r.managementConnected
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

func (r *Runtime) loadPersistedState() {
	r.loadLastGood()
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
	r.mu.Unlock()

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
		parts := strings.Split(rest, ",")
		switch parts[0] {
		case "CLIENT:CONNECT", "CLIENT:REAUTH":
			if len(parts) >= 3 {
				pending = &pendingAuth{cid: parts[1], kid: parts[2], env: map[string]string{}}
			}
		case "CLIENT:DISCONNECT":
			if len(parts) >= 2 {
				r.mu.Lock()
				delete(r.sessions, parts[1])
				r.mu.Unlock()
			}
		}
	}
}

type pendingAuth struct {
	cid string
	kid string
	env map[string]string
}

func (r *Runtime) evaluate(pending *pendingAuth) {
	r.mu.Lock()
	snapshot := r.snapshot
	r.mu.Unlock()

	incoming := incomingFromEnv(pending.env)
	decision := admission.Decide(snapshot, incoming, r.now())
	if decision.Allow {
		if err := r.send(fmt.Sprintf("client-auth-nt %s %s", pending.cid, pending.kid)); err != nil {
			r.log.Warn("send client-auth-nt failed", "error", err)
			return
		}
		r.mu.Lock()
		r.sessions[pending.cid] = sessionInfo{
			profileID:  decision.ProfileID,
			certSerial: incoming.CertSerial,
		}
		r.mu.Unlock()
		r.log.Info("client admitted",
			"profile_id", decision.ProfileID,
			"cid", pending.cid,
			"auth", decision.Code)
		return
	}

	message := escapeMgmtArg(decision.Reason)
	if err := r.send(fmt.Sprintf("client-deny %s %s %q %q", pending.cid, pending.kid, decision.Code, message)); err != nil {
		r.log.Warn("send client-deny failed", "error", err)
	}
	r.log.Warn("client denied",
		"cid", pending.cid,
		"code", decision.Code,
		"reason", decision.Reason)
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
	for cid, info := range sessions {
		kill := !admission.ProfileUsable(snapshot, info.profileID, now)
		if !kill && info.certSerial != "" {
			var profile *admission.Profile
			for i := range snapshot.Profiles {
				if snapshot.Profiles[i].ID == info.profileID {
					profile = &snapshot.Profiles[i]
					break
				}
			}
			if profile == nil || profile.CertSerial == "" || !admission.SerialMatches(profile.CertSerial, info.certSerial) {
				kill = true
			}
		}
		if kill {
			// 不附带自由文本：OpenVPN 会把附加文本当作控制通道消息发给客户端，
			// 客户端只会记录 unknown control message。断开原因保留在代理日志。
			if err := r.send(fmt.Sprintf("client-kill %s", cid)); err != nil {
				r.log.Warn("send client-kill failed", "error", err)
			}
			r.mu.Lock()
			delete(r.sessions, cid)
			r.mu.Unlock()
			r.log.Warn("session killed after policy update", "cid", cid, "profile_id", info.profileID)
		}
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

	r.mu.Lock()
	if r.proc == proc {
		r.proc = nil
		r.procDone = nil
		r.openvpnRunning = false
	}
	r.sessions = map[string]sessionInfo{}
	r.mu.Unlock()
}

// Stop 停止 OpenVPN、管理连接与项目专用 nftables 表。
func (r *Runtime) Stop() {
	r.stopProcess()
	if err := netpolicy.Delete(); err != nil {
		r.log.Warn("delete network policy failed", "error", err)
	}
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
