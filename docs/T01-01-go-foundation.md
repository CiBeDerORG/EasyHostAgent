# T01-01 — EasyHostAgent Go 原生工程基础

> 实施日期：2026-10-27
> 状态：**DONE（代码、单元测试、GitHub Actions Linux amd64/arm64 双架构构建全部通过）**
> 源码仓库：`CiBeDerORG/EasyHostAgent` `main`
> 主体提交：[10fbe952](https://github.com/CiBeDerORG/EasyHostAgent/commit/10fbe952ffe8bd1f06ca533c01cacc09f865611e)
> 格式修复：[c0d9cf06](https://github.com/CiBeDerORG/EasyHostAgent/commit/c0d9cf06c95926d33077e24b8a717f609004a636)
> 最终 CI 验证提交：[2537d9ba](https://github.com/CiBeDerORG/EasyHostAgent/commit/2537d9ba71feac49368500a8af4c98f3e6890386)
> CI：[Go Agent Foundation #40423379295](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40423379295) — `success`。

## 1. 本任务完成的真实代码

| 文件 | 职责 |
|---|---|
| `go.mod` | Go module `github.com/CiBeDerORG/EasyHostAgent`，Go 1.23，零第三方 Go 依赖 |
| `cmd/easyhostagent/main.go` | CLI 入口，`-version`、`-check-config`，监听 SIGINT/SIGTERM 优雅退出 |
| `cmd/easyhostagent/main_test.go` | CLI 参数、版本、配置正常与异常校验 |
| `internal/buildinfo/buildinfo.go` | 版本、Commit、构建信息，允许使用 `-ldflags` 注入 |
| `internal/config/config.go` | 环境配置、保守的 FnOS 系统标志识别、软件根目录与状态目录、绝对路径及等级校验 |
| `internal/config/config_test.go` | Linux/FnOS 默认路径、手动覆盖、路径和日志等级错误测试 |
| `internal/logging/logging.go` | 标准库 `log/slog` JSON Lines，debug/info/warn/error |
| `internal/logging/logging_test.go` | 级别解析、日志 JSON 与过滤测试 |
| `internal/app/app.go` | 非网络监听的最小阻塞生命周期和停止日志 |
| `internal/app/app_test.go` | Context 取消、生命周期日志 |
| `Makefile` | 本地测试、gofmt 校验、vet、构建及交叉编译快捷入口 |
| `.github/workflows/go-build.yml` | gofmt、`go vet`、`go test -race`、Linux AMD64/ARM64 静态构建、7 天 CI artifact |
| `README.md` | 工程结构、手工运行方法、配置约定、后续实施边界 |
| `.gitignore` | 忽略二进制构建产物及本地敏感文件 |

**当前只是一套可运行的 Daemon 骨架，完全没有打开 HTTP/HTTPS 端口，没有配对、Docker 操作、后台采集/心跳或 Deploy 安装能力。**

## 2. 配置规范

| 环境变量 | Linux 默认 | FnOS 默认 |
|---|---|---|
| `EASYHOSTAGENT_DATA_DIR` | `/var/lib/easyhostagent` | `/var/lib/easyhostagent` |
| `EASYHOSTAGENT_SOFTWARE_ROOT` | `/opt/software` | `/vol3/1000/software` |
| `EASYHOSTAGENT_LOG_LEVEL` | `info` | `info` |

`-check-config` 不创建目录、不修改 Docker、不触发 HTTP 请求。

FnOS 的探测基于 `/etc/os-release` 的 ID / NAME / ID_LIKE 和 `/etc/fnos-release`。设备兼容性、安装后的持久化和路径迁移保护由 T01-02/T01-07 接续；当前无法保证所有 FnOS 固件都有一致的标志文件。

## 3. 已执行的验证

GitHub Actions Run `40423379295` / commit `2537d9ba`：

| Job | 检查 | 结果 |
|---|---|---|
| Format, vet, test | gofmt | ✅ success |
| Format, vet, test | go vet | ✅ success |
| Format, vet, test | go test -race ./... | ✅ success |
| Linux amd64 | CGO_ENABLED=0, GOOS=linux, GOARCH=amd64 | ✅ success |
| Linux arm64 | CGO_ENABLED=0, GOOS=linux, GOARCH=arm64 | ✅ success |
| Artifact | 两种基础二进制归档 | ✅ success |

CI 构建产物：
- `easyhostagent-foundation-linux-amd64`
- `easyhostagent-foundation-linux-arm64`

这两个只是 **T01-01 基础程序测试产物**，并非正式安装包或可用的 Agent 服务。不应部署到生产 FnOS 期待可管理 Docker。

首次运行未通过 gofmt，定位为 `internal/config/config.go` 常量对齐；已按 gofmt 格式修改并恢复严格校验，最终整个 Go 工作流成功。

## 4. 需求边界和约束

- 无周期心跳、无需定时轮询。
- Agent 不拉取私有 Deploy Git 仓库。
- 不引入自动回滚。
- 不存储或输出真实凭证、配对码。
- 不引入 Redis/MQTT/消息队列等依赖。
- 仍需 T01-02 定义 Linux/FnOS 安装目录、systemd 单元、权限和持久 Agent 数据的升级保留。
- T01-03 才会实现真正的 Agent 身份及认证的 HTTPS API。
- M2 才会实现 Docker SDK/Compose。

## 5. 继续实施

**下一任务：T01-02 — FnOS/Linux systemd 安装器。**

具体包括自动检测 `linux/amd64` / `linux/arm64`，编写可复制的下载/本地安装命令、systemd unit、数据目录和权限，确保卸载或重新安装不误删应用配置。完成后应验证 Linux systemd 行为，FnOS 实机验证需具备目标主机访问。
