// Command ovpanel-maintain 是面板容器内的受限 OpenVPN 维护进程。
//
// 它单独持有宿主 Docker Socket，只通过容器内 Unix Socket 接受面板请求，
// 不提供任意 Docker API、镜像、挂载或命令执行。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dreamstation625/ovpanel/internal/dockermaint"
	"github.com/dreamstation625/ovpanel/internal/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ovpanel-maintain: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	log := logging.New(os.Stdout, getenv("OVPANEL_LOG_LEVEL", "info"))
	slog.SetDefault(log)

	service := dockermaint.New(dockermaint.Config{
		SocketPath:      getenv("OVPANEL_MAINTENANCE_SOCKET", "/run/ovpanel/maintain.sock"),
		DockerSocket:    getenv("OVPANEL_DOCKER_SOCKET", "/var/run/docker.sock"),
		StateDir:        getenv("OVPANEL_MAINTENANCE_STATE_DIR", "/var/lib/ovpanel-maintenance"),
		ContainerName:   getenv("OVPANEL_OPENVPN_CONTAINER", "ovpanel-openvpn"),
		ImageRepository: getenv("OVPANEL_OPENVPN_IMAGE_REPOSITORY", "dreamstation625/ovpanel-openvpn"),
		Log:             log,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := service.Run(ctx); err != nil {
		return err
	}
	log.Info("ovpanel-maintain stopped")
	return nil
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
