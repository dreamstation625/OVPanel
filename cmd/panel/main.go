// Command panel 是 OVPanel 的唯一 Web 进程入口。
//
// 一个二进制同时提供 HTTP(S) 接口与内嵌前端资源，部署时不需要 Node.js、
// 反向代理或额外的静态文件服务器。
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/dreamstation625/ovpanel/internal/api"
	"github.com/dreamstation625/ovpanel/internal/bootstrap"
	"github.com/dreamstation625/ovpanel/internal/config"
	"github.com/dreamstation625/ovpanel/internal/devtls"
	"github.com/dreamstation625/ovpanel/internal/logging"
	"github.com/dreamstation625/ovpanel/internal/store"
	"github.com/dreamstation625/ovpanel/web"
)

// 链接期注入的构建信息。
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ovpanel: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("加载配置: %w", err)
	}

	log := logging.New(os.Stdout, cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	var cleanupWG sync.WaitGroup
	defer func() {
		// 先停止后台会话清理并等待它退出，再关闭数据库。
		stop()
		cleanupWG.Wait()
		if err := st.Close(); err != nil {
			log.Warn("close database failed", "error", err)
		}
	}()

	schema, err := st.Migrate(ctx)
	if err != nil {
		return fmt.Errorf("应用数据库迁移: %w", err)
	}
	log.Info("database ready", "path", st.Path(), "schema", schema)

	initialized, err := st.AdminExists(ctx)
	if err != nil {
		return err
	}

	token, err := bootstrap.LoadOrCreate(cfg.BootstrapTokenPath(), cfg.BootstrapToken, initialized)
	if err != nil {
		return err
	}
	switch {
	case token.Generated:
		log.Warn("bootstrap token created",
			"path", token.Path,
			"note", "远端初始化需要它；可在容器内执行 cat 读取，初始化成功后自动删除")
	case token.FromEnv:
		log.Info("bootstrap token loaded from environment")
	case token.Path != "" && !initialized:
		log.Info("bootstrap token reused", "path", token.Path)
	case initialized:
		log.Info("panel already initialized; setup endpoint is closed")
	}

	webFS := web.Assets()
	if _, err := fs.Stat(webFS, "index.html"); err != nil {
		log.Warn("frontend assets are not built",
			"note", "在 web 目录执行 npm ci && npm run build 后再运行；当前只有 API 可用")
	}

	srv := api.New(api.Options{
		Config: cfg,
		Store:  st,
		Log:    log,
		Build: api.BuildInfo{
			Version:   version,
			Commit:    commit,
			BuildTime: buildTime,
			GoVersion: runtime.Version(),
		},
		BootstrapToken: token.Value,
		WebFS:          webFS,
		OnSetupComplete: func() error {
			if token.Path == "" {
				return nil
			}
			return bootstrap.Remove(token.Path)
		},
	})

	cleanupWG.Add(1)
	go func() {
		defer cleanupWG.Done()
		cleanupSessions(ctx, st, log)
	}()

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	scheme := "http"
	switch {
	case cfg.DevSelfSigned:
		cert, err := devtls.Certificate()
		if err != nil {
			return err
		}
		httpServer.TLSConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
		scheme = "https"
		log.Warn("using development self-signed certificate",
			"note", "仅用于本机调试；正式部署请挂载受信任证书")
	case cfg.TLSCertFile != "":
		httpServer.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		scheme = "https"
	}

	log.Info("panel starting",
		"version", version,
		"listen", cfg.Listen,
		"scheme", scheme,
		"data_dir", cfg.DataDir)

	errCh := make(chan error, 1)
	go func() {
		var serveErr error
		switch {
		case cfg.DevSelfSigned:
			serveErr = httpServer.ListenAndServeTLS("", "")
		case cfg.TLSCertFile != "":
			serveErr = httpServer.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		default:
			serveErr = httpServer.ListenAndServe()
		}
		errCh <- serveErr
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP 服务异常退出: %w", err)
		}
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		httpServer.Close()
		return fmt.Errorf("优雅关闭超时: %w", err)
	}
	log.Info("panel stopped")
	return nil
}

// cleanupSessions 定期清理过期会话。启动时先执行一次，避免长期停机后
// 旧会话在数据库中残留。
func cleanupSessions(ctx context.Context, st *store.Store, log *slog.Logger) {
	runOnce := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		n, err := st.DeleteExpiredSessions(cleanupCtx, time.Now().UTC())
		if err != nil {
			log.Warn("clean expired sessions failed", "error", err)
			return
		}
		if n > 0 {
			log.Info("expired sessions removed", "count", n)
		}
	}

	runOnce()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}
