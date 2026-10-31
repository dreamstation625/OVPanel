// Package vpnruntime 组装运行代理所需的完整控制包。
//
// 面板只负责生成配置、证书材料、tls-crypt 与认证快照，并原子写入共享控制目录；
// OpenVPN 进程启动与准入回调由 vpn-agent 执行。写入文件是“已下发”，
// 不代表代理已经确认应用。
package vpnruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dreamstation625/ovpanel/internal/accounts"
	"github.com/dreamstation625/ovpanel/internal/agentproto"
	"github.com/dreamstation625/ovpanel/internal/netpolicy"
	"github.com/dreamstation625/ovpanel/internal/pki"
	"github.com/dreamstation625/ovpanel/internal/runtimeauth"
	"github.com/dreamstation625/ovpanel/internal/serverconfig"
	"github.com/dreamstation625/ovpanel/internal/store"
	"github.com/google/uuid"
)

// BundleFileName 是控制包文件名。
const BundleFileName = "bundle.json"

// Service 生成并下发运行控制包。
type Service struct {
	store      *store.Store
	pki        *pki.Service
	accounts   *accounts.Service
	controlDir string
	now        func() time.Time
}

// New 创建运行控制包服务。
func New(st *store.Store, pkiService *pki.Service, accountsService *accounts.Service, controlDir string) *Service {
	return &Service{
		store:      st,
		pki:        pkiService,
		accounts:   accountsService,
		controlDir: controlDir,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// Result 描述本次下发的控制包。
type Result struct {
	ConfigRevision int64
	ConfigHash     string
	Users          int
	Profiles       int
	BundlePath     string
	// AgentPending 表示控制包已落盘但尚未收到代理应用确认。
	AgentPending bool
}

// AgentStatus 读取运行代理写入控制目录的状态；尚未写入时返回 ok=false。
func (s *Service) AgentStatus() (map[string]any, bool) {
	data, err := os.ReadFile(filepath.Join(s.controlDir, "status.json"))
	if err != nil {
		return nil, false
	}
	var status map[string]any
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, false
	}
	return status, true
}

// Command 写入启停、重启或备份/恢复相关控制命令。
func (s *Service) Command(action string) error {
	switch action {
	case "start", "stop", "restart", "checkpoint", "restore-usage":
	default:
		return fmt.Errorf("未知控制命令：%q", action)
	}
	return s.writeCommand(map[string]string{
		"id":       uuid.NewString(),
		"action":   action,
		"issuedAt": s.now().Format(time.RFC3339),
	})
}

// Disconnect 写入断开指定 CID 会话的控制命令。
func (s *Service) Disconnect(cid string) error {
	if cid == "" {
		return fmt.Errorf("断开命令缺少 CID")
	}
	return s.writeCommand(map[string]string{
		"id":       uuid.NewString(),
		"action":   "disconnect",
		"cid":      cid,
		"issuedAt": s.now().Format(time.RFC3339),
	})
}

func (s *Service) writeCommand(command map[string]string) error {
	if err := os.MkdirAll(s.controlDir, 0o700); err != nil {
		return fmt.Errorf("创建控制目录: %w", err)
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.controlDir, ".command-*.tmp")
	if err != nil {
		return fmt.Errorf("创建控制命令文件: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(s.controlDir, "command.json")); err != nil {
		return fmt.Errorf("写入控制命令: %w", err)
	}
	return nil
}

// Apply 使用当前生效配置（没有时用默认值）生成控制包。
func (s *Service) Apply(ctx context.Context) (Result, error) {
	state, err := s.store.ServerConfigState(ctx)
	if err != nil {
		return Result{}, err
	}

	config := serverconfig.Defaults()
	revision := int64(0)
	if state.Active != nil {
		if err := json.Unmarshal([]byte(state.Active.ConfigJSON), &config); err != nil {
			return Result{}, fmt.Errorf("解析生效服务端配置: %w", err)
		}
		revision = state.Active.Revision
	}
	return s.ApplyConfig(ctx, config, revision)
}

// ApplyConfig 用指定配置生成控制包并原子写入控制目录。
func (s *Service) ApplyConfig(ctx context.Context, config serverconfig.Config, revision int64) (Result, error) {
	prepared, err := serverconfig.Prepare(config)
	if err != nil {
		return Result{}, err
	}
	rendered, configHash, err := serverconfig.Render(prepared)
	if err != nil {
		return Result{}, err
	}

	exportSettings, configured, err := s.accounts.GetExportSettings(ctx)
	if err != nil {
		return Result{}, err
	}
	if !configured {
		return Result{}, accounts.ErrExportNotConfigured
	}
	if exportSettings.Port != config.Port || exportSettings.Proto != config.Protocol {
		return Result{}, fmt.Errorf("导出连接设置与服务端端口/协议不一致（导出 %d/%s，服务端 %d/%s）",
			exportSettings.Port, exportSettings.Proto, config.Port, config.Protocol)
	}

	serverCert, serverKey, err := s.pki.EnsureServerCertificate(ctx)
	if err != nil {
		return Result{}, err
	}
	caPEM, err := s.pki.CAPEM(ctx)
	if err != nil {
		return Result{}, err
	}
	tlsKey, err := s.accounts.TLSCryptKey(ctx)
	if err != nil {
		return Result{}, err
	}
	crlPEM, err := s.pki.CRLPEM(ctx)
	if err != nil {
		return Result{}, err
	}
	snapshot, err := runtimeauth.Build(ctx, s.store)
	if err != nil {
		return Result{}, err
	}

	bundle := agentproto.Bundle{
		Version:        agentproto.Version,
		ConfigRevision: revision,
		ConfigHash:     configHash,
		Config:         rendered,
		CAPEM:          caPEM,
		ServerCertPEM:  serverCert,
		ServerKeyPEM:   serverKey,
		TLSCryptKey:    tlsKey,
		CRLPEM:         crlPEM,
		Policy: netpolicy.Policy{
			VPNPool:          prepared.VPNNetwork,
			TunDevice:        "tun0",
			AllowInterClient: prepared.AllowInterClient,
			GlobalExit:       prepared.RouteMode == serverconfig.RouteGlobal,
			BlockIPv6:        prepared.BlockIPv6,
			AllowedInternal:  prepared.AllowedInternal,
		},
		Snapshot: snapshot,
		IssuedAt: s.now(),
	}
	payload, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("编码控制包: %w", err)
	}

	if err := os.MkdirAll(s.controlDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("创建控制目录: %w", err)
	}
	if err := os.Chmod(s.controlDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("收紧控制目录权限: %w", err)
	}

	tmp, err := os.CreateTemp(s.controlDir, ".bundle-*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("创建控制包临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return Result{}, fmt.Errorf("设置控制包权限: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return Result{}, fmt.Errorf("写入控制包: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Result{}, fmt.Errorf("同步控制包: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Result{}, fmt.Errorf("关闭控制包: %w", err)
	}

	target := filepath.Join(s.controlDir, BundleFileName)
	if err := os.Rename(tmpName, target); err != nil {
		return Result{}, fmt.Errorf("替换控制包: %w", err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		return Result{}, fmt.Errorf("控制包权限: %w", err)
	}

	return Result{
		ConfigRevision: revision,
		ConfigHash:     configHash,
		Users:          len(snapshot.Users),
		Profiles:       len(snapshot.Profiles),
		BundlePath:     target,
		AgentPending:   true,
	}, nil
}
