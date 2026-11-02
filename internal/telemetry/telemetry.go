// Package telemetry 定义运行代理回传的用量快照与事件格式。
//
// 快照用于实时页面，事件日志用于历史与日汇总。两者都不包含密码、
// 私钥或其他凭据材料。面板对事件按「运行代次 + 序号」去重，重复读取
// 同一事件文件不会重复累计。
package telemetry

import "time"

// Version 是快照与事件的格式版本。
const Version = 1

// 控制目录中的文件名与事件类型。
const (
	SnapshotFile = "usage.json"
	// EventFilePrefix 是事件日志文件名前缀；面板读取控制目录中所有匹配文件。
	EventFilePrefix = "events"
	EventFileSuffix = ".jsonl"

	// CheckpointFile 是供面板备份读取的完整账本检查点。
	CheckpointFile = "usage-state-export.json"
	// RestoreFile 是恢复时面板写入、代理导入的账本检查点。
	RestoreFile = "usage-restore.json"

	EventSessionStart   = "session_start"
	EventSessionAddress = "session_address"
	EventUsage          = "usage"
	EventSessionEnd     = "session_end"
)

// Snapshot 是运行代理每秒写出的实时状态。
type Snapshot struct {
	Version    int            `json:"version"`
	Generation string         `json:"generation"`
	UpdatedAt  time.Time      `json:"updatedAt"`
	EventSeq   uint64         `json:"eventSeq"`
	Profiles   []ProfileUsage `json:"profiles"`
	Sessions   []LiveSession  `json:"sessions"`
}

// ProfileUsage 是某份配置在当前额度代次下的用量。
type ProfileUsage struct {
	ProfileID  string `json:"profileId"`
	QuotaEpoch int64  `json:"quotaEpoch"`
	UsageBytes uint64 `json:"usageBytes"`
	TotalBytes uint64 `json:"totalBytes"`
	Gaps       int    `json:"gaps"`
}

// LiveSession 是一条在线连接。
type LiveSession struct {
	ID          string    `json:"id"`
	CID         string    `json:"cid"`
	ProfileID   string    `json:"profileId"`
	VPNIP       string    `json:"vpnIp,omitempty"`
	SourceIP    string    `json:"sourceIp,omitempty"`
	ConnectedAt time.Time `json:"connectedAt"`
	UpBytes     uint64    `json:"upBytes"`
	DownBytes   uint64    `json:"downBytes"`
	UpRate      float64   `json:"upRate"`
	DownRate    float64   `json:"downRate"`
	Blocked     bool      `json:"blocked,omitempty"`
	BlockReason string    `json:"blockReason,omitempty"`
}

// Event 是追加写入事件日志的一条记录。
//
// UpBytes/DownBytes 是会话累计值（上传=服务端接收，下载=服务端发送）；
// DeltaUp/DeltaDown 是相对上一条事件新增的字节，日汇总只累加增量。
type Event struct {
	Version    int       `json:"version"`
	Seq        uint64    `json:"seq"`
	Generation string    `json:"generation"`
	Kind       string    `json:"kind"`
	At         time.Time `json:"at"`

	SessionID   string     `json:"sessionId"`
	CID         string     `json:"cid,omitempty"`
	ProfileID   string     `json:"profileId"`
	VPNIP       string     `json:"vpnIp,omitempty"`
	SourceIP    string     `json:"sourceIp,omitempty"`
	ConnectedAt *time.Time `json:"connectedAt,omitempty"`

	UpBytes   uint64 `json:"upBytes"`
	DownBytes uint64 `json:"downBytes"`
	DeltaUp   uint64 `json:"deltaUp,omitempty"`
	DeltaDown uint64 `json:"deltaDown,omitempty"`
	Reason    string `json:"reason,omitempty"`
}
