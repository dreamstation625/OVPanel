// Package updatecheck 查询 GitHub Releases 并判断当前版本是否有更新。
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultRepository = "dreamstation625/OVPanel"
	DefaultAPIBase    = "https://api.github.com"
	defaultTTL        = 10 * time.Minute
)

// ErrNoStableRelease 表示仓库还没有正式 Release。
var ErrNoStableRelease = errors.New("GitHub 上还没有正式版本")

var versionPattern = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?$`)

// Release 是 GitHub Release 的公开字段。
type Release struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Prerelease  bool   `json:"prerelease"`
	Draft       bool   `json:"draft"`
}

// Result 是一次更新检查的结果。
type Result struct {
	CurrentVersion  string    `json:"currentVersion"`
	CurrentCommit   string    `json:"currentCommit"`
	LatestVersion   string    `json:"latestVersion,omitempty"`
	LatestName      string    `json:"latestName,omitempty"`
	Notes           string    `json:"notes,omitempty"`
	URL             string    `json:"url,omitempty"`
	PublishedAt     string    `json:"publishedAt,omitempty"`
	UpdateAvailable bool      `json:"updateAvailable"`
	CheckedAt       time.Time `json:"checkedAt"`
}

// Checker 缓存短期检查结果，避免频繁请求 GitHub API。
type Checker struct {
	client       *http.Client
	apiBase      string
	repository   string
	ttl          time.Duration
	now          func() time.Time
	mu           sync.Mutex
	cached       *Result
	cachedKey    string
	cachedExpiry time.Time
}

// Options 允许测试注入 API 地址、客户端与时钟。
type Options struct {
	Client     *http.Client
	APIBase    string
	Repository string
	TTL        time.Duration
	Now        func() time.Time
}

// New 创建使用 GitHub 公共 API 的检查器。
func New() *Checker {
	return NewWithOptions(Options{})
}

// NewWithOptions 创建可配置的检查器。
func NewWithOptions(opts Options) *Checker {
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	apiBase := strings.TrimRight(opts.APIBase, "/")
	if apiBase == "" {
		apiBase = DefaultAPIBase
	}
	repository := strings.Trim(strings.TrimSpace(opts.Repository), "/")
	if repository == "" {
		repository = DefaultRepository
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Checker{client: client, apiBase: apiBase, repository: repository, ttl: ttl, now: now}
}

// Check 返回当前版本与最新 Release；同进程内缓存 10 分钟。
func (c *Checker) Check(ctx context.Context, currentVersion, currentCommit, buildTime string) (Result, error) {
	key := currentVersion + "|" + currentCommit + "|" + buildTime
	c.mu.Lock()
	if c.cached != nil && c.cachedKey == key && c.now().Before(c.cachedExpiry) {
		result := *c.cached
		c.mu.Unlock()
		return result, nil
	}
	c.mu.Unlock()

	releases, err := c.fetchReleases(ctx)
	if err != nil {
		return Result{}, err
	}
	result, err := c.choose(ctx, releases, currentVersion, currentCommit, buildTime)
	if err != nil {
		return Result{}, err
	}
	result.CheckedAt = c.now()

	c.mu.Lock()
	cached := result
	c.cached = &cached
	c.cachedKey = key
	c.cachedExpiry = c.now().Add(c.ttl)
	c.mu.Unlock()
	return result, nil
}

func (c *Checker) fetchReleases(ctx context.Context) ([]Release, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases?per_page=20", c.apiBase, c.repository)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "OVPanel")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 GitHub 失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub 返回 %s", resp.Status)
	}
	var releases []Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&releases); err != nil {
		return nil, fmt.Errorf("解析 GitHub Release 失败：%w", err)
	}
	return releases, nil
}

func (c *Checker) choose(ctx context.Context, releases []Release, currentVersion, currentCommit, buildTime string) (Result, error) {
	stable := stableReleases(releases)
	if len(stable) == 0 {
		return Result{}, ErrNoStableRelease
	}
	latest := stable[0]
	result := Result{
		CurrentVersion: currentVersion,
		CurrentCommit:  currentCommit,
		LatestVersion:  latest.TagName,
		LatestName:     latest.Name,
		Notes:          strings.TrimSpace(latest.Body),
		URL:            latest.HTMLURL,
		PublishedAt:    latest.PublishedAt,
	}
	latestVersion, _, _ := parseVersion(latest.TagName)
	currentVersionBase, currentStable, hasCurrent := parseVersion(currentVersion)

	switch {
	case hasCurrent && currentStable:
		result.UpdateAvailable = compareSemver(latestVersion, currentVersionBase) > 0
	case hasCurrent:
		switch compareSemver(latestVersion, currentVersionBase) {
		case 1:
			result.UpdateAvailable = true
		case 0:
			result.UpdateAvailable = c.newerThanBuild(ctx, latest, currentCommit, buildTime)
		}
	default:
		result.UpdateAvailable = c.newerThanBuild(ctx, latest, currentCommit, buildTime)
	}
	return result, nil
}

// stableReleases 只保留正式版本，并按语义化版本从高到低排序。
func stableReleases(releases []Release) []Release {
	stable := make([]Release, 0, len(releases))
	for _, release := range releases {
		if release.Draft || release.Prerelease || strings.TrimSpace(release.TagName) == "" {
			continue
		}
		if _, isStable, ok := parseVersion(release.TagName); !ok || !isStable {
			continue
		}
		stable = append(stable, release)
	}
	sort.SliceStable(stable, func(i, j int) bool {
		left, _, _ := parseVersion(stable[i].TagName)
		right, _, _ := parseVersion(stable[j].TagName)
		if cmp := compareSemver(left, right); cmp != 0 {
			return cmp > 0
		}
		return stable[i].PublishedAt > stable[j].PublishedAt
	})
	return stable
}

// newerThanBuild 判断正式版本是否在当前构建之后发布。
// 发布时间不可用时退回比较 tag 与当前构建的提交。
func (c *Checker) newerThanBuild(ctx context.Context, release Release, currentCommit, buildTime string) bool {
	if currentTime, err := time.Parse(time.RFC3339, strings.TrimSpace(buildTime)); err == nil {
		if published, err := time.Parse(time.RFC3339, strings.TrimSpace(release.PublishedAt)); err == nil {
			return published.After(currentTime)
		}
	}
	tagCommit, err := c.tagCommit(ctx, release.TagName)
	if err != nil || !isCommit(currentCommit) || !isCommit(tagCommit) {
		return false
	}
	return !sameCommit(currentCommit, tagCommit)
}

func (c *Checker) tagCommit(ctx context.Context, tag string) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/git/ref/tags/%s", c.apiBase, c.repository, url.PathEscape(tag))
	var ref struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if err := c.fetchJSON(ctx, endpoint, &ref); err != nil {
		return "", err
	}
	if ref.Object.Type != "tag" {
		return ref.Object.SHA, nil
	}
	var annotated struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.fetchJSON(ctx, fmt.Sprintf("%s/repos/%s/git/tags/%s", c.apiBase, c.repository, url.PathEscape(ref.Object.SHA)), &annotated); err != nil {
		return "", err
	}
	return annotated.Object.SHA, nil
}

func (c *Checker) fetchJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "OVPanel")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub 返回 %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(target)
}

func parseVersion(value string) ([3]int, bool, bool) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return [3]int{}, false, false
	}
	var out [3]int
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return [3]int{}, false, false
		}
		out[i] = n
	}
	return out, match[4] == "", true
}

func compareSemver(left, right [3]int) int {
	for i := 0; i < 3; i++ {
		if left[i] > right[i] {
			return 1
		}
		if left[i] < right[i] {
			return -1
		}
	}
	return 0
}

func isCommit(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 7 && value != "none" && value != "unknown"
}

func sameCommit(current, release string) bool {
	current = strings.ToLower(strings.TrimSpace(current))
	release = strings.ToLower(strings.TrimSpace(release))
	return strings.HasPrefix(release, current) || strings.HasPrefix(current, release)
}
