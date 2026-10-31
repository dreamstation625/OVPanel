-- P05 会话、用量与限制。
--
-- 运行代理是限额与用量的事实来源；面板保存同步副本、连接历史与日汇总。
-- quota_epoch 由面板在手动重置额度时递增，运行代理据此把当前累计设为
-- 新基线，重置前的流量不会重复计入新周期。
--
-- 时间统一为 UTC 的 RFC3339 文本；usage_daily.day 使用 Asia/Shanghai
-- 自然日（YYYY-MM-DD）。

ALTER TABLE client_profile ADD COLUMN quota_epoch INTEGER NOT NULL DEFAULT 0;

-- 连接历史。主键是「运行代次:CID」，不能只用 CID 或 CN。
CREATE TABLE vpn_session (
    id              TEXT PRIMARY KEY,
    generation      TEXT NOT NULL,
    cid             TEXT NOT NULL,
    profile_id      TEXT NOT NULL REFERENCES client_profile(id) ON DELETE CASCADE,
    vpn_ip          TEXT NOT NULL DEFAULT '',
    source_ip       TEXT NOT NULL DEFAULT '',
    connected_at    TEXT NOT NULL,
    disconnected_at TEXT,
    end_reason      TEXT NOT NULL DEFAULT '',
    up_bytes        INTEGER NOT NULL DEFAULT 0,
    down_bytes      INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

CREATE INDEX idx_vpn_session_profile ON vpn_session (profile_id, connected_at DESC);
CREATE INDEX idx_vpn_session_connected ON vpn_session (connected_at DESC);
CREATE INDEX idx_vpn_session_open ON vpn_session (disconnected_at) WHERE disconnected_at IS NULL;

-- 按配置汇总的自然日流量。会话明细清理不影响日汇总。
CREATE TABLE usage_daily (
    day        TEXT NOT NULL,
    profile_id TEXT NOT NULL REFERENCES client_profile(id) ON DELETE CASCADE,
    up_bytes   INTEGER NOT NULL DEFAULT 0,
    down_bytes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, profile_id)
) WITHOUT ROWID;

CREATE INDEX idx_usage_daily_profile ON usage_daily (profile_id, day DESC);

-- 运行事件同步游标按代次保存；代理状态丢失后重新从 0 开始不会跳过事件。
CREATE TABLE usage_sync_state (
    generation  TEXT PRIMARY KEY,
    max_seq     INTEGER NOT NULL DEFAULT 0,
    updated_at  TEXT NOT NULL
) WITHOUT ROWID;

-- 手动重置额度记录；历史用量与记录不因重置删除。
CREATE TABLE quota_reset (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id TEXT NOT NULL REFERENCES client_profile(id) ON DELETE CASCADE,
    epoch      INTEGER NOT NULL,
    reset_at   TEXT NOT NULL
);

CREATE INDEX idx_quota_reset_profile ON quota_reset (profile_id, reset_at DESC);
