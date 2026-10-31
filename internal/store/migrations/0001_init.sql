-- P02 初始 schema。
--
-- 持久化布局在 P02 冻结：后续阶段只追加迁移文件，不修改已应用的迁移，
-- 避免已部署实例的 schema 与代码分叉。
--
-- 时间统一以 UTC 的 RFC3339 文本保存，界面按 Asia/Shanghai 展示。
-- 字节量用 INTEGER，不使用浮点，避免累计误差。

CREATE TABLE admin (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    username      TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE TABLE admin_session (
    token_hash   BLOB PRIMARY KEY,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    remote_ip    TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT ''
) WITHOUT ROWID;

CREATE INDEX idx_admin_session_expires_at ON admin_session (expires_at);

-- 长任务状态。升级、备份、恢复等操作共享这一张表，
-- 面板重启后仍能展示任务处于哪个阶段。
CREATE TABLE operation (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    state       TEXT NOT NULL,
    progress    INTEGER NOT NULL DEFAULT 0,
    message     TEXT NOT NULL DEFAULT '',
    error_code  TEXT NOT NULL DEFAULT '',
    started_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    finished_at TEXT
) WITHOUT ROWID;

CREATE INDEX idx_operation_state ON operation (state, updated_at DESC);
