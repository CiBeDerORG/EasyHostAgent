# 第二阶段：macOS Docker Desktop 管理适配（首批）

## 本批已实现

- macOS 原生 EasyHostAgent 默认使用当前用户 `~/.docker/run/docker.sock`，不依赖 `/var/run/docker.sock` 的可选软链接。
- 复用 Linux/fnOS 已有的 Docker Engine API 客户端与 HTTPS、配对授权；开放 `GET /v1/docker/inventory`、`GET /v1/docker/resources/{resource_id}/logs`。
- 容器与 Compose 项目仍通过 Docker Compose 标签自动分组；仅在 **完整** Engine 查询成功时返回有效发现列表。
- 新增 `GET /v1/docker/resources/{resource_id}/stats`：按需读取容器 CPU 百分比、原始内存使用/限制；仅运行中容器采样，Compose 资源合并返回成员数据，失败时不伪造完整结果。
- `GET /v1/host` 中的 Docker 状态改为真实读取 Docker Desktop Engine 版本；Compose 版本使用登录用户 CLI，兼容 LaunchAgent 的精简 PATH。
- 即使 Docker Desktop 未安装或未启动，Agent 仍能提供 HTTPS 身份、主机指标与配对。Docker 探测返回不可用，不返回空但“完整”的资源清单。
- macOS 应用执行器、Bundle 写入与 Linux 脚本继续关闭，`app_manifest_versions: []` 不变。

## 安全边界

Agent 只连接本地 Unix Socket，不使用来自环境的 `DOCKER_HOST`、不暴露远程 Docker TCP 端口。Docker Socket 按 Docker Desktop 当前登录用户访问；不要求 sudo 或系统级 root 守护进程。容器发现与日志受已配对控制端的 Bearer 授权保护。容器 start/stop/restart 的现有 `dockerengine.Act` 内部能力尚未接入持久任务端点，不能绕过审计直接开放同步 HTTP 写操作。

## 未完成

- 持久任务框架中的容器/Compose start、stop、restart 适配，以及安全的删除操作；
- 控制端 CPU/内存统计展示与 Docker Desktop 启停状态呈现；
- Compose 配置文件部署、重建、拉取与服务变更（现有 Group 仅对已创建容器分组）；
- EasyNetTopo 前端交互及 Mac mini 实机 Docker Desktop、LaunchAgent 登录会话验收。

## Mac mini 实机验收

先在当前 macOS 登录用户会话中启动 Docker Desktop：

```bash
docker context ls
docker info
docker compose version
ls -l "$HOME/.docker/run/docker.sock"
```

通过 deploy 第一阶段安装/更新入口部署本分支编译出的 Agent 二进制（不得沿用第一阶段 Release 并声称已有 Docker API）。验证 `GET /v1/host` 的 `docker.available`，然后以已配对控制端调用 `GET /v1/docker/inventory` 并检查 `complete=true`。无需人工创建 /var/run/docker.sock，且 Docker Desktop 未运行时不应误报容器删除。

参考：https://docs.docker.com/desktop/setup/install/mac-permission-requirements/

## macOS Docker 操作任务（第二批）

Agent 现在为 macOS Docker 资源提供经过配对授权的 `POST /v1/tasks`、`GET /v1/tasks`、`GET /v1/tasks/{task_id}` 和 `POST /v1/tasks/{task_id}/cancel`。
只允许 `kind=docker_resource` 与 `action=start/stop/restart`；`resource_id` 必须是 `container:<id>` 或 `compose:<project>`，重新通过完整清单解析后才操作；不能注入任意命令。
任务在开始执行前使用 0600 文件安全持久化，使用客户端 UUID 防止重试重复执行。同资源操作互斥，重启后未完成任务标记为 `interrupted` 且不会自动重试；取消可能留下已部分执行的容器，需重新查询状态。
Compose 管理目前仅覆盖已存在的容器启动、停止、重启；不执行 Compose up/down、重建、删除数据或更新镜像。
