# OVPanel

OpenVPN 服务端管理面板。单实例部署，面向少量管理员与 20 条以内并发连接；
Go 提供 HTTP 服务并内嵌 Vue 3 前端，SQLite 持久化。
支持 Linux amd64 与 arm64。

## 架构

严格两个容器：

- `panel`：面板 Web、内嵌前端与 SQLite 数据，唯一发布管理端口的容器。
- `openvpn`：OpenVPN 与运行代理，不发布管理端口，不挂载 Docker Socket。

面板前后端一体，运行期不需要 Node.js。面板没有网页自更新入口，升级通过宿主机
Docker 命令完成。初始化为一次性口令，无默认管理员密码。

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
| `OVPANEL_DATA_DIR` | `data` | 数据目录（SQLite、主密钥、引导口令） |
| `OVPANEL_LISTEN` | `:8080` | 监听地址 |
| `OVPANEL_TLS_CERT` / `OVPANEL_TLS_KEY` | 空 | 同时设置时启用 HTTPS |
| `OVPANEL_DEV_SELF_SIGNED` | `false` | 仅本机调试的短期自签名证书 |
| `OVPANEL_BOOTSTRAP_TOKEN` | 空 | 可选的初始化口令（至少 16 字符） |
| `OVPANEL_MASTER_KEY` | 空 | 可选主密钥（32 字节 base64/hex）；留空生成 `master.key` |
| `OVPANEL_SESSION_TTL` | `12h` | 管理员会话时长，最大 7 天 |
| `OVPANEL_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

`master.key` 与数据库同等敏感：丢失后无法解密已有证书私钥与 tls-crypt 密钥。
备份与迁移必须同时保存它；当前版本尚未提供备份功能。

## Docker 部署

```bash
cp .env.example .env
./deploy/check-host.sh --data-dir ./data
docker compose up -d --build
```

默认只绑定 `127.0.0.1:8080`，不会直接暴露到局域网。部署细节、HTTPS、容器边界、
数据目录和升级方式见 [deploy/README.md](deploy/README.md)。

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
- 两个镜像都是 `linux/amd64` + `linux/arm64`：
  `dreamstation625/ovpanel`、`dreamstation625/ovpanel-openvpn`。
- 工作流读取仓库环境 `DOCKERHUB` 的变量 `DOCKERHUB_USERNAME` 与保密值
  `DOCKERHUB_TOKEN`。

发布正式版本只需修改 `VERSION` 并推送 `main`：例如从 `0.0.1` 改为 `0.0.2`，
工作流会创建 `0.0.2` 的 Release，并推送同名镜像与 `latest`。

## 目录

```text
compose.yaml       严格双容器部署入口
.env.example       环境变量示例
cmd/panel/         面板进程入口（Web + 内嵌前端）
cmd/vpn-agent/     OpenVPN 容器进程（运行代理）
internal/accounts/ 用户、配置、证书与导出编排
internal/api/      路由、错误体、中间件、静态资源
internal/auth/     Argon2id 密码、会话与 CSRF 令牌
internal/bootstrap/一次性初始化口令
internal/config/   环境变量配置
internal/devtls/   本机调试自签名证书
internal/logging/  脱敏 JSON 日志
internal/openvpnprofile/ .ovpn 与 tls-crypt 生成
internal/pki/      CA 与客户端证书生命周期
internal/runtimeenv/容器数据面能力探测
internal/secretbox/主密钥与私密材料加密
internal/store/    SQLite 与迁移
web/               Vue 3 + Vue Router 源码，构建产物内嵌
deploy/            Dockerfile、环境变量示例、宿主检查、运行手册
```
