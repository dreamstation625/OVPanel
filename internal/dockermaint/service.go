// Package dockermaint 在面板容器内以独立进程运行 OpenVPN 容器维护。
//
// 它只通过本机 Unix Socket 接收面板请求，只允许更新受管 OpenVPN 容器到
// 指定仓库的合法标签；不提供任意 Docker API、任意镜像、宿主路径或命令执行。
package dockermaint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dreamstation625/ovpanel/internal/maintapi"
)

// AgentProtocol 是当前面板要求的最小代理协议版本。
const AgentProtocol = "1"

var tagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Config 是维护进程配置。
type Config struct {
	SocketPath      string
	DockerSocket    string
	StateDir        string
	ContainerName   string
	ImageRepository string
	Log             *slog.Logger
}

// Service 是受限 OpenVPN 维护进程。
type Service struct {
	cfg    Config
	docker *dockerClient
	log    *slog.Logger
	now    func() time.Time

	mu         sync.Mutex
	task       *maintapi.Task
	oldInspect json.RawMessage
	available  bool
	reason     string
	server     *http.Server
}

type persistedState struct {
	Task       *maintapi.Task  `json:"task"`
	OldInspect json.RawMessage `json:"oldInspect,omitempty"`
}

// New 创建维护服务。
func New(cfg Config) *Service {
	return &Service{
		cfg:    cfg,
		docker: newDockerClient(cfg.DockerSocket),
		log:    cfg.Log,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// Run 启动 Unix Socket HTTP 服务，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) error {
	if err := os.MkdirAll(s.cfg.StateDir, 0o700); err != nil {
		return fmt.Errorf("创建维护目录: %w", err)
	}
	s.loadTask()
	s.refreshAvailability(ctx)

	if err := os.MkdirAll(filepath.Dir(s.cfg.SocketPath), 0o770); err != nil {
		return fmt.Errorf("创建维护 Socket 目录: %w", err)
	}
	_ = os.Remove(s.cfg.SocketPath)
	listener, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("监听维护 Socket: %w", err)
	}
	if err := os.Chmod(s.cfg.SocketPath, 0o660); err != nil {
		listener.Close()
		return fmt.Errorf("设置维护 Socket 权限: %w", err)
	}
	// 维护进程以 root 运行时把 Socket 交给面板组，Web 才能连接；
	// 非 root 测试环境忽略该错误。
	if err := os.Chown(s.cfg.SocketPath, -1, 10001); err != nil {
		s.log.Debug("chown maintenance socket skipped", "error", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/state", s.handleState)
	mux.HandleFunc("POST /v1/update", s.handleUpdate)
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       time.Minute,
	}
	s.log.Info("maintenance ready",
		"socket", s.cfg.SocketPath,
		"docker_socket", s.cfg.DockerSocket,
		"available", s.isAvailable(),
		"container", s.cfg.ContainerName)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
	}()
	s.resumeInterrupted(ctx)
	err = s.server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Service) isAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.available
}

func (s *Service) refreshAvailability(ctx context.Context) {
	if err := s.validateManagedContainer(ctx); err != nil {
		s.mu.Lock()
		s.available = false
		s.reason = err.Error()
		s.mu.Unlock()
		s.log.Warn("maintenance unavailable", "reason", err)
		return
	}
	s.mu.Lock()
	s.available = true
	s.reason = ""
	s.mu.Unlock()
}

func (s *Service) validateManagedContainer(ctx context.Context) error {
	if s.cfg.ContainerName == "" {
		return errors.New("未配置受管 OpenVPN 容器名")
	}
	info, _, err := s.docker.inspectContainer(ctx, s.cfg.ContainerName)
	if err != nil {
		return fmt.Errorf("读取 OpenVPN 容器失败：%w", err)
	}
	labels := containerLabels(info)
	if labels["com.ovpanel.managed"] != "true" || labels["com.ovpanel.role"] != "openvpn" {
		return errors.New("目标容器不是 OVPanel 受管 OpenVPN 容器")
	}
	image, _ := info.Config["Image"].(string)
	if !strings.HasPrefix(image, s.cfg.ImageRepository+":") {
		return fmt.Errorf("当前镜像 %q 不属于允许仓库 %q", image, s.cfg.ImageRepository)
	}
	return nil
}

func containerLabels(info inspectContainer) map[string]string {
	labels := map[string]string{}
	if raw, ok := info.Config["Labels"].(map[string]any); ok {
		for key, value := range raw {
			if text, ok := value.(string); ok {
				labels[key] = text
			}
		}
	}
	return labels
}

func containerImage(info inspectContainer) string {
	image, _ := info.Config["Image"].(string)
	return image
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "available": s.isAvailable()})
}

