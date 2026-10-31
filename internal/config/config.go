// Package config 负责面板进程的配置加载与校验。
//
// 配置只来自环境变量：容器部署不需要挂载额外的配置文件，
// 也不会因为工作目录不同而读到另一套参数。
// 所有校验在启动时一次完成，运行期不再重复判断。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// 环境变量名。集中定义，避免字符串散落在各处。
const (
	EnvDataDir        = "OVPANEL_DATA_DIR"
	EnvListen         = "OVPANEL_LISTEN"
	EnvTLSCert        = "OVPANEL_TLS_CERT"
	EnvTLSKey         = "OVPANEL_TLS_KEY"
	EnvDevSelfSigned  = "OVPANEL_DEV_SELF_SIGNED"
	EnvBootstrapToken = "OVPANEL_BOOTSTRAP_TOKEN"
	EnvLogLevel       = "OVPANEL_LOG_LEVEL"
	EnvSessionTTL     = "OVPANEL_SESSION_TTL"
)

// 默认值。数据目录使用相对路径，便于在普通终端直接运行。
const (
	defaultDataDir    = "data"
	defaultListen     = ":8080"
	defaultLogLevel   = "info"
	defaultSessionTTL = 12 * time.Hour
	maxSessionTTL     = 7 * 24 * time.Hour
)

// Config 是已校验的运行时配置。
type Config struct {
	DataDir        string
	Listen         string
	TLSCertFile    string
	TLSKeyFile     string
	DevSelfSigned  bool
	BootstrapToken string
	LogLevel       string
	SessionTTL     time.Duration
}

// Load 读取环境变量并校验。
func Load() (Config, error) {
	c := Config{
		DataDir:        getString(EnvDataDir, defaultDataDir),
		Listen:         getString(EnvListen, defaultListen),
		TLSCertFile:    getString(EnvTLSCert, ""),
		TLSKeyFile:     getString(EnvTLSKey, ""),
		BootstrapToken: getString(EnvBootstrapToken, ""),
		LogLevel:       strings.ToLower(getString(EnvLogLevel, defaultLogLevel)),
		SessionTTL:     defaultSessionTTL,
	}

	if raw := getString(EnvDevSelfSigned, ""); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s 不是合法布尔值：%q", EnvDevSelfSigned, raw)
		}
		c.DevSelfSigned = v
	}

	if raw := getString(EnvSessionTTL, ""); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s 不是合法时长：%q", EnvSessionTTL, raw)
		}
		c.SessionTTL = d
	}

	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate 校验配置的一致性。
//
// 证书与私钥必须成对出现：只配一个会导致启动时才发现无法启用 HTTPS，
// 这种问题应该在加载阶段就报出来。
func (c Config) Validate() error {
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("%s 不能为空", EnvDataDir)
	}
	if strings.TrimSpace(c.Listen) == "" {
		return fmt.Errorf("%s 不能为空", EnvListen)
	}

	hasCert := strings.TrimSpace(c.TLSCertFile) != ""
	hasKey := strings.TrimSpace(c.TLSKeyFile) != ""
	if hasCert != hasKey {
		return fmt.Errorf("%s 与 %s 必须同时设置或同时留空", EnvTLSCert, EnvTLSKey)
	}
	if hasCert && c.DevSelfSigned {
		return fmt.Errorf("%s 与 %s 不能同时使用自签名证书", EnvTLSCert, EnvDevSelfSigned)
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("%s 只能是 debug/info/warn/error：%q", EnvLogLevel, c.LogLevel)
	}

	if c.SessionTTL <= 0 {
		return fmt.Errorf("%s 必须大于 0", EnvSessionTTL)
	}
	if c.SessionTTL > maxSessionTTL {
		return fmt.Errorf("%s 不能超过 %s", EnvSessionTTL, maxSessionTTL)
	}

	// 外部注入的一次性口令用于避免首次初始化被抢占，不能允许明显过短的值。
	if c.BootstrapToken != "" && len(c.BootstrapToken) < 16 {
		return fmt.Errorf("%s 至少需要 16 个字符", EnvBootstrapToken)
	}
	return nil
}

// TLSEnabled 表示是否以 HTTPS 启动。
func (c Config) TLSEnabled() bool {
	return c.DevSelfSigned || (c.TLSCertFile != "" && c.TLSKeyFile != "")
}

// DBPath 返回面板数据库路径。
func (c Config) DBPath() string {
	return joinPath(c.DataDir, "panel.db")
}

// BootstrapTokenPath 返回首次初始化引导口令的落盘位置。
func (c Config) BootstrapTokenPath() string {
	return joinPath(c.DataDir, "bootstrap-token")
}

func getString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return def
}
