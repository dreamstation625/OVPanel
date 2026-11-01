# OVPanel 部署与运行

本目录提供 Dockerfile、部署前检查与运行手册；严格双容器部署入口是项目根目录的
`compose.yaml`，环境变量示例是根目录的 `.env.example`。

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

部署入口是项目根目录的 `compose.yaml`；运行数据与证书默认保存在根目录的
`data/`、`certs/`（Git 忽略，首次启动创建）。以下命令都在项目根目录执行。

## 前置条件

- Linux amd64 或 arm64，rootful Docker Engine + Compose v2。
- 宿主存在 `/dev/net/tun`，可加载 `ifb`、`sch_htb`、`act_mirred`。
- 不把 Windows/macOS Docker Desktop、rootless Docker 当作已支持生产环境。

先运行检查：

```bash
./deploy/check-host.sh --data-dir ./data
```

检查必须没有 `[fail]`。脚本只读取宿主信息；加载缺失内核模块需要 root。

## 首次启动

```bash
cp .env.example .env
./deploy/compose.sh up -d --build
./deploy/compose.sh ps
```

默认只绑定 `127.0.0.1:8080`，不会直接暴露到局域网。

也可以不本地构建，直接使用 Docker Hub 上的发布镜像：

```dotenv
# 项目根目录的 .env
OVPANEL_PANEL_IMAGE=dreamstation625/ovpanel:0.0.1
OVPANEL_OPENVPN_IMAGE=dreamstation625/ovpanel-openvpn:0.0.1
```

```bash
./deploy/compose.sh pull
./deploy/compose.sh up -d --no-build
```

`latest` 跟随 `main` 的最新正式版本，`dev` 是 `dev` 分支的预发布镜像。
版本规则见仓库 README 的「版本与发布」。

浏览器打开 `http://127.0.0.1:8080`。容器通过 Docker 端口映射访问，请求源地址是
Docker 网关而不是回环地址，因此必须输入一次性初始化口令。读取方式：

  ```bash
  ./deploy/compose.sh exec panel cat /data/bootstrap-token
  ```

初始化成功后管理员落库，引导口令文件自动删除，初始化接口永久关闭。面板没有
默认管理员密码。

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
./deploy/compose.sh up -d --no-deps --force-recreate panel
```

访问 `https://<宿主地址>:8443`。`OVPANEL_DEV_SELF_SIGNED=true` 只用于本机调试，
会在内存中生成短期自签名证书；它不能替代正式证书。

如果 HTTPS 由外部反向代理终止，面板自身看到的是 HTTP，Cookie 的 `Secure`
标记不会自动开启。当前版本尚未实现代理头信任配置；这种部署方式需要在后续阶段
补齐后再用于公网。

## OpenVPN 数据端口

openvpn 容器把 `OVPANEL_VPN_BIND:OVPANEL_VPN_PORT` 映射到容器内同号端口，协议由
`OVPANEL_VPN_PROTO` 决定；三者必须与面板「服务端」页保存的协议/端口一致，否则
客户端连不上。默认 `0.0.0.0:1194/udp`，与面板默认配置相同。

```dotenv
OVPANEL_VPN_BIND=0.0.0.0
OVPANEL_VPN_PORT=21194
OVPANEL_VPN_PROTO=tcp
```

修改后重建 openvpn 容器：

```bash
./deploy/compose.sh up -d --no-deps --force-recreate openvpn
```

管理端口 9443 只在项目网络内可达，不发布。

## 数据与持久化

宿主持久目录由 `OVPANEL_DATA_ROOT` 控制，默认项目根目录下的 `data`：

```text
data/
  panel/
    panel.db             面板 SQLite（管理员、会话、用户、配置、证书、任务、统计）
    master.key           加密 CA/客户端私钥与 tls-crypt 的主密钥
    bootstrap-token      一次性初始化口令（初始化成功后删除）
    backups/             加密备份包与恢复临时目录
    maintenance/         维护任务状态与 OpenVPN 镜像固定值
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

## 网络能力检查

宿主侧：

```bash
./deploy/check-host.sh --data-dir ./data
```

容器侧（不发布端口，只在容器网络内访问）：

```bash
./deploy/compose.sh exec openvpn wget -qO- http://127.0.0.1:9443/readyz
./deploy/compose.sh exec openvpn wget -qO- http://127.0.0.1:9443/v1/environment
```

- `/healthz` 只表示代理进程存活。
- `/readyz` 包含 TUN、OpenVPN、nft、tc、ip 与内核模块检查，缺项时返回 503。
- `/v1/environment` 返回完整报告与当前配置状态。

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
#    OVPANEL_PANEL_IMAGE=dreamstation625/ovpanel:0.0.2
#    OVPANEL_OPENVPN_IMAGE=dreamstation625/ovpanel-openvpn:0.0.2
./deploy/compose.sh pull
./deploy/compose.sh up -d --no-deps --force-recreate panel
./deploy/compose.sh up -d --no-deps --force-recreate openvpn
./deploy/compose.sh ps
```

`--no-deps` 避免重建一个容器时联动另一个。升级后检查 `/api/health` 与页面版本。

## 容器边界

- 正常运行只有两个项目容器；可用
  `docker ps --filter label=com.ovpanel.managed` 核对。
- panel 容器内的维护进程持有 Docker Socket；Web 进程不直接打开 Socket，网页也
  不提供 OpenVPN 或面板的更新操作。
- openvpn 只发布数据面端口（`OVPANEL_VPN_PORT`/`OVPANEL_VPN_PROTO`，须与面板
  配置一致）；管理端口 9443 不发布，也不挂载 Docker Socket。
- panel 根文件系统只读，仅 `/data`、`/tmp` 与维护 Socket 的 tmpfs 可写。
- openvpn 需要 `NET_ADMIN`、`NET_RAW`、`/dev/net/tun` 与 `net.ipv4.ip_forward=1`。

## 开发与测试

本项目只发布 Docker 镜像，不单独发布二进制，也不把直接运行二进制作为部署方式。
开发、联调和验收统一使用 Docker Compose 构建镜像：

```bash
./deploy/compose.sh up -d --build
```

Go 二进制与前端产物只作为镜像构建的中间层，不对外交付。代码检查可使用
`go test ./...` 与 `npm run build`，它们不产生发布包。

完整环境变量见 `internal/config/config.go` 与根目录的 `.env.example`。
