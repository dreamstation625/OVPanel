-- P04 服务端配置修订。
--
-- 草稿与生效配置分开保存：保存只改变草稿，生效修订必须显式激活并由
-- 运行代理确认应用。渲染哈希用于核对面板保存内容与代理实际应用内容。

CREATE TABLE server_config_state (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    active_revision INTEGER,
    draft_revision  INTEGER,
    updated_at      TEXT NOT NULL
) WITHOUT ROWID;

INSERT INTO server_config_state (id, active_revision, draft_revision, updated_at)
VALUES (1, NULL, NULL, '1970-01-01T00:00:00Z');

CREATE TABLE server_config_revision (
    revision      INTEGER PRIMARY KEY AUTOINCREMENT,
    state         TEXT NOT NULL CHECK (state IN ('draft', 'active', 'superseded', 'discarded')),
    config_json   TEXT NOT NULL,
    rendered_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    applied_at    TEXT
);

CREATE INDEX idx_server_config_revision_state
    ON server_config_revision (state, revision DESC);
