// Package agentproto 定义面板与运行代理之间的持久化配置包格式。
//
// 控制包只通过两个容器共享的私有控制目录传递；文件包含服务端私钥与
// 认证哈希，权限必须为 0600。运行代理不会把其中的敏感材料写入日志。
package agentproto

import (
	"time"

	"github.com/dreamstation625/ovpanel/internal/admission"
	"github.com/dreamstation625/ovpanel/internal/netpolicy"
)

// Version 是控制包格式版本。
const Version = 2

// Bundle 是一次完整应用所需的配置、材料与认证快照。
type Bundle struct {
	Version        int                `json:"version"`
	ConfigRevision int64              `json:"configRevision"`
	ConfigHash     string             `json:"configHash"`
	Config         string             `json:"config"`
	CAPEM          string             `json:"caPem"`
	ServerCertPEM  string             `json:"serverCertPem"`
	ServerKeyPEM   string             `json:"serverKeyPem"`
	TLSCryptKey    string             `json:"tlsCryptKey"`
	CRLPEM         string             `json:"crlPem"`
	Policy         netpolicy.Policy   `json:"policy"`
	Snapshot       admission.Snapshot `json:"snapshot"`
	IssuedAt       time.Time          `json:"issuedAt"`
}
