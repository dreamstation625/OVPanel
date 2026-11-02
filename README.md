# OVPanel

OpenVPN 服务端管理面板。单实例部署，面向少量管理员与 20 条以内并发连接；
Go 提供 HTTP 服务并内嵌 Vue 3 前端，SQLite 持久化。
支持 Linux amd64 与 arm64。

## 架构

严格两个容器：

- `panel`：面板 Web、内嵌前端与 SQLite 数据，唯一发布管理端口的容器。
- `openvpn`：OpenVPN 与运行代理，不发布管理端口，不挂载 Docker Socket。

面板前后端一体，运行期不需要 Node.js。侧栏显示当前版本，并会检查 GitHub 正式
Release：有新的正式版本时提示并展示更新说明，但网页不执行自更新；面板与 OpenVPN
的升级都按 Release 说明在宿主机用 Docker 命令完成。面板容器内的受限维护进程单独
持有 Docker Socket，Web 进程不直接访问 Docker。初始化为一次性口令，无默认管理员密码。

## 部署方式

只通过 Docker 部署。交付物是 panel 与 openvpn 两个 Docker 镜像，不单独构建、
发布或提供 Go 二进制包；部署、升级和回退都使用仓库的 Compose 文件与部署脚本。

运行参数通过 Compose 环境变量传入：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `OVPANEL_DATA_DIR` | `data` | 数据目录（SQLite、主密钥、引导口令） |
| `OVPANEL_LISTEN` | `:8080` | 监听地址 |
| `OVPANEL_TLS_CERT` / `OVPANEL_TLS_KEY` | 空 | 同时设置时启用 HTTPS |
| `OVPANEL_DEV_SELF_SIGNED` | `false` | 仅本机调试的短期自签名证书 |
| `OVPANEL_BOOTSTRAP_TOKEN` | 空 | 可选的初始化口令（至少 16 字符） |
| `OVPANEL_MASTER_KEY` | 空 | 可选主密钥（32 字节 base64/hex）；留空生成 `master.key` |
| `OVPANEL_SESSION_TTL` | `12h` | 管理员会话时长，最大 7 天 |
| `OVPANEL_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

`master.key` 与数据库同等敏感：丢失后无法解密已有证书私钥与 tls-crypt 密钥。
加密备份包会包含主密钥；恢复密码仍需单独保管，二者不要放在同一位置。

## Docker 部署

```bash
cp .env.example .env
./deploy/check-host.sh --data-dir ./data
./deploy/compose.sh up -d --build
```

`compose.sh up -d` 会在启动后自动检查并补齐 `ovpanel` 网络的宿主转发和 NAT；
需要 root 或免密 sudo，可用 `OVPANEL_AUTO_FORWARD=0` 关闭。若使用裸
`docker compose` 或 1Panel Compose，首次启动后手动执行一次
`sudo ./deploy/ensure-forwarding.sh`；规则持久化后不需要每次执行。详见
[deploy/README.md](deploy/README.md)。

### host-network 模式

如果希望 OpenVPN 不经过 Docker bridge、由容器自身自动配置宿主转发，可以使用
host-network 覆盖文件：

```bash
docker compose -f compose.yaml -f compose.hostnet.yaml up -d --build
```

该模式仍只有 panel 与 openvpn 两个容器，但 OpenVPN 直接使用宿主网络，管理端口
只监听 `127.0.0.1:9443`；entrypoint 会为 VPN 地址池维护宿主转发规则和 UFW 数据
端口放行。需要 Compose v2.24+（用于 `!reset`）和宿主
`net.ipv4.ip_forward=1`。OpenVPN 的 nft 规则会作用在宿主网络命名空间，适合希望
像 ovpn-admin 一样开箱自动转发、且能接受该网络边界的部署。

两种模式详细介绍见 [deploy/README.md](deploy/README.md)。

默认只绑定 `127.0.0.1:8080`，不会直接暴露到局域网。部署细节、HTTPS、容器边界、
数据目录和升级方式见 [deploy/README.md](deploy/README.md)。不提供独立二进制启动
或发布方式。

## OpenVPN 加密与连接

服务端页面可以选择数据加密算法、控制通道认证、TLS 最低版本、keepalive 间隔/超时
和最大客户端数。可选的数据加密算法包括 `AES-256-GCM`、`AES-128-GCM`、
`CHACHA20-POLY1305`、`AES-256-CBC`、`AES-128-CBC`；服务端按上述固定顺序协商，
与勾选顺序无关，优先使用 GCM/CHACHA20，CBC 仅用于兼容旧客户端。默认值与旧版本一致：`AES-256-GCM:AES-128-GCM`、
`SHA256`、TLS 1.2、keepalive `10 60`，最大客户端数不额外限制。

导出的客户端 `.ovpn` 会使用当前生效服务端的加密设置。修改加密算法、认证或
TLS 最低版本并应用后，需要重新下载客户端配置并重连；只支持旧算法或旧 TLS
版本的客户端可能无法连接。选择 `CHACHA20-POLY1305` 或 TLS 1.3 前，请确认
客户端版本支持。

## 客户端内网共享

除了服务端自己能访问的目标网段，还可以让一台客户端把它后面的内网共享给其他
VPN 客户端。例如客户端 A 位于分支办公室，A 连接 VPN 后，其他有权限的客户端
可以访问 A 后面的 `192.168.50.0/24`。

配置方式：

- 在 A 的配置详情中填写「共享内网」；
- 服务端「网络与路由」里打开「允许访问客户端共享内网」，或在配置的「共享内网访问」
  中单独设为允许；
- 共享内网只支持私网/CGNAT IPv4（`10/8`、`172.16/12`、`192.168/16`、`100.64/10`），
  不能与 VPN 地址池、服务端本地网络或其他配置共享的网段重叠；
- 同一个网段只能由一个配置共享。建议共享配置使用固定 IP，并只保持一条连接。

网络行为：

- 其他 VPN 客户端访问共享内网时，服务端不做 SNAT，A 看到的是对方的 VPN 地址
  （例如 `10.8.0.2`），可在面板「连接管理」中对应到具体用户和配置；
- 如果 A 自己再做 NAT，共享内网里的设备最终看到的是 A 的地址。要端到端保留来源，
  A 需要作为该内网的网关，或内网有回程路由，并且不做 NAT；
- 默认是单向共享：其他客户端可以访问共享内网，共享内网里的设备不能主动访问
  VPN 客户端，只能回包。若需要内网设备主动访问 VPN 客户端，需要让 A 把
  LAN→VPN 流量 NAT 成自己的 VPN 地址，或后续启用双向模式；
- 只支持 IPv4 单播，不支持广播、mDNS、NetBIOS、网络邻居发现；
- 如果客户端本地已有同网段，本地路由优先，无法通过 VPN 访问该共享网段。

A 侧要求：

- A 必须开启 IPv4 转发，并让共享内网能回到 VPN 地址池：A 是内网网关时通常
  不需要 NAT；否则一般需要在 A 上对 VPN 源段做 MASQUERADE；
- Linux / OpenWrt 最容易配置；Windows、macOS 需要手工开启转发和 NAT；
  手机客户端不支持作为共享网关。

共享网段由运行代理动态维护路由，增删网段不会重启 VPN 服务。A 离线时共享网段
会被丢弃，不会从服务端默认路由泄漏；已移除的共享网段会保留 blackhole 直到
OpenVPN 容器重启，避免旧客户端流量回落到默认路由。新增或修改共享网段后，
共享方需要重新连接以生成新的 `iroute`，需要访问新网段的客户端也需要重新连接
以获取新的路由。

## 版本与发布

仓库根目录的 `VERSION` 是唯一版本来源，推送后由
`.github/workflows/release.yml` 自动发布：

| 分支 | 发布条件 | Release | Docker 标签 |
| --- | --- | --- | --- |
| `main` | `VERSION` 对应的 tag 不存在 | 正式版，tag 等于 `VERSION` | `<version>`、`latest` |
| `dev` | 每次推送 | 预发布 | `VERSION` 含 `-dev` 时为 `dev`，否则 `<version>-pre` |

- `main` 的 `VERSION` 必须是正式版本，不能带预发布后缀。
- `dev` 分支使用 `<version>-dev`，始终只对应一个 `dev` 标签和一份预发布
  Release；每次推送覆盖重建，不会堆积多个版本号。
- 镜像内嵌的版本号来自 `VERSION`（例如 `0.0.1-dev`），Docker 标签仍按上表。
- 每份 Release 都附带源代码归档（Source code zip/tar.gz），并由面板侧栏检查
  正式版本、展示该版本的更新说明；dev 和预发布 Release 不参与更新判断。面板
  需要能访问 `api.github.com`；网络不通时只提示检查失败，不影响其他功能。
- 两个镜像都是 `linux/amd64` + `linux/arm64`：
  `dreamstation625/ovpanel`、`dreamstation625/ovpanel-openvpn`。
- 工作流读取仓库环境 `DOCKERHUB` 的变量 `DOCKERHUB_USERNAME` 与保密值
  `DOCKERHUB_TOKEN`。

发布正式版本只需修改 `VERSION` 并推送 `main`：例如从 `0.0.1` 改为 `0.0.2`，
工作流会创建 `0.0.2` 的 Release，并推送同名镜像与 `latest`。

## 目录

```text
compose.yaml       严格双容器部署入口（默认 bridge 模式）
compose.hostnet.yaml host-network 模式覆盖文件
.env.example       环境变量示例
cmd/panel/         面板进程入口（Web + 内嵌前端）
cmd/ovpanel-maintain/ 面板容器内的受限 OpenVPN 维护进程
cmd/vpn-agent/     OpenVPN 容器进程（运行代理）
internal/accounts/ 用户、配置、证书与导出编排
internal/admission/ 三种认证的共享准入判定
internal/agent/    OpenVPN 子进程、控制包应用与管理接口准入
internal/agentproto/ 面板到运行代理的控制包格式
internal/clientroutes/ 客户端共享内网的动态服务端路由
internal/api/      路由、错误体、中间件、静态资源
internal/auth/     Argon2id 密码、会话与 CSRF 令牌
internal/backup/   加密备份包、保留策略与恢复编排
internal/bootstrap/一次性初始化口令
internal/config/   环境变量配置
internal/devtls/   本机调试自签名证书
internal/dockermaint/ 受限 Docker 维护服务（只更新受管 OpenVPN）
internal/logging/  脱敏 JSON 日志
internal/maintclient/ 面板到维护进程的本地客户端
internal/maintapi/ 面板与维护进程的本地接口类型
internal/openvpnprofile/ .ovpn 与 tls-crypt 生成
internal/pki/      CA 与客户端证书生命周期
internal/runtimeauth/数据库到认证快照的映射
internal/runtimeenv/容器数据面能力探测
internal/secretbox/主密钥与私密材料加密
internal/serverconfig/服务端配置校验、指令兼容清单与渲染
internal/store/    SQLite 与迁移
internal/updatecheck/ GitHub Release 版本检查
internal/vpnruntime/控制包组装与共享控制目录下发
web/               Vue 3 + Vue Router 源码，构建产物内嵌
deploy/            Dockerfile、环境变量示例、宿主检查、运行手册
```
