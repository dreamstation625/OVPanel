// Package logging 提供统一的日志构造，并在写出前脱敏敏感字段。
//
// 面板会记录认证失败、配置应用和升级过程，这些日志很容易带上密码、
// 会话令牌或引导口令。脱敏放在 Handler 层统一完成，
// 避免每个调用点各自记得处理。
package logging

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"strings"
)

// redacted 是敏感字段的替代值。
const redacted = "[redacted]"

// sensitiveKeys 是必须脱敏的字段名（小写比较，忽略分隔符）。
var sensitiveKeys = map[string]struct{}{
	"password":        {},
	"passwd":          {},
	"passphrase":      {},
	"token":           {},
	"sessiontoken":    {},
	"secret":          {},
	"cookie":          {},
	"setcookie":       {},
	"authorization":   {},
	"privatekey":      {},
	"bootstrapsecret": {},
	"bootstrap":       {},
	"credential":      {},
	"credentials":     {},
}

// New 创建 JSON 日志器。level 为 debug/info/warn/error。
func New(w io.Writer, level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	inner := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: l})
	return slog.New(&redactHandler{inner: inner})
}

type redactHandler struct {
	inner slog.Handler
}

func (h *redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		clean = append(clean, redactAttr(a))
	}
	return &redactHandler{inner: h.inner.WithAttrs(clean)}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr 递归处理分组属性，保证嵌套字段同样被脱敏。
//
// 注意：slog.Any 传入 map[string]any 时值是 KindAny 而不是 KindGroup，
// 只判断 KindGroup 会漏掉这类最常见的嵌套写法，因此需要先 Resolve 再分支。
func redactAttr(a slog.Attr) slog.Attr {
	if isSensitive(a.Key) {
		return slog.String(a.Key, redacted)
	}

	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		group := v.Group()
		clean := make([]slog.Attr, 0, len(group))
		for _, g := range group {
			clean = append(clean, redactAttr(g))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}

	case slog.KindAny:
		m, ok := v.Any().(map[string]any)
		if !ok {
			return slog.Attr{Key: a.Key, Value: v}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		clean := make([]slog.Attr, 0, len(m))
		for _, k := range keys {
			clean = append(clean, redactAttr(slog.Any(k, m[k])))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

func isSensitive(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(key))
	_, hit := sensitiveKeys[normalized]
	return hit
}