func (s *Service) handleState(w http.ResponseWriter, r *http.Request) {
	if !s.isAvailable() {
		s.refreshAvailability(r.Context())
	}
	s.writeJSON(w, http.StatusOK, s.snapshot(r.Context()))
}

func (s *Service) snapshot(ctx context.Context) maintapi.State {
	s.mu.Lock()
	task := cloneTask(s.task)
	available := s.available
	reason := s.reason
	s.mu.Unlock()

	state := maintapi.State{
		Available:         available,
		Reason:            reason,
		AllowedRepository: s.cfg.ImageRepository,
		Task:              task,
	}
	if pin, err := os.ReadFile(filepath.Join(s.cfg.StateDir, "openvpn-image")); err == nil {
		state.PinImage = strings.TrimSpace(string(pin))
	}
	info, _, err := s.docker.inspectContainer(ctx, s.cfg.ContainerName)
	if err != nil {
		state.Available = false
		state.Reason = "无法读取 OpenVPN 容器：" + err.Error()
		return state
	}
	container := &maintapi.Container{
		Name:    s.cfg.ContainerName,
		ID:      info.ID,
		Image:   containerImage(info),
		Running: info.State.Running,
	}
	labels := containerLabels(info)
	// 镜像标签是实际运行版本的权威来源；容器标签可能仍是 Compose 的旧值。
	container.Version = tagOf(container.Image)
	if container.Version == "" {
		container.Version = labels["com.ovpanel.openvpn.version"]
	}
	if info.State.Health != nil {
		container.Health = info.State.Health.Status
	}
	state.Container = container
	return state
}

func (s *Service) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var req maintapi.UpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	req.TargetTag = strings.TrimSpace(req.TargetTag)
	if !tagPattern.MatchString(req.TargetTag) || strings.ContainsAny(req.TargetTag, "/:@") {
		s.writeError(w, http.StatusBadRequest, "目标版本标签不合法")
		return
	}
	if !s.isAvailable() {
		s.refreshAvailability(r.Context())
	}
	if !s.isAvailable() {
		s.writeError(w, http.StatusServiceUnavailable, "维护进程不可用："+s.reasonLocked())
		return
	}

	s.mu.Lock()
	if s.task != nil && (s.task.State == maintapi.TaskPending || s.task.State == maintapi.TaskRunning) {
		s.mu.Unlock()
		s.writeError(w, http.StatusConflict, "已有更新任务在进行")
		return
	}
	s.mu.Unlock()

	info, raw, err := s.docker.inspectContainer(r.Context(), s.cfg.ContainerName)
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "读取当前容器失败："+err.Error())
		return
	}
	target := s.cfg.ImageRepository + ":" + req.TargetTag
	current := containerImage(info)
	if current == target {
		s.writeError(w, http.StatusConflict, "当前已是目标版本")
		return
	}

	task := &maintapi.Task{
		ID:             newTaskID(),
		State:          maintapi.TaskPending,
		Phase:          "准备",
		TargetImage:    target,
		OldImage:       current,
		OldContainerID: info.ID,
		BackupID:       req.BackupID,
		Message:        "已登记更新任务",
		StartedAt:      s.now(),
	}
	s.mu.Lock()
	s.task = task
	s.oldInspect = append(json.RawMessage(nil), raw...)
	s.saveTaskLocked()
	s.mu.Unlock()

	go s.runUpdate(target, req.TargetTag, req.BackupID, append(json.RawMessage(nil), raw...))
	s.writeJSON(w, http.StatusAccepted, map[string]any{"task": cloneTask(task)})
}

func (s *Service) reasonLocked() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reason == "" {
		return "未知原因"
	}
	return s.reason
}

