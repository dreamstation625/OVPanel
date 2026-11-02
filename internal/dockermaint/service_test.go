package dockermaint

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/maintapi"
)

type fakeDocker struct {
	mu        sync.Mutex
	container *inspectContainer
	images    map[string]imageInspect
	pullFail  bool
	maxCount  int
}

func newFakeDocker() *fakeDocker {
	old := inspectContainer{ID: "old-id"}
	old.Config = map[string]any{
		"Image":  "ovpanel-p07-openvpn:v1",
		"Labels": map[string]any{"com.ovpanel.managed": "true", "com.ovpanel.role": "openvpn"},
	}
	old.HostConfig = map[string]any{"NetworkMode": "testnet", "CapAdd": []any{"NET_ADMIN"}}
	old.NetworkSettings.Networks = map[string]struct {
		Aliases    []string          `json:"Aliases"`
		IPAMConfig *endpointIPAM     `json:"IPAMConfig"`
		Links      []string          `json:"Links"`
		DriverOpts map[string]string `json:"DriverOpts"`
	}{}
	incompatibleArch := "arm64"
	if runtime.GOARCH == "arm64" {
		incompatibleArch = "amd64"
	}
	return &fakeDocker{
		container: &old,
		images: map[string]imageInspect{
			"ovpanel-p07-openvpn:v2": {ID: "img2", Architecture: runtime.GOARCH, Os: runtime.GOOS,
				Config: imageConfig([]string{"/usr/local/bin/vpn-agent"}, map[string]string{"com.ovpanel.agent-protocol": "1"})},
			"ovpanel-p07-openvpn:v3": {ID: "img3", Architecture: runtime.GOARCH, Os: runtime.GOOS,
				Config: imageConfig([]string{"/bin/sh"}, nil)},
			"ovpanel-p07-openvpn:v4": {ID: "img4", Architecture: incompatibleArch, Os: runtime.GOOS,
				Config: imageConfig([]string{"/usr/local/bin/vpn-agent"}, nil)},
			"ovpanel-p07-openvpn:v5": {ID: "img5", Architecture: runtime.GOARCH, Os: runtime.GOOS,
				Config: imageConfig([]string{"/usr/local/bin/vpn-agent"}, nil)},
		},
	}
}

func imageConfig(entrypoint []string, labels map[string]string) struct {
	Entrypoint []string          `json:"Entrypoint"`
	Labels     map[string]string `json:"Labels"`
	Env        []string          `json:"Env"`
} {
	var cfg struct {
		Entrypoint []string          `json:"Entrypoint"`
		Labels     map[string]string `json:"Labels"`
		Env        []string          `json:"Env"`
	}
	cfg.Entrypoint = entrypoint
	cfg.Labels = map[string]string{"com.ovpanel.role": "openvpn", "com.ovpanel.agent-protocol": AgentProtocol}
	for key, value := range labels {
		cfg.Labels[key] = value
	}
	return cfg
}

func TestValidateCurrentAndLegacyImageEntrypoints(t *testing.T) {
	data, err := os.ReadFile("../../deploy/Dockerfile.openvpn")
	if err != nil {
		t.Fatal(err)
	}
	var official []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "ENTRYPOINT ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "ENTRYPOINT ")), &official); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, entrypoint := range [][]string{official, {"/usr/local/bin/vpn-agent"}} {
		if err := validateImage(imageInspect{Config: imageConfig(entrypoint, nil)}); err != nil {
			t.Fatalf("项目镜像入口应可升级：%v", err)
		}
	}
	for _, entrypoint := range [][]string{{"/bin/sh", "-c", "vpn-agent"}, {"/tmp/vpn-agent"}, {"/usr/local/bin/entrypoint-openvpn", "extra"}} {
		if err := validateImage(imageInspect{Config: imageConfig(entrypoint, nil)}); err == nil {
			t.Fatalf("不应接受非托管入口：%v", entrypoint)
		}
	}
	image := imageInspect{Config: imageConfig(official, nil)}
	delete(image.Config.Labels, "com.ovpanel.agent-protocol")
	if err := validateImage(image); err == nil {
		t.Fatal("缺少协议标签时应拒绝")
	}
}

