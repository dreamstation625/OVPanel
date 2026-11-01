-- 固定 VPN 地址；空字符串表示由 OpenVPN 动态分配。
ALTER TABLE client_profile ADD COLUMN fixed_vpn_ip TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_client_profile_fixed_vpn_ip
    ON client_profile (fixed_vpn_ip) WHERE fixed_vpn_ip <> '';