func (s *Service) runUpdate(target, tag, backupID string, oldRaw json.RawMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	var old inspectContainer
	if err := json.Unmarshal(oldRaw, &old); err != nil {
		s.fail(ctx, "解析旧容器信息失败："+err.Error())
		return
	}
	s.mu.Lock()
	if s.task != nil {
		if s.task.OldImage == "" {
			s.task.OldImage = containerImage(old)
		}
		if s.task.OldContainerID == "" {
			s.task.OldContainerID = old.ID
		}
		if s.task.BackupID == "" {
			s.task.BackupID = backupID
		}
		s.saveTaskLocked()
	}
	s.mu.Unlock()

	s.updatePhase("检查镜像", "拉取并校验目标镜像")
	image, err := s.docker.inspectImage(ctx, target)
	if err != nil {
		// 目标不在本机时再拉取；已固定的本地镜像不依赖外网。
		if pullErr := s.docker.pullImage(ctx, s.cfg.ImageRepository, tag); pullErr != nil {
			s.fail(ctx, "拉取目标镜像失败："+pullErr.Error())
			return
		}
		image, err = s.docker.inspectImage(ctx, target)
		if err != nil {
			s.fail(ctx, "检查目标镜像失败："+err.Error())
			return
		}
	}
	if err := validateImage(image); err != nil {
		s.fail(ctx, err.Error())
		return
	}

	s.updatePhase("停止旧容器", "停止并删除旧 OpenVPN 容器")
	if err := s.docker.stopContainer(ctx, old.ID, 20); err != nil {
		s.fail(ctx, "停止旧容器失败："+err.Error())
		return
	}
	if err := s.docker.removeContainer(ctx, old.ID); err != nil {
		_ = s.docker.startContainer(ctx, old.ID)
		s.fail(ctx, "删除旧容器失败："+err.Error())
		return
	}

	s.updatePhase("创建新容器", "使用原部署规格创建新容器")
	id, err := s.docker.createContainer(ctx, s.cfg.ContainerName, old, target)
	if err != nil {
		s.rollback(ctx, oldRaw, "创建新容器失败："+err.Error())
		return
	}
	s.setNewContainer(id)
	if err := s.docker.startContainer(ctx, id); err != nil {
		s.rollback(ctx, oldRaw, "启动新容器失败："+err.Error())
		return
	}

	s.updatePhase("健康检查", "等待新容器健康并与代理就绪")
	if err := s.waitHealthy(ctx, id, 120*time.Second); err != nil {
		s.rollback(ctx, oldRaw, "新容器健康检查失败："+err.Error())
		return
	}
	if err := s.writePin(target); err != nil {
		s.rollback(ctx, oldRaw, "写入镜像固定值失败："+err.Error())
		return
	}

	now := s.now()
	s.mu.Lock()
	if s.task != nil {
		s.task.State = maintapi.TaskSucceeded
		s.task.Phase = "完成"
		s.task.NewImage = target
		s.task.NewContainerID = id
		s.task.FinishedAt = &now
		s.task.Message = "OpenVPN 已更新到 " + target
		s.saveTaskLocked()
	}
	s.mu.Unlock()
	s.log.Info("openvpn updated", "image", target, "container_id", id)
}

func validateImage(image imageInspect) error {
	if image.Os != "" && image.Os != runtime.GOOS {
		return fmt.Errorf("镜像系统 %s 与当前主机 %s 不兼容", image.Os, runtime.GOOS)
	}
	if image.Architecture != "" && image.Architecture != runtime.GOARCH {
		return fmt.Errorf("镜像架构 %s 与当前主机 %s 不兼容", image.Architecture, runtime.GOARCH)
	}
	entrypoint := strings.Join(image.Config.Entrypoint, " ")
	if !strings.Contains(entrypoint, "vpn-agent") {
		return fmt.Errorf("镜像入口不是 OVPanel 运行代理：%q", entrypoint)
	}
	if role := image.Config.Labels["com.ovpanel.role"]; role != "" && role != "openvpn" {
		return fmt.Errorf("镜像角色标签不是 openvpn：%q", role)
	}
	if protocol := image.Config.Labels["com.ovpanel.agent-protocol"]; protocol != "" && protocol != AgentProtocol {
		return fmt.Errorf("镜像代理协议 %s 与当前面板要求的 %s 不兼容", protocol, AgentProtocol)
	}
	return nil
}

func (s *Service) rollback(ctx context.Context, oldRaw json.RawMessage, cause string) {
	s.updatePhase("回退", "更新失败，恢复旧容器")
	var old inspectContainer
	if err := json.Unmarshal(oldRaw, &old); err != nil {
		s.fail(ctx, cause+"；解析旧容器信息失败："+err.Error())
		return
	}
	if current, _, err := s.docker.inspectContainer(ctx, s.cfg.ContainerName); err == nil {
		_ = s.docker.stopContainer(ctx, current.ID, 20)
		_ = s.docker.removeContainer(ctx, current.ID)
	}
	id, err := s.docker.createContainer(ctx, s.cfg.ContainerName, old, containerImage(old))
	if err != nil {
		s.fail(ctx, cause+"；重建旧容器失败："+err.Error())
		return
	}
	s.setNewContainer(id)
	if err := s.docker.startContainer(ctx, id); err != nil {
		s.fail(ctx, cause+"；启动旧容器失败："+err.Error())
		return
	}
	if err := s.waitHealthy(ctx, id, 120*time.Second); err != nil {
		s.fail(ctx, cause+"；旧容器健康检查失败："+err.Error())
		return
	}
	now := s.now()
	s.mu.Lock()
	if s.task != nil {
		s.task.State = maintapi.TaskRolledBack
		s.task.Phase = "已回退"
		s.task.NewContainerID = id
		s.task.RolledBack = true
		s.task.Error = cause
		s.task.FinishedAt = &now
		s.task.Message = "更新失败，已恢复旧版本：" + cause
		s.saveTaskLocked()
	}
	s.mu.Unlock()
	s.log.Warn("openvpn update rolled back", "reason", cause, "container_id", id)
}

