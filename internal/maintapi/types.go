// Package maintapi 定义面板与受限维护进程之间的本地接口类型。
//
// 维护进程只接受“已知 OpenVPN 容器 + 允许的仓库 + 合法标签”，
// 不接受任意镜像、容器参数、宿主路径或命令。
package maintapi

import "time"

// 任务状态。
const (
	TaskPending    = "pending"
	TaskRunning    = "running"
	TaskSucceeded  = "succeeded"
	TaskFailed     = "failed"
	TaskRolledBack = "rolled_back"
)

// Task 是一次 OpenVPN 更新任务。
type Task struct {
	ID             string     `json:"id"`
	State          string     `json:"state"`
	Phase          string     `json:"phase"`
	TargetImage    string     `json:"targetImage"`
	OldImage       string     `json:"oldImage"`
	NewImage       string     `json:"newImage,omitempty"`
	OldContainerID string     `json:"oldContainerId,omitempty"`
	NewContainerID string     `json:"newContainerId,omitempty"`
	BackupID       string     `json:"backupId,omitempty"`
	Message        string     `json:"message"`
	Error          string     `json:"error,omitempty"`
	RolledBack     bool       `json:"rolledBack"`
	StartedAt      time.Time  `json:"startedAt"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
}

// Container 是当前 OpenVPN 容器的公开信息。
type Container struct {
	Name    string `json:"name"`
	ID      string `json:"id"`
	Image   string `json:"image"`
	Version string `json:"version,omitempty"`
	Running bool   `json:"running"`
	Health  string `json:"health,omitempty"`
}

// State 是维护进程当前状态。
type State struct {
	Available         bool       `json:"available"`
	Reason            string     `json:"reason,omitempty"`
	AllowedRepository string     `json:"allowedRepository,omitempty"`
	Container         *Container `json:"container,omitempty"`
	Task              *Task      `json:"task,omitempty"`
	PinImage          string     `json:"pinImage,omitempty"`
}

// UpdateRequest 是启动更新的唯一输入：只允许标签，不接受完整镜像引用。
type UpdateRequest struct {
	TargetTag string `json:"targetTag"`
	BackupID  string `json:"backupId,omitempty"`
}

// ErrorResponse 是维护接口错误。
type ErrorResponse struct {
	Error string `json:"error"`
}
