-- P03 用户、客户端配置、证书、CA 与导出设置。
--
-- 用户不是面板账号；一个用户拥有多份客户端配置，每份配置是独立的
-- 认证与授权单位。证书以 profile 归属，吊销后保留历史记录。

CREATE TABLE app_user (
    id           TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    notes        TEXT NOT NULL DEFAULT '',
    tags_json    TEXT NOT NULL DEFAULT '[]',
    status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
) WITHOUT ROWID;

CREATE INDEX idx_app_user_created ON app_user (created_at DESC, id DESC);

CREATE TABLE client_profile (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES app_user(id) ON DELETE RESTRICT,
    name             TEXT NOT NULL,
    auth_type        TEXT NOT NULL CHECK (auth_type IN ('certificate', 'certificate_password', 'password')),
    username         TEXT COLLATE NOCASE,
    password_hash    TEXT,
    status           TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    expires_at       TEXT,
    concurrent_limit INTEGER CHECK (concurrent_limit IS NULL OR concurrent_limit > 0),
    quota_bytes      INTEGER CHECK (quota_bytes IS NULL OR quota_bytes > 0),
    upload_bps       INTEGER CHECK (upload_bps IS NULL OR upload_bps > 0),
    download_bps     INTEGER CHECK (download_bps IS NULL OR download_bps > 0),
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    CHECK (
        (auth_type = 'certificate' AND username IS NULL AND password_hash IS NULL) OR
        (auth_type IN ('certificate_password', 'password') AND username IS NOT NULL AND password_hash IS NOT NULL)
    )
) WITHOUT ROWID;

CREATE UNIQUE INDEX idx_client_profile_username
    ON client_profile (username) WHERE username IS NOT NULL;
CREATE INDEX idx_client_profile_user ON client_profile (user_id, created_at);

CREATE TABLE certificate (
    id                 TEXT PRIMARY KEY,
    profile_id         TEXT NOT NULL REFERENCES client_profile(id) ON DELETE RESTRICT,
    serial_number      TEXT NOT NULL UNIQUE,
    fingerprint_sha256 TEXT NOT NULL UNIQUE,
    common_name        TEXT NOT NULL,
    cert_pem           TEXT NOT NULL,
    key_ciphertext     BLOB NOT NULL,
    status             TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    issued_at          TEXT NOT NULL,
    expires_at         TEXT NOT NULL,
    revoked_at         TEXT,
    revoke_reason      TEXT NOT NULL DEFAULT ''
) WITHOUT ROWID;

-- 同一配置最多一张生效证书；轮换必须在事务内先吊销旧证书再插入新证书。
CREATE UNIQUE INDEX idx_certificate_one_active
    ON certificate (profile_id) WHERE status = 'active';
CREATE INDEX idx_certificate_profile ON certificate (profile_id, issued_at DESC);

CREATE TABLE pki_ca (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    cert_pem       TEXT NOT NULL,
    key_ciphertext BLOB NOT NULL,
    created_at     TEXT NOT NULL,
    expires_at     TEXT NOT NULL
);

-- 生成客户端 .ovpn 所需的连接地址与 tls-crypt 密钥。
-- tls-crypt 密钥与 CA/客户端私钥一样只保存密文。
CREATE TABLE vpn_export_setting (
    id                   INTEGER PRIMARY KEY CHECK (id = 1),
    vpn_host             TEXT NOT NULL,
    vpn_port             INTEGER NOT NULL CHECK (vpn_port BETWEEN 1 AND 65535),
    vpn_proto            TEXT NOT NULL CHECK (vpn_proto IN ('udp', 'tcp')),
    tls_crypt_ciphertext BLOB NOT NULL,
    updated_at           TEXT NOT NULL
);
