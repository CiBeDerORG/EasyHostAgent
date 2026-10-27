# EasyHostAgent

EasyHostAgent 是 EasyTier 的 Go 原生主机执行代理。该仓库目前处于 **T01-02 原生安装器完成、T01-03 HTTP 身份服务尚未开始**，首期目标为 Linux / FnOS 上的 Docker / Docker Compose 管理。

> **注意：当前版本仅为可启动/可退出的程序骨架，不提供 HTTP/HTTPS 接口，也不能配对、查询 Docker 或安装应用。请勿将其当作可以实际部署使用的 Agent。**

## 工程结构

```text
cmd/easyhostagent/       CLI 入口（-version、-check-config、信号退出）
internal/app/            主程序生命周期（暂不监听端口、不建立后台任务）
internal/config/         软件根目录、状态目录、日志等级
internal/logging/        标准库 log/slog JSON 结构化日志
internal/buildinfo/      构建版本元数据
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

运行骨架（输出一次启动日志，等待 SIGTERM/SIGINT 后退出）：

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

## 配置（环境变量）

| 变量 | Linux 默认值 | FnOS 检测后默认值 |
|---|---|---|
| `EASYHOSTAGENT_DATA_DIR` | `/var/lib/easyhostagent` | `/var/lib/easyhostagent` |
| `EASYHOSTAGENT_SOFTWARE_ROOT` | `/opt/software` | `/vol3/1000/software` |
| `EASYHOSTAGENT_LOG_LEVEL` | `info` | `info` |

FnOS 目前仅根据 `/etc/os-release` 的 `ID/NAME/ID_LIKE` 和 `/etc/fnos-release` 进行保守的识别；用户可使用 `EASYHOSTAGENT_SOFTWARE_ROOT` 明确覆盖。后续 T01-07 负责完善 FnOS 兼容与 UI 可配置路径，以及存在受管应用时禁止直接迁移目录的业务规则。

配置校验仅读取参数，不会创建目录、修改文件或运行 Docker。日志使用 JSON Lines，标准输出可由 systemd/journald 收集；**当前代码未声明监听地址，也不对外开放接口**。

## 任务路线

- **T01-01**：Go 工程骨架、配置、日志、构建与单元测试。
- **T01-02（完成）**：FnOS/Linux 安装脚本和 systemd unit（实机验收仍需目标机器）。
- **T01-03**：持久 Agent 身份、HTTPS API 和授权。
- **T01-04**：一次性配对码。
- **T02**：Docker 基础管理。
- **T03/T04**：中央 Deploy 部署包、应用管理任务。

所有业务规则见 [需求基线](docs/requirements-plan.md) 与 [任务计划](docs/task-plan.md)。

本项目不采用心跳，不由 Agent 克隆私有 Deploy 仓库，不实现自动回滚。失败保留现场和日志。
