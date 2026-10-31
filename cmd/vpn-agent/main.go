// Command vpn-agent 是 OpenVPN 容器内的运行代理。
//
// 代理读取共享控制目录中的配置包，应用网络策略、启动 OpenVPN 并执行
// 客户端准入；限速和用量记录在后续阶段接入。进程不会把「环境可用」
// 说成「VPN 已配置」，接口中用 configured 明确区分。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/dreamstation625/ovpanel/internal/agent"
	"github.com/dreamstation625/ovpanel/internal/logging"
	"github.com/dreamstation625/ovpanel/internal/runtimeenv"
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

const (
	envListen     = "OVPANEL_AGENT_LISTEN"
	envStateDir   = "OVPANEL_AGENT_STATE_DIR"
	envControlDir = "OVPANEL_CONTROL_DIR"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "vpn-agent: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	listen := getenv(envListen, ":9443")
	stateDir := getenv(envStateDir, "/var/lib/ovpanel")
	controlDir := getenv(envControlDir, "/control")

	log := logging.New(os.Stdout, getenv("OVPANEL_LOG_LEVEL", "info"))
	slog.SetDefault(log)

	report := runtimeenv.Probe()
	log.Info("runtime environment probed",
		"summary", report.String(),
		"state_dir", stateDir,
		"environment_ready", report.EnvironmentReady)
	for _, blocker := range report.Blockers {
		log.Warn("runtime environment blocker", "reason", blocker)
	}

	runtime := agent.New(controlDir, stateDir, log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go runtime.Run(ctx)

	handler := newHandler(report, runtime)
	server := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	log.Info("vpn-agent starting",
		"version", version,
		"listen", listen,
		"environment_ready", report.EnvironmentReady,
		"control_dir", controlDir)

	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP 服务异常退出: %w", err)
		}
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		server.Close()
		return fmt.Errorf("优雅关闭超时: %w", err)
	}
	runtime.Stop()
	log.Info("vpn-agent stopped")
	return nil
}

// newHandler 组装代理的只读接口；配置与命令只通过共享控制目录传递。
func newHandler(report runtimeenv.Report, rt *agent.Runtime) http.Handler {
	status := func() agent.Status {
		if rt == nil {
			return agent.Status{}
		}
		return rt.Status()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":           "ok",
			"version":          version,
			"environmentReady": report.EnvironmentReady,
			"configured":       status().Configured,
			"blockers":         report.Blockers,
		})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		runtimeReady := rt == nil || rt.Ready()
		code := http.StatusOK
		if !report.EnvironmentReady || !runtimeReady {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]any{
			"environmentReady": report.EnvironmentReady,
			"configured":       status().Configured,
			"openvpnRunning":   status().OpenVPNRunning,
			"managementReady":  status().ManagementConnected,
			"blockers":         report.Blockers,
		})
	})
	mux.HandleFunc("GET /v1/environment", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"version":   version,
			"commit":    commit,
			"buildTime": buildTime,
			"goVersion": runtime.Version(),
			"message":   "运行代理；OpenVPN 配置与准入由面板下发的控制包驱动",
			"report":    report,
			"runtime":   status(),
		})
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, status())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]string{"code": "not_found", "message": "接口不存在"},
		})
	})
	return withHeaders(mux)
}

func withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