func TestRecoveryRejectsUnmanagedOrPrivilegedSpecs(t *testing.T) {
	for _, kind := range []string{"image", "privileged", "labels"} {
		t.Run(kind, func(t *testing.T) {
			fake := newFakeDocker()
			service, _ := newFakeService(t, fake)
			old := *fake.snapshot()
			switch kind {
			case "image":
				old.Config = map[string]any{"Image": "outside-allowlist:v1"}
			case "privileged":
				old.HostConfig = map[string]any{"Privileged": true, "Binds": []string{"/:/host"}}
			case "labels":
				old.Config = map[string]any{"Image": "ovpanel-p07-openvpn:v1"}
			}
			raw, _ := json.Marshal(old)
			service.task = &maintapi.Task{ID: "unsafe", State: maintapi.TaskRunning}
			service.rollback(t.Context(), raw, "中断恢复")
			if service.task.State != maintapi.TaskFailed || fake.snapshot().ID != "old-id" {
				t.Fatal("不安全规格必须在停止或替换当前容器之前拒绝")
			}
		})
	}
}

func (f *fakeDocker) healthFor(image string) string {
	if strings.HasSuffix(image, ":v5") {
		return "unhealthy"
	}
	return "healthy"
}

func (f *fakeDocker) snapshot() *inspectContainer {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.container == nil {
		return nil
	}
	copied := *f.container
	return &copied
}

