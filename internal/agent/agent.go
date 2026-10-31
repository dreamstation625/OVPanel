// Package agent 在 OpenVPN 容器内应用面板下发的控制包并执行准入。
//
// 控制包由面板写入共享控制目录；本包只读该目录，不接收网络控制请求。
// OpenVPN 管理接口只绑定容器内 Unix Socket，准入由本代理完成。
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
	"github.com/dreamstation625/ovpanel/internal/serverconfig"
)

const (
	bundleFile       = "bundle.json"
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
	OpenVPNRunning      bool       `json:"openvpnRunning"`
	ManagementConnected bool       `json:"managementConnected"`
	ConfigHash          string     `json:"configHash,omitempty"`
	Sessions            int        `json:"sessions"`
	LastError           string     `json:"lastError,omitempty"`
	AppliedAt           *time.Time `json:"appliedAt,omitempty"`
}

// Runtime 管理控制包、OpenVPN 子进程与准入。
type Runtime struct {
	controlDir string
	stateDir   string
	log        *slog.Logger
	now        func() time.Time

	mu                  sync.Mutex
	snapshot            admission.Snapshot
	bundleHash          string
	configHash          string
	openvpnRunning      bool
	managementConnected bool
	configured          bool
	sessions            map[string]sessionInfo
	lastError           string
	appliedAt           time.Time

	proc       *exec.Cmd
	procDone   chan struct{}
	mgmtCancel context.CancelFunc
	mgmtConn   net.Conn
	mgmtWriter *bufio.Writer
	writeMu    sync.Mutex
}

// New 创建运行代理。
func New(controlDir, stateDir string, log *slog.Logger) *Runtime {
	return &Runtime{
		controlDir: controlDir,
		stateDir:   stateDir,
		log:        log,
		now:        func() time.Time { return time.Now().UTC() },
		sessions:   map[string]sessionInfo{},
	}
}

// Run 持续监听控制包，直到 ctx 结束。
func (r *Runtime) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := r.applyOnce(ctx); err != nil {
			r.setError(err)
			r.log.Warn("apply control bundle failed", "error", err)
		}
		select {
		case <-ctx.Done():
			r.Stop()
			return
		case <-ticker.C:
		}
	}
}

func (r *Runtime) applyOnce(ctx context.Context) error {
	path := filepath.Join(r.controlDir, bundleFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取控制包: %w", err)
	}
	var bundle agentproto.Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return fmt.Errorf("解析控制包: %w", err)
	}
	if bundle.Version != agentproto.Version {
		return fmt.Errorf("控制包版本不支持：%d", bundle.Version)
	}
	sum := sha256.Sum256([]byte(bundle.Config))
	if hex.EncodeToString(sum[:]) != bundle.ConfigHash {
		return fmt.Errorf("控制包配置哈希不匹配")
	}
	if bundle.CAPEM == "" || bundle.ServerCertPEM == "" || bundle.ServerKeyPEM == "" || bundle.TLSCryptKey == "" || bundle.CRLPEM == "" {
		return fmt.Errorf("控制包材料不完整")
	}
	if err := bundle.Snapshot.Validate(); err != nil {
		return fmt.Errorf("认证快照非法: %w", err)
	}

	bundleHash := fmt.Sprintf("%x", sha256.Sum256(data))
	r.mu.Lock()
	already := r.bundleHash == bundleHash && r.openvpnRunning && r.managementConnected
	r.mu.Unlock()
	if already {
		return nil
	}

	if err := r.writeMaterials(bundle); err != nil {
		return err
	}

	r.mu.Lock()
	r.snapshot = bundle.Snapshot
	r.bundleHash = bundleHash
	r.configured = true
	r.appliedAt = bundle.IssuedAt
	r.mu.Unlock()

	r.mu.Lock()
	needsRestart := r.configHash != bundle.ConfigHash || !r.openvpnRunning || !r.managementConnected
	r.mu.Unlock()

	if needsRestart {
		if err := r.restartOpenVPN(ctx, bundle.ConfigHash); err != nil {
			return err
		}
	} else {
		r.pruneSessions()
	}
	r.clearError()
	r.log.Info("control bundle applied",
		"config_hash", bundle.ConfigHash,
		"profiles", len(bundle.Snapshot.Profiles),
		"users", len(bundle.Snapshot.Users),
		"restarted", needsRestart)
	return nil
}

func (r *Runtime) writeMaterials(bundle agentproto.Bundle) error {
	if err := os.MkdirAll("/etc/openvpn/ovpanel", 0o700); err != nil {
		return fmt.Errorf("创建证书目录: %w", err)
	}
	files := map[string]string{
		serverconfig.PathCA:                                  bundle.CAPEM,
		serverconfig.PathServerCert:                          bundle.ServerCertPEM,
		serverconfig.PathServerKey:                           bundle.ServerKeyPEM,
		serverconfig.PathTLSCrypt:                            bundle.TLSCryptKey,
		serverconfig.PathCRL:                                 bundle.CRLPEM,
		filepath.Join("/etc/openvpn/ovpanel", "server.conf"): bundle.Config,
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

	cmd := exec.Command("openvpn", "--config", filepath.Join("/etc/openvpn/ovpanel", "server.conf"))
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

// Stop 停止 OpenVPN 与管理连接。
func (r *Runtime) Stop() {
	r.stopProcess()
}

// Ready 表示 OpenVPN 与准入管理接口都已就绪。
func (r *Runtime) Ready() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.configured && r.openvpnRunning && r.managementConnected
}

// Status 返回当前状态快照。
func (r *Runtime) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := Status{
		Configured:          r.configured,
		OpenVPNRunning:      r.openvpnRunning,
		ManagementConnected: r.managementConnected,
		ConfigHash:          r.configHash,
		Sessions:            len(r.sessions),
		LastError:           r.lastError,
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
