# EasyHostAgent

EasyHostAgent 是面向受授权管理控制端的 Go 原生 Linux / FnOS 主机代理。**EasyNetTopo V1.0 是首个正式对接控制端**；一台 Agent 同时只允许一个有效控制端授权，成功重新配对会撤销旧授权。Agent 不依赖 EasyTier 组网，可通过直接可达的 IP:port + HTTPS 管理，也可在可信网络显式启用 HTTP。

支持 HTTP/HTTPS 身份配对、Docker/Compose 资源发现和按摘要缓存应用包。主机管理员授权指定应用源后，控制端可安装、卸载并批量查询 Docker 应用。详见本文「Docker 应用商店」。下文 T01/T02 和 M3 章节保留各阶段的开发记录。

## 推荐部署：GitHub Actions → GHCR → deploy

EasyHostAgent 的默认集成部署方式现为 **Docker Compose 单容器**，GitHub Actions 自动构建并发布 AMD64/ARM64 镜像 `ghcr.io/cibederorg/easyhostagent:feat-docker-app-store`。安装端不编译 Go、不下载二进制 Release。完整配置、安装、更新与卸载命令见 [deploy 的 EasyHostAgent 指南](https://github.com/CiBeDerORG/deploy/blob/feat/docker-app-store/easyhostagent/README.md)。

该镜像基于官方 Docker CLI 运行时（包含 `docker compose`），需要挂载宿主机 `/var/run/docker.sock` 和相同绝对路径的软件根目录，因此具有**宿主机 Docker 管理权限**。容器以 root 运行；建议仅在可信网络使用，默认 HTTPS、可配置 HTTP，不发送心跳。容器内根据 `EASYHOSTAGENT_PLATFORM=fnos|linux` 保持目标主机的平台标识，避免将 fnOS 误识别成镜像自身的 Alpine Linux。

下方原生 systemd 安装器保留用于独立使用；`deploy.sh easyhostagent` 已不再走原生安装器。开发版二进制 Release 的自动发布流程已经停用，推荐使用 GHCR 镜像。

## 工程结构

```text
cmd/easyhostagent/       CLI 入口（-version、-check-config、-identity、-pairing-code）
internal/app/            HTTP / TLS 1.3 HTTPS 服务生命周期与优雅退出
internal/config/         软件根目录、状态目录、日志等级
internal/logging/        标准库 log/slog JSON 结构化日志
internal/buildinfo/      构建版本元数据
internal/identity/       私有身份状态文件、Agent UUID、TLS 证书与指纹
internal/httpapi/        HTTP/HTTPS health / identity / pairings 与应用操作 API
internal/auth/           Bearer + Controller/Agent ID 鉴权中间件
internal/pairing/        一次性配对窗口与持久授权仓库
docs/                    架构与协议文档
tests/contracts/         M0 契约测试和 Docker 示例
```

## 编译与验证

Go >= 1.23，**仅依赖 Go 标准库**。

```bash
go test ./...
go vet ./...
go build -trimpath -o bin/easyhostagent ./cmd/easyhostagent
./bin/easyhostagent -version
./bin/easyhostagent -check-config
```

运行身份发现服务（输出启动日志，HTTPS 监听 7014 端口，等待 SIGTERM/SIGINT）：

```bash
./bin/easyhostagent
```

交叉编译：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/easyhostagent-linux-amd64 ./cmd/easyhostagent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o dist/easyhostagent-linux-arm64 ./cmd/easyhostagent
```

GitHub Actions 工作流 `.github/workflows/go-build.yml` 会执行 gofmt、go vet、带 race 的单元测试，以及 AMD64/ARM64 构建并保存短期测试产物。**这不是发行版本或正式安装包**。

## FnOS / Linux systemd 原生安装器（T01-02）

安装器和 unit 见 [scripts/install.sh](scripts/install.sh)、[scripts/uninstall.sh](scripts/uninstall.sh) 和 [packaging/systemd/easyhostagent.service](packaging/systemd/easyhostagent.service)。

**当前还没有发布正式 GitHub Release**；在获得 GitHub Actions 的测试版 `easyhostagent-foundation-linux-amd64` / `-arm64` 二进制后，放到目标宿主机，从本仓库克隆完整安装器及 unit，执行：

```bash
git clone --depth 1 https://github.com/CiBeDerORG/EasyHostAgent.git
cd EasyHostAgent
chmod +x /absolute/path/easyhostagent-linux-amd64
sudo bash scripts/install.sh --binary /absolute/path/easyhostagent-linux-amd64
sudo systemctl status easyhostagent.service --no-pager
```

ARM64 主机改为实际下载的 `easyhostagent-foundation-linux-arm64`。文件名不限，但二进制必须可以在宿主机原生运行并支持 `-version`。

**未来发布正式 GitHub Release 后**（release 需要提供 `easyhostagent-linux-amd64` 或 `easyhostagent-linux-arm64` 和同名的 `.sha256` 文件）：

```bash
sudo bash scripts/install.sh --release v1.2.3
```

这里的 `v1.2.3` **仅为命令格式示例，不代表该版本已发布**；支持 `--release latest`，但更建议指定固定版本。安装时必须先匹配下载的 SHA-256 才会安装。SHA-256 仅验证下载与发布文件的一致性，并非独立代码签名；仍需信任 GitHub Release 来源。

安装器支持 `--software-root /vol3/1000/software`、`--data-dir /var/lib/easyhostagent`、`--log-level info`、`--no-start`。初次安装生成 `/etc/easyhostagent/agent.env`，设为 0600；状态目录 0700。以后重装**保持原环境配置**，要改变参数须明确编辑配置文件，避免覆盖用户数据或变更根目录。

用于无 root / 无 systemd 的本地验证（不会启动 systemctl）：

```bash
mkdir -p /tmp/eha-test-root
bash scripts/install.sh --binary /absolute/path/easyhostagent-linux-amd64 --staging-root /tmp/eha-test-root
bash scripts/uninstall.sh --staging-root /tmp/eha-test-root
```

正常卸载：

```bash
sudo bash scripts/uninstall.sh
```

仅移除 installer 管理的二进制与 systemd unit，**保留 Agent 身份、`/etc/easyhostagent/` 配置、`/var/lib/easyhostagent/` 数据、SOFTWARE_ROOT 下应用、Docker 容器和卷**。系统服务以 root 身份运行，后续 Docker 管理需要宿主机特权；请仅在你信任的机器部署。不存在自动回滚。

[FnOS/Linux 安装器测试记录](docs/T01-02-native-installer.md) · [安装器 CI](https://github.com/CiBeDerORG/EasyHostAgent/actions/workflows/installer-tests.yml)

## Agent 身份与 HTTPS 接口（T01-03）

首次运行时，Agent 在 `EASYHOSTAGENT_DATA_DIR/identity.json` 生成稳定 UUID、ECDSA P-256 私钥与自签名证书。目录权限要求 0700，状态文件 0600；证书/状态损坏时拒绝启动，**绝不自动重新生成身份并覆盖现有密钥**。

本地终端显示唯一身份与需要人工核对的 SPKI SHA-256 公钥指纹：

```bash
sudo /usr/local/bin/easyhostagent -identity
```

HTTPS API v1 暂时仅实现以下两个无需 Token 的发现接口：

| 请求 | 返回 |
|---|---|
| `GET https://设备IP:7014/v1/health` | `{ "status": "ready", "protocol_version": 1 }` |
| `GET https://设备IP:7014/v1/identity` | Agent UUID、主机名、平台、架构、版本、公钥 SHA-256、时间 |

**重要**：当前证书是自签名证书，普通 HTTPS 客户端没有自动信任链。后续 EasyTier 必须通过用户在宿主机可信终端核对的 SPKI 指纹固定证书信任，绝对不能因为网络接口自己返回同样的指纹就认为已认证，也不能在配对/管理链路中跳过 TLS 认证。

HTTPS 默认监听 `0.0.0.0:7014`，可在初次安装或编辑 `/etc/easyhostagent/agent.env` 添加 `EASYHOSTAGENT_LISTEN_ADDRESS=127.0.0.1:7014` 限制为本机。由于默认允许本地网络连接，请用路由器/宿主机防火墙仅放行可信 EasyTier 控制端；无需将 7014 映射到公网。

**T01-04 新增 HTTPS 配对及授权撤销，但仍不开放 Docker、脚本或应用安装操作。**

在宿主机本地受信任终端执行：

```bash
sudo /usr/local/bin/easyhostagent -pairing-code
```

程序显示 Agent UUID、公钥指纹、一次性配对码及 UTC 到期时间。配对码由安全随机数生成（128 bit），有效期 10 分钟，错误尝试最多 5 次，成功后不可再次使用。**配对前必须在可信终端核对 TLS SPKI 指纹，不要跳过证书验证**；EasyTier Rust 的固定指纹配对接入归 T01-06。

HTTP 配对契约：

| 方法 | 路径 | 权限 |
|---|---|---|
| POST | `/v1/pairings` | TLS + 一次性码，返回只显示一次的 Bearer Token |
| DELETE | `/v1/pairings/current` | Bearer Token + Controller ID + Agent ID，成功后撤销授权 |

本地 `/var/lib/easyhostagent/pairing.json` 保存控制端绑定 ID、错误次数和**配对码/Token 的 SHA-256 摘要，不保存明文**，以 0600 权限保护。重新配对前旧授权继续生效，成功后旧 Token 立即失效。系统安装器卸载/重装时保留该目录；身份状态损坏会拒绝启动而不是自动重置。

[配对实现与验收报告](docs/T01-04-pairing.md) · [Go race 与双架构 CI](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40428675525)

## 配置（环境变量）

| 变量 | Linux 默认值 | FnOS 检测后默认值 |
|---|---|---|
| `EASYHOSTAGENT_DATA_DIR` | `/var/lib/easyhostagent` | `/var/lib/easyhostagent` |
| `EASYHOSTAGENT_SOFTWARE_ROOT` | `/opt/software` | `/vol3/1000/software` |
| `EASYHOSTAGENT_LOG_LEVEL` | `info` | `info` |
| `EASYHOSTAGENT_LISTEN_ADDRESS` | `0.0.0.0:7014` | `0.0.0.0:7014` |
| `EASYHOSTAGENT_TRANSPORT` | `https` | `https` |

FnOS 目前仅根据 `/etc/os-release` 的 `ID/NAME/ID_LIKE` 和 `/etc/fnos-release` 进行保守的识别；用户可使用 `EASYHOSTAGENT_SOFTWARE_ROOT` 明确覆盖。后续 T01-07 负责完善 FnOS 兼容与 UI 可配置路径，以及存在受管应用时禁止直接迁移目录的业务规则。

**HTTP 模式（可选）**：在 `/etc/easyhostagent/agent.env` 设置 `EASYHOSTAGENT_TRANSPORT=http` 和 `EASYHOSTAGENT_LISTEN_ADDRESS=0.0.0.0:7014`（或可信网卡的具体 IP），随后重启 `easyhostagent.service`。EasyNetTopo 中登记 `http://<Agent-IP>:7014`；不写协议的旧格式 `IP:7014` 仍默认为 HTTPS。HTTP 使用与 HTTPS 相同的配对码、Bearer Token、来源授权和安装/卸载/状态接口，但**没有传输加密，登录令牌、安装参数及返回信息可能被旁路读取或篡改**，只能用于可信隔离网络，禁止直接暴露公网；强烈建议使用 HTTPS。服务不会主动发送心跳，不会定时轮询控制端。

配置校验仅读取参数，不会创建目录、修改文件或运行 Docker。身份命令首次调用时会写入本地受保护的状态；配对码仅经本地 CLI 显示。日志使用 JSON Lines，标准输出由 systemd/journald 收集。

## 历史计划说明

此前依据 EasyTier 集成路线编写的需求计划和任务计划已经撤销。M3 文档记录了安装包缓存阶段，当前商店操作见下节。

应用源由 EasyNetTopo 拉取，Agent 按需执行并保留失败现场。

## Docker 应用商店

配对后，管理员从 EasyNetTopo 页面核对控制端 ID 和 Git 来源 ID，在主机的 `/etc/easyhostagent/agent.env` 设置 `EASYHOSTAGENT_APP_GRANTS=<控制端UUID>/<来源UUID>`，然后重启 Agent。多个授权用逗号分隔。移除授权并重启即停止接受该来源的新操作。未设置时仍可查询状态。授权意味着信任该控制端为指定来源选择和传送脚本，脚本以 Agent 服务用户身份执行。

控制端通过已配对的 HTTP 或 HTTPS 连接调用 `POST /v1/apps/install`、`uninstall`、`status`（三个接口共用 `/v1/apps/` 前缀）。安装传 `source_id`、`app_id`、`bundle_sha256` 和清单允许的 `config`；卸载只传前两项。批量查询传 `{"apps":[{"source_id":"…","app_id":"…"}],"include_managed":true}`。动作受理后返回当前结果，后续查询同时获得执行结果和 Docker 运行状态。每台主机同时执行一个应用操作。

每个应用使用独立的软件根目录 `<SOFTWARE_ROOT>/.easyhostagent-apps/<来源UUID>/<应用ID>/`。该目录下的 `deploy/.env` 和 `deploy/.secrets` 保存普通配置和密钥，卸载保留配置和应用数据。软件根目录及上级须防止被其他用户替换。脚本应按字面量读取这两份配置，每行 `KEY=VALUE`，读取时去除一层成对引号；禁止把配置作为 Shell 代码执行。包内脚本输出可能包含初始密码，接口仅返回结果与退出码。
