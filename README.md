# OVPanel

OpenVPN 服务端管理面板。Go 提供 HTTP 服务并内嵌 Vue 3 前端，SQLite 持久化，
严格两个 Docker 容器。

当前阶段：**P02 工程与部署基础（未关闭）**。方案设计、P00 技术实验和 P01
界面设计已完成；P02 的工程骨架、初始化、HTTPS、持久化和本地运行已验证，
Docker 镜像构建与双容器启动尚未在 Linux 环境完成验证。

## 已实现

- 统一 JSON 错误体、请求 ID、请求体大小与格式校验、基础安全头与同源校验。
- 环境变量配置、SQLite 迁移、单管理员约束、会话 Cookie、登录限流。
- 首次初始化：无默认密码；非本机直连必须提供一次性引导口令，成功后口令文件删除、
  入口永久关闭。
- HTTPS：可直接挂载证书与私钥，或在本机调试时生成短期自签名证书。
- 日志脱敏（密码、令牌、Cookie、私钥字段）。
- 长任务状态表，供后续升级、备份、恢复复用。
- Vue 3 + Naive UI 前端：初始化、登录、系统状态与任务列表，深浅主题与手机布局，
  构建产物内嵌 Go，运行期不需要 Node.js。
- OpenVPN 容器运行代理骨架与 TUN/nft/tc/IFB/模块能力报告；**尚未启动 OpenVPN**。

## 普通终端运行

需要 Go 1.26+ 与 Node.js 22（仅构建前端时需要）：

```bash
cd web
npm ci
npm run build
cd ..

go run ./cmd/panel
```

浏览器打开 `http://127.0.0.1:8080`。本机回环直连可直接初始化；Docker 端口映射下
需要读取数据目录中的 `bootstrap-token`。

常用环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `OVPANEL_DATA_DIR` | `data` | 数据目录（SQLite、引导口令） |
| `OVPANEL_LISTEN` | `:8080` | 监听地址 |
| `OVPANEL_TLS_CERT` / `OVPANEL_TLS_KEY` | 空 | 同时设置时启用 HTTPS |
| `OVPANEL_DEV_SELF_SIGNED` | `false` | 仅本机调试的短期自签名证书 |
| `OVPANEL_BOOTSTRAP_TOKEN` | 空 | 可选的初始化口令（至少 16 字符） |
| `OVPANEL_SESSION_TTL` | `12h` | 管理员会话时长，最大 7 天 |
| `OVPANEL_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

## Docker 部署

```bash
cd deploy
cp .env.example .env
./check-host.sh --data-dir ./data
docker compose up -d --build
```

默认只绑定 `127.0.0.1:8080`，不会直接暴露到局域网。部署细节、HTTPS、容器边界、
数据目录和升级基线见 [deploy/README.md](deploy/README.md)。

## 目录

```text
cmd/panel/         面板进程入口（Web + 内嵌前端）
cmd/vpn-agent/     OpenVPN 容器运行代理骨架
internal/api/      路由、错误体、中间件、静态资源
internal/auth/     Argon2id 密码、会话令牌、并发限制
internal/bootstrap/一次性初始化口令
internal/config/   环境变量配置
internal/devtls/   本机调试自签名证书
internal/logging/  脱敏 JSON 日志
internal/runtimeenv/容器数据面能力探测
internal/store/    SQLite 与迁移
web/               Vue 3 源码，构建产物内嵌
deploy/            Dockerfile、Compose、宿主检查、运行手册
```

## 文档

设计资料保存在本地 `docs/`，按项目约定不纳入 Git：产品与界面设计、技术方案、
阶段计划、P01 设计变量与 P02 工程记录。README 与 `deploy/README.md` 是仓库内的
运行入口。

## 验证边界

已完成：Go 单元测试与 `go vet`、前端类型检查与构建、linux amd64/arm64 静态编译、
真实 HTTP/HTTPS 启动、初始化口令门禁与删除、重启后数据保留、1440px 与 390px
浏览器检查。

未完成：Docker 镜像构建、Linux 双容器启动与宿主能力检查、arm64 运行、正式证书链、
OpenVPN 数据面。P02 不把这些写成已通过；P03 起继续完成身份与证书闭环。

部署约束：严格两个容器，面板前后端一体；面板没有网页自更新入口，只通过宿主
Docker 命令升级。最新确认的需求以本地设计文档为准，文档不代表运行验证。