func (f *fakeDocker) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/json"):
			if f.container == nil {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"message":"No such container"}`)
				return
			}
			json.NewEncoder(w).Encode(f.container)
		case strings.HasSuffix(path, "/containers/create"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			image, _ := body["Image"].(string)
			f.container = &inspectContainer{ID: "new-id"}
			f.container.Config = map[string]any{"Image": image, "Labels": map[string]any{
				"com.ovpanel.managed": "true", "com.ovpanel.role": "openvpn"}}
			f.container.HostConfig = body["HostConfig"].(map[string]any)
			f.container.State.Running = true
			health := f.healthFor(image)
			f.container.State.Health = &struct {
				Status string `json:"Status"`
			}{Status: health}
			f.container.NetworkSettings.Networks = map[string]struct {
				Aliases    []string          `json:"Aliases"`
				IPAMConfig *endpointIPAM     `json:"IPAMConfig"`
				Links      []string          `json:"Links"`
				DriverOpts map[string]string `json:"DriverOpts"`
			}{}
			count := 1
			if count > f.maxCount {
				f.maxCount = count
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"Id":"new-id"}`)
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			index := strings.Index(path, "/images/")
			ref := strings.TrimSuffix(path[index+len("/images/"):], "/json")
			image, ok := f.images[ref]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"message":"No such image"}`)
				return
			}
			json.NewEncoder(w).Encode(image)
		case strings.HasSuffix(path, "/images/create"):
			if f.pullFail {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, `{"message":"registry unavailable"}`)
				return
			}
			io.WriteString(w, `{"status":"ok"}`)
		case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/stop"):
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(path, "/containers/") && strings.HasSuffix(path, "/start"):
			if f.container != nil {
				f.container.State.Running = true
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(path, "/v1.47/containers/") && r.Method == http.MethodDelete:
			f.container = nil
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(path, "/version"):
			io.WriteString(w, `{"ApiVersion":"1.47"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"unexpected path `+path+`"}`)
		}
	})
}

func newFakeService(t *testing.T, fake *fakeDocker) (*Service, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	service := New(Config{
		SocketPath:      filepath.Join(t.TempDir(), "maint.sock"),
		DockerSocket:    "unused",
		StateDir:        t.TempDir(),
		ContainerName:   "ovpanel-openvpn",
		ImageRepository: "ovpanel-p07-openvpn",
		Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	service.docker = &dockerClient{baseURL: server.URL, client: server.Client(), version: "1.47"}
	return service, server
}

func TestRunUpdateReplacesContainerAndWritesPin(t *testing.T) {
	fake := newFakeDocker()
	service, _ := newFakeService(t, fake)
	old := *fake.container
	raw, _ := json.Marshal(old)
	service.task = &maintapi.Task{ID: "t1", State: maintapi.TaskPending, TargetImage: "ovpanel-p07-openvpn:v2"}
	service.oldInspect = raw
	service.runUpdate("ovpanel-p07-openvpn:v2", "v2", "backup-1", raw)

	task := service.task
	if task.State != maintapi.TaskSucceeded || task.NewImage != "ovpanel-p07-openvpn:v2" {
		t.Fatalf("任务未成功：%+v", task)
	}
	current := fake.snapshot()
	if current == nil || containerImage(*current) != "ovpanel-p07-openvpn:v2" {
		t.Fatalf("容器镜像未更新：%+v", current)
	}
	pin, err := os.ReadFile(filepath.Join(service.cfg.StateDir, "openvpn-image"))
	if err != nil || strings.TrimSpace(string(pin)) != "ovpanel-p07-openvpn:v2" {
		t.Fatalf("镜像固定值异常：%q %v", pin, err)
	}
	env, err := os.ReadFile(filepath.Join(service.cfg.StateDir, "openvpn-image.env"))
	if err != nil || !strings.Contains(string(env), "OVPANEL_OPENVPN_IMAGE=ovpanel-p07-openvpn:v2") {
		t.Fatalf("部署环境固定值异常：%q %v", env, err)
	}
	if task.BackupID != "backup-1" || task.OldImage != "ovpanel-p07-openvpn:v1" {
		t.Fatalf("任务元数据异常：%+v", task)
	}
	if fake.maxCount > 1 {
		t.Fatalf("更新期间同时存在 %d 个容器", fake.maxCount)
	}
}

func TestRunUpdateRollsBackOnUnhealthyImage(t *testing.T) {
	fake := newFakeDocker()
	service, _ := newFakeService(t, fake)
	old := *fake.container
	raw, _ := json.Marshal(old)
	service.task = &maintapi.Task{ID: "t2", State: maintapi.TaskPending, TargetImage: "ovpanel-p07-openvpn:v5"}
	service.oldInspect = raw
	service.runUpdate("ovpanel-p07-openvpn:v5", "v5", "backup-2", raw)

	task := service.task
	if task.State != maintapi.TaskRolledBack || !task.RolledBack {
		t.Fatalf("失败更新未回退：%+v", task)
	}
	current := fake.snapshot()
	if current == nil || containerImage(*current) != "ovpanel-p07-openvpn:v1" {
		t.Fatalf("回退后镜像异常：%+v", current)
	}
	if _, err := os.Stat(filepath.Join(service.cfg.StateDir, "openvpn-image")); !os.IsNotExist(err) {
		t.Fatal("失败更新不应写入镜像固定值")
	}
	if fake.maxCount > 1 {
		t.Fatalf("回退期间同时存在 %d 个容器", fake.maxCount)
	}
}

func TestRunUpdateRejectsIncompatibleImages(t *testing.T) {
	for _, tag := range []string{"v3", "v4"} {
		fake := newFakeDocker()
		service, _ := newFakeService(t, fake)
		old := *fake.container
		raw, _ := json.Marshal(old)
		service.task = &maintapi.Task{ID: "t-" + tag, State: maintapi.TaskPending}
		service.oldInspect = raw
		service.runUpdate("ovpanel-p07-openvpn:"+tag, tag, "", raw)
		if service.task.State != maintapi.TaskFailed {
			t.Fatalf("%s 应在删除旧容器前失败：%+v", tag, service.task)
		}
		current := fake.snapshot()
		if current == nil || containerImage(*current) != "ovpanel-p07-openvpn:v1" {
			t.Fatalf("%s 失败后旧容器被改变：%+v", tag, current)
		}
	}
}

func TestResumeInterruptedTaskCompletesHealthyContainer(t *testing.T) {
	fake := newFakeDocker()
	service, _ := newFakeService(t, fake)
	fake.mu.Lock()
	fake.container.Config["Image"] = "ovpanel-p07-openvpn:v2"
	fake.container.State.Running = true
	fake.container.State.Health = &struct {
		Status string `json:"Status"`
	}{Status: "healthy"}
	fake.mu.Unlock()
	service.task = &maintapi.Task{
		ID: "t-recover", State: maintapi.TaskRunning, Phase: "健康检查",
		TargetImage: "ovpanel-p07-openvpn:v2", OldImage: "ovpanel-p07-openvpn:v1",
	}
	service.resumeInterrupted(context.Background())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		service.mu.Lock()
		state := service.task.State
		service.mu.Unlock()
		if state == maintapi.TaskSucceeded {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	service.mu.Lock()
	task := *service.task
	service.mu.Unlock()
	if task.State != maintapi.TaskSucceeded {
		t.Fatalf("中断任务未恢复成功：%+v", task)
	}
	if pin, err := os.ReadFile(filepath.Join(service.cfg.StateDir, "openvpn-image")); err != nil ||
		strings.TrimSpace(string(pin)) != "ovpanel-p07-openvpn:v2" {
		t.Fatalf("恢复后镜像固定值异常：%q %v", pin, err)
	}
}

func TestLocalImageSkipsPull(t *testing.T) {
	fake := newFakeDocker()
	fake.pullFail = true
	service, _ := newFakeService(t, fake)
	old := *fake.container
	raw, _ := json.Marshal(old)
	service.task = &maintapi.Task{ID: "t-local", State: maintapi.TaskPending}
	service.oldInspect = raw
	service.runUpdate("ovpanel-p07-openvpn:v2", "v2", "", raw)
	if service.task.State != maintapi.TaskSucceeded {
		t.Fatalf("拉取失败未回退本地镜像：%+v", service.task)
	}
}
