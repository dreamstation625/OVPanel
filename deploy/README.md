# OVPanel 部署与运行

本目录提供 Dockerfile、部署前检查与运行手册；严格双容器部署入口是项目根目录的
`compose.yaml`，推荐叠加 `compose.hostnet.yaml`，让 OpenVPN 使用 host 网络；
面板仍使用 bridge 网络。环境变量示例是根目录的 `.env.example`。
宝塔、1Panel 等面板可直接使用独立的 [`compose.host.yaml`](../compose.host.yaml)，
配套环境变量见 [`.env.host.example`](../.env.host.example)，无需克隆仓库或合并多个 Compose 文件。

OpenVPN 容器运行真实 OpenVPN 与运行代理，服务端配置、准入、网络策略、统计、
限额和限速由代理执行。容器 healthy 代表进程、代理握手与数据面环境检查通过，
不代替真实客户端连通验收。

## 目录

```text
deploy/
  Dockerfile.panel        Vue 构建 + Go 内嵌 + 非 root Web + 受限维护进程
  Dockerfile.openvpn      OpenVPN 2.7.7 与运行代理
  entrypoint-panel.sh     修正目录权限，分别启动 Web 与维护进程
  compose.sh              统一 Compose 入口，加载镜像固定值
  check-host.sh           宿主 Docker、TUN、nft、tc、内核模块检查
```

仓库部署使用根目录的 `compose.yaml` 与 `compose.hostnet.yaml`，运行数据与证书默认
保存在根目录的 `data/`、`certs/`（Git 忽略，首次启动创建）。独立 `compose.host.yaml`
使用 `/opt/ovpanel/data`、`/opt/ovpanel/certs`，不依赖面板的编排工作目录。
除宝塔 / 1Panel 部署一节外，以下命令都在项目根目录执行。

## 前置条件

- Linux amd64 或 arm64，rootful Docker Engine + Compose v2；仓库的 host 覆盖文件需支持
  `!reset`，独立 `compose.host.yaml` 不使用此标签。
- 宿主存在 `/dev/net/tun`，可加载 `ifb`、`sch_htb`、`act_mirred`。
- 不把 Windows/macOS Docker Desktop、rootless Docker 当作已支持生产环境。

先运行检查：

```bash
./deploy/check-host.sh --data-dir ./data
```

检查必须没有 `[fail]`。脚本检查宿主信息；以 root 执行时也会尝试加载缺失内核模块。
host 模式需在宿主开启 IPv4 转发并持久化：

```bash
printf 'net.ipv4.ip_forward=1\n' | sudo tee /etc/sysctl.d/90-ovpanel.conf
sudo sysctl -p /etc/sysctl.d/90-ovpanel.conf
```

## 宝塔 / 1Panel 部署

先满足上文的 Linux、TUN、内核模块和 IPv4 转发要求。宿主没有仓库时，可手动检查
`/dev/net/tun` 及 `ifb`、`sch_htb`、`act_mirred` 模块；启动后再检查 OpenVPN 容器健康状态。