func (s *Service) fail(ctx context.Context, message string) {
	now := s.now()
	s.mu.Lock()
	if s.task != nil {
		s.task.State = maintapi.TaskFailed
		s.task.Phase = "失败"
		s.task.Error = message
		s.task.FinishedAt = &now
		s.task.Message = message
		s.saveTaskLocked()
	}
	s.mu.Unlock()
	s.log.Error("openvpn update failed", "error", message)
}

func (s *Service) updatePhase(phase, message string) {
	s.mu.Lock()
	if s.task != nil {
		s.task.State = maintapi.TaskRunning
		s.task.Phase = phase
		s.task.Message = message
		s.saveTaskLocked()
	}
	s.mu.Unlock()
}

func (s *Service) setNewContainer(id string) {
	s.mu.Lock()
	if s.task != nil {
		s.task.NewContainerID = id
		s.saveTaskLocked()
	}
	s.mu.Unlock()
}

func (s *Service) waitHealthy(ctx context.Context, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	started := time.Now()
	for time.Now().Before(deadline) {
		info, _, err := s.docker.inspectContainer(ctx, id)
		if err != nil {
			return err
		}
		if !info.State.Running {
			return fmt.Errorf("容器已退出")
		}
		if info.State.Health != nil {
			switch info.State.Health.Status {
			case "healthy":
				return nil
			case "unhealthy":
				return fmt.Errorf("容器健康状态为 unhealthy")
			}
		} else if time.Since(started) > 8*time.Second {
			// 镜像没有健康检查时，运行稳定一段时间后视为可用。
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("等待健康检查超时")
}

func (s *Service) writePin(image string) error {
	if err := os.MkdirAll(s.cfg.StateDir, 0o700); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(s.cfg.StateDir, "openvpn-image"), []byte(image+"\n"), 0o644); err != nil {
		return err
	}
	env := "OVPANEL_OPENVPN_IMAGE=" + image + "\n"
	return writeAtomic(filepath.Join(s.cfg.StateDir, "openvpn-image.env"), []byte(env), 0o644)
}

func (s *Service) loadTask() {
	data, err := os.ReadFile(filepath.Join(s.cfg.StateDir, "task.json"))
	if err != nil {
		return
	}
	var persisted persistedState
	if err := json.Unmarshal(data, &persisted); err != nil {
		s.log.Warn("ignore invalid maintenance state", "error", err)
		return
	}
	s.mu.Lock()
	s.task = persisted.Task
	s.oldInspect = persisted.OldInspect
	s.mu.Unlock()
}

func (s *Service) saveTaskLocked() {
	persisted := persistedState{Task: s.task, OldInspect: s.oldInspect}
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return
	}
	if err := writeAtomic(filepath.Join(s.cfg.StateDir, "task.json"), data, 0o600); err != nil {
		s.log.Warn("persist maintenance task failed", "error", err)
	}
}

func (s *Service) resumeInterrupted(ctx context.Context) {
	s.mu.Lock()
	task := cloneTask(s.task)
	oldRaw := append(json.RawMessage(nil), s.oldInspect...)
	s.mu.Unlock()
	if task == nil || (task.State != maintapi.TaskPending && task.State != maintapi.TaskRunning) {
		return
	}
	go func() {
		runCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		info, _, err := s.docker.inspectContainer(runCtx, s.cfg.ContainerName)
		if err == nil && containerImage(info) == task.TargetImage {
			if err := s.waitHealthy(runCtx, info.ID, 120*time.Second); err == nil {
				if err := s.writePin(task.TargetImage); err == nil {
					now := s.now()
					s.mu.Lock()
					if s.task != nil {
						s.task.State = maintapi.TaskSucceeded
						s.task.Phase = "完成"
						s.task.NewImage = task.TargetImage
						s.task.NewContainerID = info.ID
						s.task.FinishedAt = &now
						s.task.Message = "维护进程重启后确认更新完成"
						s.saveTaskLocked()
					}
					s.mu.Unlock()
					return
				}
			}
		}
		s.rollback(runCtx, oldRaw, "维护进程重启时任务未完成")
	}()
}

func (s *Service) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Service) writeError(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, maintapi.ErrorResponse{Error: message})
}

func cloneTask(task *maintapi.Task) *maintapi.Task {
	if task == nil {
		return nil
	}
	copied := *task
	return &copied
}

func tagOf(image string) string {
	idx := strings.LastIndex(image, ":")
	if idx < 0 || idx == len(image)-1 {
		return ""
	}
	return image[idx+1:]
}

func newTaskID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".maint-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
