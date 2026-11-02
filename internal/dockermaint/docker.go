package dockermaint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// dockerClient 是只覆盖本项目所需端点的最小 Docker Engine 客户端。
// 它通过面板容器内的 Unix Socket 访问宿主 Docker，不使用 shell。
type dockerClient struct {
	socket  string
	baseURL string
	client  *http.Client
	mu      sync.Mutex
	version string
}

func newDockerClient(socket string) *dockerClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    2,
		IdleConnTimeout: 30 * time.Second,
	}
	return &dockerClient{
		socket: socket,
		client: &http.Client{
			Transport: transport,
			Timeout:   15 * time.Minute,
		},
	}
}

func (d *dockerClient) apiVersion(ctx context.Context) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.version != "" {
		return d.version, nil
	}
	data, _, err := d.request(ctx, http.MethodGet, "/version", nil)
	if err != nil {
		return "", err
	}
	var info struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := json.Unmarshal(data, &info); err != nil || info.APIVersion == "" {
		return "", fmt.Errorf("读取 Docker API 版本失败")
	}
	d.version = info.APIVersion
	return d.version, nil
}

func (d *dockerClient) request(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	version := ""
	if path != "/version" {
		var err error
		version, err = d.apiVersion(ctx)
		if err != nil {
			return nil, 0, err
		}
	}
	target := path
	if version != "" {
		target = "/v" + version + path
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(data)
	}
	base := d.baseURL
	if base == "" {
		base = "http://docker"
	}
	req, err := http.NewRequestWithContext(ctx, method, base+target, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, resp.StatusCode, dockerError(resp.StatusCode, data)
	}
	return data, resp.StatusCode, nil
}

func dockerError(status int, data []byte) error {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &payload); err == nil && payload.Message != "" {
		return fmt.Errorf("Docker API %d: %s", status, payload.Message)
	}
	text := strings.TrimSpace(string(data))
	if len(text) > 300 {
		text = text[:300]
	}
	if text == "" {
		return fmt.Errorf("Docker API 返回 %d", status)
	}
	return fmt.Errorf("Docker API %d: %s", status, text)
}

type inspectContainer struct {
	ID              string         `json:"Id"`
	Image           string         `json:"Image"`
	Config          map[string]any `json:"Config"`
	HostConfig      map[string]any `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]struct {
			Aliases    []string          `json:"Aliases"`
			IPAMConfig *endpointIPAM     `json:"IPAMConfig"`
			Links      []string          `json:"Links"`
			DriverOpts map[string]string `json:"DriverOpts"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
	State struct {
		Running bool   `json:"Running"`
		Status  string `json:"Status"`
		Health  *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

type endpointIPAM struct {
	IPv4Address  string   `json:"IPv4Address,omitempty"`
	IPv6Address  string   `json:"IPv6Address,omitempty"`
	LinkLocalIPs []string `json:"LinkLocalIPs,omitempty"`
}

func (d *dockerClient) inspectContainer(ctx context.Context, name string) (inspectContainer, []byte, error) {
	data, _, err := d.request(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil)
	if err != nil {
		return inspectContainer{}, nil, err
	}
	var info inspectContainer
	if err := json.Unmarshal(data, &info); err != nil {
		return inspectContainer{}, nil, fmt.Errorf("解析容器状态: %w", err)
	}
	return info, data, nil
}

type imageInspect struct {
	ID           string `json:"Id"`
	Architecture string `json:"Architecture"`
	Os           string `json:"Os"`
	Config       struct {
		Entrypoint []string          `json:"Entrypoint"`
		Labels     map[string]string `json:"Labels"`
		Env        []string          `json:"Env"`
	} `json:"Config"`
}

func (d *dockerClient) inspectImage(ctx context.Context, ref string) (imageInspect, error) {
	data, _, err := d.request(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil)
	if err != nil {
		return imageInspect{}, err
	}
	var info imageInspect
	if err := json.Unmarshal(data, &info); err != nil {
		return imageInspect{}, fmt.Errorf("解析镜像信息: %w", err)
	}
	return info, nil
}

func (d *dockerClient) pullImage(ctx context.Context, repository, tag string) error {
	path := "/images/create?fromImage=" + url.QueryEscape(repository) + "&tag=" + url.QueryEscape(tag)
	_, _, err := d.request(ctx, http.MethodPost, path, nil)
	return err
}

func (d *dockerClient) stopContainer(ctx context.Context, id string, timeout int) error {
	_, _, err := d.request(ctx, http.MethodPost, fmt.Sprintf("/containers/%s/stop?t=%d", url.PathEscape(id), timeout), nil)
	if err != nil && strings.Contains(err.Error(), "not running") {
		return nil
	}
	return err
}

func (d *dockerClient) removeContainer(ctx context.Context, id string) error {
	_, _, err := d.request(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=1&v=0", nil)
	if err != nil && strings.Contains(err.Error(), "No such container") {
		return nil
	}
	return err
}

func (d *dockerClient) createContainer(ctx context.Context, name string, old inspectContainer, image string) (string, error) {
	config := make(map[string]any, len(old.Config)+3)
	for key, value := range old.Config {
		config[key] = value
	}
	config["Image"] = image
	if hostname, _ := config["Hostname"].(string); hostname == old.ID {
		delete(config, "Hostname")
	}
	if labels, ok := config["Labels"].(map[string]any); ok {
		labels["com.ovpanel.openvpn.version"] = tagOf(image)
	}
	config["HostConfig"] = old.HostConfig
	endpoints := make(map[string]any, len(old.NetworkSettings.Networks))
	for network, endpoint := range old.NetworkSettings.Networks {
		item := map[string]any{}
		if len(endpoint.Aliases) > 0 {
			item["Aliases"] = endpoint.Aliases
		}
		if endpoint.IPAMConfig != nil {
			item["IPAMConfig"] = endpoint.IPAMConfig
		}
		if len(endpoint.Links) > 0 {
			item["Links"] = endpoint.Links
		}
		if len(endpoint.DriverOpts) > 0 {
			item["DriverOpts"] = endpoint.DriverOpts
		}
		endpoints[network] = item
	}
	if len(endpoints) > 0 {
		config["NetworkingConfig"] = map[string]any{"EndpointsConfig": endpoints}
	}
	data, _, err := d.request(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(name), config)
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ID == "" {
		return "", fmt.Errorf("解析新容器 ID 失败")
	}
	return created.ID, nil
}

func (d *dockerClient) startContainer(ctx context.Context, id string) error {
	_, _, err := d.request(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil)
	return err
}
