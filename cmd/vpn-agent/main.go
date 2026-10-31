// Command vpn-agent 是 OpenVPN 容器内的运行代理骨架。
//
// P02 只提供进程、健康检查与数据面能力报告；OpenVPN 的启动、准入、
// 限速和用量记录在 P04/P05 接入。当前进程不会把「环境可用」说成
// 「VPN 已配置」，接口中用 configured 明确区分。
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

	"github.com/dreamstation625/ovpanel/internal/logging"
	"github.com/dreamstation625/ovpanel/internal/runtimeenv"
)

var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

const (
	envListen   = "OVPANEL_AGENT_LISTEN"
	envStateDir = "OVPANEL_AGENT_STATE_DIR"
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

	handler := newHandler(report)
	server := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("vpn-agent starting",
		"version", version,
		"listen", listen,
		"environment_ready", report.EnvironmentReady,
		"configured", false)

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
	log.Info("vpn-agent stopped")
	return nil
}

// newHandler 组装代理的只读接口。P02 不提供任何配置或命令入口。
func newHandler(report runtimeenv.Report) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":           "ok",
			"version":          version,
			"environmentReady": report.EnvironmentReady,
			"configured":       false,
			"blockers":         report.Blockers,
		})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		status := http.StatusOK
		if !report.EnvironmentReady {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{
			"environmentReady": report.EnvironmentReady,
			"configured":       false,
			"blockers":         report.Blockers,
		})
	})
	mux.HandleFunc("GET /v1/environment", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"version":    version,
			"commit":     commit,
			"buildTime":  buildTime,
			"goVersion":  runtime.Version(),
			"configured": false,
			"message":    "P02 运行骨架；OpenVPN 数据面在后续阶段启用",
			"report":     report,
		})
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
