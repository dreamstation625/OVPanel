-- P06 备份与恢复。
--
-- 备份包本身使用独立恢复密码加密；本表只保存公开元数据。
-- 自动备份所需的恢复密码以主密钥加密保存，不写入备份包明文。

CREATE TABLE backup_settings (
    id                     INTEGER PRIMARY KEY CHECK (id = 1),
    enabled                INTEGER NOT NULL DEFAULT 0,
    schedule_time          TEXT NOT NULL DEFAULT '03:00',
    retention_count        INTEGER NOT NULL DEFAULT 7,
    passphrase_ciphertext  BLOB,
    updated_at             TEXT NOT NULL
);

INSERT INTO backup_settings (id, enabled, schedule_time, retention_count, updated_at)
VALUES (1, 0, '03:00', 7, '1970-01-01T00:00:00Z');

CREATE TABLE backup (
    id                TEXT PRIMARY KEY,
    filename          TEXT NOT NULL UNIQUE,
    kind              TEXT NOT NULL CHECK (kind IN ('manual', 'scheduled', 'pre_restore')),
    note              TEXT NOT NULL DEFAULT '',
    size_bytes        INTEGER NOT NULL,
    sha256            TEXT NOT NULL,
    app_version       TEXT NOT NULL DEFAULT '',
    config_revision   INTEGER NOT NULL DEFAULT 0,
    agent_generation  TEXT NOT NULL DEFAULT '',
    usage_seq         INTEGER NOT NULL DEFAULT 0,
    manifest_json     TEXT NOT NULL DEFAULT '{}',
    created_at        TEXT NOT NULL
) WITHOUT ROWID;

CREATE INDEX idx_backup_created ON backup (created_at DESC);
CREATE INDEX idx_backup_kind_created ON backup (kind, created_at DESC);
