# OVPanel 部署与运行

本目录提供面板、初始化流程、HTTPS、持久化目录、部署前检查、用户/配置/证书管理、
客户端配置导出和严格双容器部署文件。

**范围限制：OpenVPN 容器只运行健康检查与数据面能力代理，尚未启动 OpenVPN。
面板侧的用户、证书与导出已经可用，但运行代理的实际准入、CRL 应用、断连、限流
和真实 VPN 连接尚未接入。** 容器显示 healthy 只代表进程与运行环境检查通过，
不代表 VPN 已可用。

## 目录

```text
deploy/
  compose.yaml            严格两个服务
  Dockerfile.panel        Vue 构建 + Go 内嵌 + 非 root 运行
  Dockerfile.openvpn      OpenVPN 2.7.7 与运行代理骨架
  entrypoint-panel.sh     修正数据目录所有权后降权运行
  check-host.sh           宿主 Docker、TUN、nft、tc、内核模块检查
  .env.example            环境变量示例
  data/                   运行时数据（Git 忽略，首次启动创建）
  certs/                  HTTPS 证书挂载目录（Git 忽略）
```

## 前置条件

- Linux amd64 或 arm64，rootful Docker Engine + Compose v2。
- 宿主存在 `/dev/net/tun`，可加载 `ifb`、`sch_htb`、`act_mirred`。
- 不把 Windows/macOS Docker Desktop、rootless Docker 当作已支持生产环境。

先运行检查：

```bash
cd deploy
./check-host.sh --data-dir ./data
```

检查必须没有 `[fail]`。脚本只读取宿主信息；加载缺失内核模块需要 root。

## 首次启动

```bash
cd deploy
cp .env.example .env
docker compose up -d --build
docker compose ps
```

默认只绑定 `127.0.0.1:8080`，不会直接暴露到局域网。

也可以不本地构建，直接使用 Docker Hub 上的发布镜像：

```dotenv
# deploy/.env
OVPANEL_PANEL_IMAGE=dreamstation625/ovpanel:0.0.1
OVPANEL_OPENVPN_IMAGE=dreamstation625/ovpanel-openvpn:0.0.1
```

```bash
cd deploy
docker compose pull
docker compose up -d --no-build
```

`latest` 跟随 `main` 的最新正式版本，`dev` 是 `dev` 分支的预发布镜像。
版本规则见仓库 README 的「版本与发布」。

浏览器打开 `http://127.0.0.1:8080`：

- 本机直接运行 Go 二进制时，回环直连可以在没有口令的情况下初始化。
- Docker 端口映射下，请求源地址是 Docker 网关而不是回环地址，因此必须输入
  一次性初始化口令。读取方式：

  ```bash
  docker compose exec panel cat /data/bootstrap-token
  ```

初始化成功后管理员落库，引导口令文件自动删除，初始化接口永久关闭。面板没有
默认管理员密码。

## HTTPS

直接把证书挂载进面板容器：

```bash
mkdir -p certs
# 将 panel.crt 与 panel.key 放入 deploy/certs/
```

在 `deploy/.env` 中设置：

```dotenv
OVPANEL_ADMIN_BIND=0.0.0.0
OVPANEL_ADMIN_PORT=8443
OVPANEL_TLS_CERT=/certs/panel.crt
OVPANEL_TLS_KEY=/certs/panel.key
OVPANEL_DEV_SELF_SIGNED=false
```

然后只重建面板：

```bash
docker compose up -d --no-deps --force-recreate panel
```

访问 `https://<宿主地址>:8443`。`OVPANEL_DEV_SELF_SIGNED=true` 只用于本机调试，
会在内存中生成短期自签名证书；它不能替代正式证书。

如果 HTTPS 由外部反向代理终止，面板自身看到的是 HTTP，Cookie 的 `Secure`
标记不会自动开启。P02 尚未实现代理头信任配置；这种部署方式需要在后续阶段
补齐后再用于公网。

## 数据与持久化

宿主持久目录由 `OVPANEL_DATA_ROOT` 控制，默认 `deploy/data`：

```text
data/
  panel/
    panel.db             面板 SQLite（管理员、会话、用户、配置、证书、任务）
    master.key           加密 CA/客户端私钥与 tls-crypt 的主密钥
    bootstrap-token      一次性初始化口令（初始化成功后删除）
  openvpn/               运行代理状态目录（尚未写入账本）
  openvpn-config/        OpenVPN 配置与 PKI 的未来挂载点
```

- 面板容器以非 root 用户运行；入口脚本只修正 `/data` 顶层所有权。
- **`master.key` 与数据库同等敏感。** 丢失后已有 CA/客户端私钥和 tls-crypt
  密钥无法解密；备份与迁移必须同时保存它。也可通过 `OVPANEL_MASTER_KEY`
  注入 32 字节主密钥，避免在数据目录落盘。
- 备份与恢复在 P06 实现。**不要**在运行中直接复制 `panel.db` 当作一致备份。
- 面板与 OpenVPN 不共享可写 SQLite 文件。

## 网络能力检查

宿主侧：

```bash
cd deploy
./check-host.sh --data-dir ./data
```

容器侧（不发布端口，只在容器网络内访问）：

```bash
docker compose exec openvpn wget -qO- http://127.0.0.1:9443/readyz
docker compose exec openvpn wget -qO- http://127.0.0.1:9443/v1/environment
```

- `/healthz` 只表示代理进程存活。
- `/readyz` 包含 TUN、OpenVPN、nft、tc、ip 与内核模块检查，缺项时返回 503。
- `/v1/environment` 返回完整报告；`configured` 在 P02 固定为 `false`。

## 面板升级

P02 只提供手工升级基线，不提供网页自更新：

```bash
cd deploy
# 1. 先按 P06 提供的方式备份（P06 前不要依赖未实现的备份命令）
# 2. 固定新的 OVPANEL_PANEL_IMAGE 或版本
# 3. 只重建 panel
docker compose up -d --no-deps --force-recreate panel
docker compose ps
```

`--no-deps` 避免联动重建 OpenVPN。升级后检查 `/api/health` 与页面版本。
OpenVPN 的网页更新属于 P07，当前没有入口，也没有第三个容器。

## 容器边界

- 正常运行只有两个项目容器；可用
  `docker ps --filter label=com.ovpanel.managed` 核对。
- panel 不挂载 Docker Socket；P07 的维护进程也只能通过受限接口操作。
- openvpn 不发布 9443，不挂载 Docker Socket。
- panel 根文件系统只读，仅 `/data` 与 `/tmp` 可写。
- openvpn 需要 `NET_ADMIN`、`NET_RAW`、`/dev/net/tun` 与 `net.ipv4.ip_forward=1`。

## 本地开发

前端与后端可以在普通终端分别运行：

```bash
cd web
npm ci
npm run dev          # Vite，代理 /api 到 127.0.0.1:8080
```

```bash
go run ./cmd/panel
```

运行前构建前端产物，或使用 Vite 开发服务器：

```bash
cd web
npm ci && npm run build
cd ..
go run ./cmd/panel
```

面板默认数据目录为相对路径 `data/`；可用 `OVPANEL_DATA_DIR` 指定其他位置。
完整环境变量见 `internal/config/config.go` 与 `.env.example`。