1. 在面板中创建 Compose 编排，粘贴 [`compose.host.yaml`](../compose.host.yaml) 的全部内容。
   [1Panel 支持 Web 编辑器创建编排](https://docs.fit2cloud.com/1panel/user_manual/containers/compose/)。
2. 将 [`.env.host.example`](../.env.host.example) 保存为编排目录下的 `.env`，或将参数
   填入面板的编排环境变量配置。通过这些变量调整镜像标签、数据目录、管理端口和 HTTPS，
   两个镜像使用同一个发布标签。已有 `.env` 时合并所需参数，不覆盖原有配置。
   当前示例默认使用 `latest` 正式版，生产环境建议固定为 `0.0.5` 或更新的正式版本。
3. 保存并启动编排。保留 `openvpn` 的 `network_mode: host`，不要额外添加端口映射或容器 sysctl。
4. 在面板的 `ovpanel-panel` 容器终端运行 `cat /data/bootstrap-token` 获取初始化口令。
   也可在宿主执行：

   ```bash
   docker exec ovpanel-panel cat /data/bootstrap-token
   ```

5. 默认管理地址仍为 `http://127.0.0.1:8080`。在自己的电脑建立 SSH 隧道后访问并初始化：

   ```bash
   ssh -L 8080:127.0.0.1:8080 <SSH用户>@<服务器地址>
   ```

数据默认保存在宿主 `/opt/ovpanel/data`，初始化口令位于其 `panel/bootstrap-token`，
初始化成功后删除。HTTPS 证书放入 `/opt/ovpanel/certs`，在 `.env` 或面板的编排环境变量中设置
`OVPANEL_TLS_CERT=/certs/panel.crt`、`OVPANEL_TLS_KEY=/certs/panel.key`，再调整 `.env` 中的管理绑定地址。
详细要求见下文「HTTPS」，包括反向代理终止 TLS 的限制。

VPN 默认端口为 `1194/udp`，请同步宿主防火墙及云安全组；管理端口 `9443/tcp` 仅监听回环地址。
此文件不需要运行宿主 `ensure-forwarding.sh`。升级前先备份，再在编排环境变量中修改两个镜像标签、
拉取镜像并逐个重建；重建 OpenVPN 会断开连接。不要与已有 OVPanel 编排同时启动。
已有数据迁移时须先停止原编排，再将本文件的数据和证书路径指向原目录，避免创建空实例。

如需脱离管理面板使用命令行，将 Compose 与环境变量示例放在同一目录，首次创建 `.env`
并按需修改；已有 `.env` 时合并参数：

```bash
cp -n .env.host.example .env
docker compose --env-file .env -f compose.host.yaml pull
docker compose --env-file .env -f compose.host.yaml up -d
docker compose --env-file .env -f compose.host.yaml ps
```

`.env` 用于 Compose 的变量替换，镜像、端口、挂载路径及容器运行参数都会读取对应值；
无需额外添加服务级 `env_file`。

## 首次启动（仓库 host 部署）

```bash
cp .env.example .env
```

编辑 `.env`，选择两个镜像使用相同的发布标签。例如使用最新正式版：

```dotenv
# 项目根目录的 .env
OVPANEL_PANEL_IMAGE=dreamstation625/ovpanel:latest
OVPANEL_OPENVPN_IMAGE=dreamstation625/ovpanel-openvpn:latest
```

```bash
docker compose -f compose.yaml -f compose.hostnet.yaml pull
docker compose -f compose.yaml -f compose.hostnet.yaml up -d --no-build
docker compose -f compose.yaml -f compose.hostnet.yaml ps
```

`latest` 跟随 `main` 的最新正式版本，`dev` 是 `dev` 分支的预发布镜像。
建议生产使用 [Release](https://github.com/dreamstation625/OVPanel/releases) 中的具体版本标签；
开发分支的 `.env.example` 使用 `dev`，部署前请确认。

默认面板只绑定 `127.0.0.1:8080`，不会直接暴露到局域网。

### 网络边界

- 使用 `!reset` 清除 OpenVPN 的 bridge 网络、端口映射与容器 sysctl。
- OpenVPN 管理端口只监听 `127.0.0.1:9443`，数据端口直接监听宿主。
- 宿主的 `9443/tcp` 与 VPN 数据端口不得被其他进程占用；云安全组需放行 VPN 数据端口。
- entrypoint 会为 VPN 地址池添加 UFW 转发规则，并放行
  `OVPANEL_VPN_PORT`/`OVPANEL_VPN_PROTO`；UFW reload 后由容器内循环重新补上。
- OpenVPN 的 nft 规则位于宿主网络命名空间，需要宿主已开启
  `net.ipv4.ip_forward=1`；需要隔离 OpenVPN 网络时使用下文的 bridge 模式。
- `.env` 中的 `OVPANEL_VPN_PORT`/`OVPANEL_VPN_PROTO` 必须与面板服务端配置一致。
- 后续操作保留两个 `-f` 参数；host 模式不使用 `deploy/compose.sh` 或
  `deploy/ensure-forwarding.sh`，这两个脚本用于 bridge 部署。

### 初始化管理员

浏览器打开 `http://127.0.0.1:8080`。容器通过 Docker 端口映射访问，请求源地址是
Docker 网关而不是回环地址，因此必须输入一次性初始化口令。读取方式：

```bash
docker compose -f compose.yaml -f compose.hostnet.yaml exec panel cat /data/bootstrap-token
```

远程服务器可先在自己的电脑执行 `ssh -L 8080:127.0.0.1:8080 <SSH用户>@<服务器地址>`，
再在本机浏览器访问上述地址。

初始化成功后管理员落库，引导口令文件自动删除，初始化接口永久关闭。面板没有
默认管理员密码。

### bridge 网络（可选）

基础 `compose.yaml` 保留 bridge 模式。首次准备环境、选择镜像后，使用部署脚本启动：

```bash
./deploy/compose.sh pull
./deploy/compose.sh up -d --no-build
./deploy/compose.sh ps
```

下文操作命令默认使用推荐的 host 模式；bridge 部署时，将
`docker compose -f compose.yaml -f compose.hostnet.yaml` 替换为 `./deploy/compose.sh`，
并按「宿主转发与 NAT」配置转发。

## HTTPS

直接把证书挂载进面板容器：

```bash
mkdir -p certs
# 将 panel.crt 与 panel.key 放入项目根目录的 certs/
```

在项目根目录的 `.env` 中设置：

```dotenv
OVPANEL_ADMIN_BIND=0.0.0.0
OVPANEL_ADMIN_PORT=8443
OVPANEL_TLS_CERT=/certs/panel.crt
OVPANEL_TLS_KEY=/certs/panel.key
OVPANEL_DEV_SELF_SIGNED=false
```

然后只重建面板：

```bash
docker compose -f compose.yaml -f compose.hostnet.yaml up -d --no-build --no-deps --force-recreate panel
```

访问 `https://<宿主地址>:8443`。`OVPANEL_DEV_SELF_SIGNED=true` 只用于本机调试，
会在内存中生成短期自签名证书；它不能替代正式证书。

如果 HTTPS 由外部反向代理终止，面板自身看到的是 HTTP，Cookie 的 `Secure`
标记不会自动开启。当前版本尚未实现代理头信任配置；这种部署方式需要在后续阶段
补齐后再用于公网。

## OpenVPN 数据端口

host 模式下 OpenVPN 直接监听宿主，数据端口与协议由面板「服务端」配置决定；
`.env` 中的 `OVPANEL_VPN_PORT` / `OVPANEL_VPN_PROTO` 供入口脚本放行 UFW，必须与
服务端配置一致。`OVPANEL_VPN_BIND` 仅用于 bridge 模式的端口映射，不控制 host 监听地址。
默认数据端口为 `1194/udp`。

```dotenv
OVPANEL_VPN_BIND=0.0.0.0
OVPANEL_VPN_PORT=21194
OVPANEL_VPN_PROTO=tcp
```

修改后在面板保存并应用相同的协议 / 端口，重建 openvpn 容器，并同步「系统 → VPN
连接地址」中的端口与宿主防火墙 / 云安全组规则，重新下载客户端配置：

```bash
docker compose -f compose.yaml -f compose.hostnet.yaml up -d --no-build --no-deps --force-recreate openvpn
```

管理端口 9443 在 host 模式仅监听宿主回环地址；bridge 模式仅在项目网络内可达，不发布。

## 数据与持久化

宿主持久目录由 `OVPANEL_DATA_ROOT` 控制，默认项目根目录下的 `data`：

```text
data/
  panel/
    panel.db             面板 SQLite（管理员、会话、用户、配置、证书、任务、统计）
    master.key           加密 CA/客户端私钥与 tls-crypt 的主密钥
    bootstrap-token      一次性初始化口令（初始化成功后删除）
    backups/             加密备份包与恢复临时目录
  maintenance/           独立挂载的维护任务状态与 OpenVPN 镜像固定值（仅维护进程可写）
  openvpn/               运行代理账本与会话状态
  openvpn-config/        OpenVPN 配置与 PKI 运行材料
  control/              面板与代理共享控制包/状态/事件
```

- 面板容器以非 root 用户运行；入口脚本只修正 `/data` 顶层所有权。
- **`master.key` 与数据库同等敏感。** 丢失后已有 CA/客户端私钥和 tls-crypt
  密钥无法解密；备份与迁移必须同时保存它。也可通过 `OVPANEL_MASTER_KEY`
  注入 32 字节主密钥，避免在数据目录落盘。
- 加密备份与恢复通过面板「备份」页面完成；**不要**在运行中直接复制 `panel.db`。
  备份包包含数据库一致性快照、代理账本与主密钥，恢复密码需单独保管。
- 面板与 OpenVPN 不共享可写 SQLite 文件。

### 从旧维护目录升级

更新镜像时同时更新 Compose：面板新增 `data/maintenance` 到
`/var/lib/ovpanel-maintenance` 的独立挂载，不能继续把维护状态放在 Web 可写的
`/data/maintenance` 内。维护进程会拒绝可被其他用户替换的目录或任务文件。
执行升级前先确认没有正在进行的 OpenVPN 更新或恢复任务。

旧的 `data/panel/maintenance/task.json` 不自动迁移或执行。旧镜像固定值也不自动
信任：先核对当前 OpenVPN 容器实际镜像，将确认的版本填写到 `.env` 的
`OVPANEL_OPENVPN_IMAGE`，再按原部署方式重建。旧目录保留供人工核对，不需要删除。
`deploy/compose.sh` 此后从新的 `data/maintenance/openvpn-image.env` 加载固定值。

### 凭据与权限变更的生效状态

用户停用、证书吊销、密码和权限变更保存后，面板只更新已有运行配置中的认证快照
与 CRL，不依赖尚在编辑的客户端导出端口。VPN 尚未确认时接口返回 `202`，保留
一次性密码等返回数据，页面显示“VPN 尚未确认生效”；后台每 5 秒核对并重试。
升级时同时更新两个镜像，运行代理需支持控制包哈希确认。

同一服务端配置下，凭据或权限更新若在代理应用阶段失败，VPN 会停止，避免回退
成已撤销的旧授权。排除磁盘、证书或网络规则错误后，后台重试可恢复服务。
仅服务端配置的候选变更仍保留原有失败回退流程；候选同时携带凭据变更时，
同样不能回退为旧授权。

VPN 隧道不开放宿主本机服务，包括服务端 VPN 地址上的 DNS/SSH/管理端口；DNS
请使用可转发访问的服务器。host 模式下，宿主机所在的物理局域网可以作为目标网段
授权给客户端，只有宿主机自身的地址仍不可访问。host 模式的 ACL、IPv6 阻断和 NAT
仅作用于 VPN 接口，不接管其他容器的流量。

## 网络能力检查

宿主侧：

```bash
./deploy/check-host.sh --data-dir ./data
```

容器侧（通过容器内回环地址访问）：

```bash
docker compose -f compose.yaml -f compose.hostnet.yaml exec openvpn wget -qO- http://127.0.0.1:9443/readyz
docker compose -f compose.yaml -f compose.hostnet.yaml exec openvpn wget -qO- http://127.0.0.1:9443/v1/environment
```

- `/healthz` 只表示代理进程存活。
- `/readyz` 包含 TUN、OpenVPN、nft、tc、ip 与内核模块检查，缺项时返回 503。
- `/v1/environment` 返回完整报告与当前配置状态。

## 宿主转发与 NAT

可选的 bridge 模式下，`./deploy/compose.sh up -d` 在容器启动后会自动检查当前
`ovpanel` 网络，并为该 IPv4 子网确保 UFW 转发和 NAT。自动配置需要 root 或免密
sudo，只处理本项目网络，规则持久化到 UFW；可用
`OVPANEL_AUTO_FORWARD=0 ./deploy/compose.sh up -d` 关闭。

自动配置目前覆盖 UFW。若宿主使用其他防火墙，或 Docker 已关闭 iptables 且没有
UFW，脚本会明确报错，需要手动配置；Docker 自身已提供 NAT 且 UFW 未启用时会跳过。
容器公网出口会顺带做一次短测试，失败时只警告，不阻断容器启动。

`compose.sh` 之外的入口（裸 `docker compose up -d`、1Panel Compose）不会自动执行
上述检查。首次启动后手动运行一次；规则持久化后，只要 `ovpanel` 子网不变，不需要
每次部署都执行：

```bash
sudo OVPANEL_HOST_NETWORKS_FILE="$(pwd)/data/control/host-networks.txt" \
  ./deploy/ensure-forwarding.sh
```

1Panel 宿主机没有仓库目录时，把 `deploy/ensure-forwarding.sh` 复制上去执行，并把
`OVPANEL_HOST_NETWORKS_FILE` 指向数据目录下的 `control/host-networks.txt`
（独立 `compose.host.yaml` 默认是 `/opt/ovpanel/data/control/host-networks.txt`）。

host-network 模式不需要执行这个脚本；OpenVPN 容器 entrypoint 会维护自己的
VPN 地址池转发和 UFW 数据端口规则。两种模式不要同时启用转发配置。

## 版本检查与升级

面板侧栏显示当前版本，并会检查 GitHub 正式 Release；有新的正式版本时显示提示，
可在「版本与更新」中查看版本号和更新说明。dev 和预发布 Release 不参与更新判断。
面板不会在网页内执行更新。面板需要能访问 `api.github.com`；网络不通时只提示
检查失败，不影响其他功能。

升级时按 Release 说明把面板与 OpenVPN 镜像切到同一版本，再逐个重建；先更新
面板不影响在线连接，重建 OpenVPN 会断开现有客户端，应安排在维护窗口：

```bash
# 1. 先创建并验证加密备份
# 2. 在 .env 中设置对应版本的镜像
#    OVPANEL_PANEL_IMAGE=dreamstation625/ovpanel:0.0.5
#    OVPANEL_OPENVPN_IMAGE=dreamstation625/ovpanel-openvpn:0.0.5
docker compose -f compose.yaml -f compose.hostnet.yaml pull
docker compose -f compose.yaml -f compose.hostnet.yaml up -d --no-build --no-deps --force-recreate panel
docker compose -f compose.yaml -f compose.hostnet.yaml up -d --no-build --no-deps --force-recreate openvpn
docker compose -f compose.yaml -f compose.hostnet.yaml ps
```

`--no-deps` 避免重建一个容器时联动另一个。升级后检查 `/api/health` 与页面版本。

## 容器边界

- 正常运行只有两个项目容器；可用
  `docker ps --filter label=com.ovpanel.managed` 核对。
- panel 容器内的维护进程持有 Docker Socket；Web 进程不直接打开 Socket，网页也
  不提供 OpenVPN 或面板的更新操作。
- openvpn 推荐的 host 模式直接监听宿主数据端口，管理端口仅 `127.0.0.1:9443`；
  可选的 bridge 模式只发布数据面端口（`OVPANEL_VPN_PORT`/`OVPANEL_VPN_PROTO`，
  须与面板配置一致），两种模式均不挂载 Docker Socket。
- panel 根文件系统只读，仅 `/data`、`/tmp` 与维护 Socket 的 tmpfs 可写。
- openvpn 需要 `NET_ADMIN`、`NET_RAW`、`/dev/net/tun` 与 `net.ipv4.ip_forward=1`；
  host-network 模式的 nft 规则会作用于宿主网络命名空间。

## 开发与测试

本项目只发布 Docker 镜像，不单独发布二进制，也不把直接运行二进制作为部署方式。
开发、联调和验收统一使用 Docker Compose 构建镜像：

```bash
docker compose -f compose.yaml -f compose.hostnet.yaml up -d --build
```

Go 二进制与前端产物只作为镜像构建的中间层，不对外交付。代码检查可使用
`go test ./...` 与 `npm run build`，它们不产生发布包。

完整环境变量见 `internal/config/config.go` 与根目录的 `.env.example`。

## OpenVPN 加密与连接

服务端页面可以选择数据加密算法、控制通道认证算法、TLS 最低版本、keepalive 间隔/超时
和最大连接数。可选的数据加密算法包括 `AES-256-GCM`、`AES-128-GCM`、
`CHACHA20-POLY1305`、`AES-256-CBC`、`AES-128-CBC`；服务端按上述固定顺序协商，
与勾选顺序无关，优先使用 GCM/CHACHA20，CBC 仅用于兼容旧客户端。默认值与旧版本一致：`AES-256-GCM:AES-128-GCM`、
`SHA256`、TLS 1.2、keepalive `10 60`，最大连接数不额外限制。

导出的客户端 `.ovpn` 会使用当前生效服务端的加密设置。修改加密算法、认证或
TLS 最低版本并应用后，需要重新下载客户端配置并重连；只支持旧算法或旧 TLS
版本的客户端可能无法连接。选择 `CHACHA20-POLY1305` 或 TLS 1.3 前，请确认
客户端版本支持。

## 客户端内网共享

除了服务端自己能访问的目标网段，还可以让一台客户端把它后面的内网共享给其他
VPN 客户端。例如客户端 A 位于分支办公室，A 连接 VPN 后，其他有权限的客户端
可以访问 A 后面的 `192.168.50.0/24`。

配置方式：

- 在 A 的配置详情中填写「路由通告」；
- 服务端页面的「服务端设置」里打开「允许访问其他客户端内网」，或在配置的「访问其他客户端内网」
  中单独设为允许；
- 共享内网只支持私网/CGNAT IPv4（`10/8`、`172.16/12`、`192.168/16`、`100.64/10`），
  不能与 VPN 地址池、服务端本地网络或其他配置共享的网段重叠；
- 「路由通告」只能填写客户端后面的内网，不要填写宿主机所在或已能访问的网段；
  填错可能覆盖服务端路由，导致宿主机或面板失联；
- 同一个网段只能由一个配置共享。建议共享配置使用固定 IP，并只保持一条连接。

宿主网段保护：

- host 模式由 `ensure-host-forwarding.sh` 自动把宿主接口和路由网段写入
  `/control/host-networks.txt`，面板和运行代理会拒绝与宿主网段重叠的
  「路由通告」或 VPN 网段；
- bridge/NAT 模式由 `compose.sh` 自动把清单写入 panel 的 `/control` 目录；
  使用裸 `docker compose` 或 1Panel 时，手动执行：
  `sudo OVPANEL_HOST_NETWORKS_FILE=<data>/control/host-networks.txt ./deploy/ensure-forwarding.sh`；
- 修改宿主网卡或路由后，重新执行上面的脚本更新清单；清单缺失时面板无法提前拦截，
  仍以界面警告和运行代理日志为准。

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
