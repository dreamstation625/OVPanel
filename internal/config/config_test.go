package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// clearEnv 清理所有相关环境变量，避免测试互相污染。
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		EnvDataDir, EnvListen, EnvTLSCert, EnvTLSKey,
		EnvDevSelfSigned, EnvBootstrapToken, EnvMasterKey, EnvLogLevel, EnvSessionTTL,
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	c, err := Load()
	if err != nil {
		t.Fatalf("默认配置应能加载：%v", err)
	}
	if c.DataDir != defaultDataDir {
		t.Fatalf("DataDir = %q", c.DataDir)
	}
	if c.Listen != defaultListen {
		t.Fatalf("Listen = %q", c.Listen)
	}
	if c.SessionTTL != defaultSessionTTL {
		t.Fatalf("SessionTTL = %v", c.SessionTTL)
	}
	if c.TLSEnabled() {
		t.Fatal("未配置证书时不应启用 HTTPS")
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvDataDir, "/srv/ovpanel")
	t.Setenv(EnvListen, ":8443")
	t.Setenv(EnvLogLevel, "debug")
	t.Setenv(EnvSessionTTL, "2h")

	c, err := Load()
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if c.DataDir != "/srv/ovpanel" || c.Listen != ":8443" || c.LogLevel != "debug" {
		t.Fatalf("配置未按环境变量生效：%+v", c)
	}
	if c.SessionTTL != 2*time.Hour {
		t.Fatalf("SessionTTL = %v", c.SessionTTL)
	}
	if c.DBPath() != filepath.Join("/srv/ovpanel", "panel.db") {
		t.Fatalf("DBPath = %q", c.DBPath())
	}
}

// TestValidateRequiresCertAndKeyPair 确认证书与私钥必须成对配置。
func TestValidateRequiresCertAndKeyPair(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvTLSCert, "/etc/ssl/panel.crt")

	if _, err := Load(); err == nil {
		t.Fatal("只配置证书时应拒绝启动")
	}
}

func TestValidateRejectsCertWithSelfSigned(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvTLSCert, "/etc/ssl/panel.crt")
	t.Setenv(EnvTLSKey, "/etc/ssl/panel.key")
	t.Setenv(EnvDevSelfSigned, "true")

	if _, err := Load(); err == nil {
		t.Fatal("挂载证书与自签名不应同时启用")
	}
}

func TestValidateSessionTTLBounds(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvSessionTTL, "8d")
	if _, err := Load(); err == nil {
		t.Fatal("超过上限的会话时长应被拒绝")
	}

	clearEnv(t)
	t.Setenv(EnvSessionTTL, "-1h")
	if _, err := Load(); err == nil {
		t.Fatal("非正会话时长应被拒绝")
	}
}

func TestValidateLogLevel(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvLogLevel, "trace")
	if _, err := Load(); err == nil {
		t.Fatal("未知日志级别应被拒绝")
	}
}

func TestValidateBootstrapTokenLength(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvBootstrapToken, "short")
	if _, err := Load(); err == nil {
		t.Fatal("过短的引导口令应被拒绝")
	}

	clearEnv(t)
	t.Setenv(EnvBootstrapToken, "a-long-enough-bootstrap-token")
	if _, err := Load(); err != nil {
		t.Fatalf("合法引导口令应通过：%v", err)
	}
}

func TestTLSEnabledWithSelfSigned(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvDevSelfSigned, "true")

	c, err := Load()
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if !c.TLSEnabled() {
		t.Fatal("自签名模式应启用 HTTPS")
	}
}
