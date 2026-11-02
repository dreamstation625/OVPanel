-- 客户端共享内网：配置连接后把其背后的私网网段共享给其他客户端。
--
-- client_networks_json 只保存网络地址（CIDR 或单 IP 规范化为 /32）。
-- client_lan_access: inherit（跟随服务端开关）/ allow / deny。

ALTER TABLE client_profile ADD COLUMN client_networks_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE client_profile ADD COLUMN client_lan_access TEXT NOT NULL DEFAULT 'inherit';
