# EasyHostAgent

EasyHostAgent 是 EasyTier 的 Go 原生主机执行代理。该仓库目前处于 **T01-01 工程基础阶段**，首期目标为 Linux / FnOS 上的 Docker / Docker Compose 管理。

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
- **T01-02**：FnOS/Linux 安装脚本和 systemd unit。
- **T01-03**：持久 Agent 身份、HTTPS API 和授权。
- **T01-04**：一次性配对码。
- **T02**：Docker 基础管理。
- **T03/T04**：中央 Deploy 部署包、应用管理任务。

所有业务规则见 [需求基线](docs/requirements-plan.md) 与 [任务计划](docs/task-plan.md)。

本项目不采用心跳，不由 Agent 克隆私有 Deploy 仓库，不实现自动回滚。失败保留现场和日志。
