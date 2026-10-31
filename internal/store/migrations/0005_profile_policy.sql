-- P04 配置级网络策略：路由方式与内网访问权限独立于服务端默认值。
--
-- route_mode: inherit（继承服务端）/ split（仅访问内网）/ global（全部 IPv4）
-- access_mode: inherit（继承服务端）/ allow（仅允许指定网段）/ deny（禁止内网）
-- allowed_internal_json 只在 access_mode = 'allow' 时使用。

ALTER TABLE client_profile ADD COLUMN route_mode TEXT NOT NULL DEFAULT 'inherit';
ALTER TABLE client_profile ADD COLUMN access_mode TEXT NOT NULL DEFAULT 'inherit';
ALTER TABLE client_profile ADD COLUMN allowed_internal_json TEXT NOT NULL DEFAULT '[]';
