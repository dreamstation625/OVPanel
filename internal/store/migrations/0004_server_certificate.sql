-- P03/P04 运行代理所需的 OpenVPN 服务端证书。
--
-- 私钥与 CA 一样以主密钥加密保存；运行代理应用配置时只在内存中解密。

CREATE TABLE server_certificate (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    cert_pem       TEXT NOT NULL,
    key_ciphertext BLOB NOT NULL,
    issued_at      TEXT NOT NULL,
    expires_at     TEXT NOT NULL
);
