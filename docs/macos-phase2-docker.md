# 第二阶段：macOS Docker Desktop 管理适配（首批）

## 本批已实现

- macOS 原生 EasyHostAgent 默认使用当前用户 `~/.docker/run/docker.sock`，不依赖 `/var/run/docker.sock` 的可选软链接。
- 复用 Linux/fnOS 已有的 Docker Engine API 客户端与 HTTPS、配对授权；开放 `GET /v1/docker/inventory`、`GET /v1/docker/resources/{resource_id}/logs`。
- 容器与 Compose 项目仍通过 Docker Compose 标签自动分组；仅在 **完整** Engine 查询成功时返回有效发现列表。
- `GET /v1/host` 中的 Docker 状态改为真实读取 Docker Desktop Engine 版本；Compose 版本使用登录用户 CLI，兼容 LaunchAgent 的精简 PATH。
- 即使 Docker Desktop 未安装或未启动，Agent 仍能提供 HTTPS 身份、主机指标与配对。Docker 探测返回不可用，不返回空但“完整”的资源清单。
- macOS 应用执行器、Bundle 写入与 Linux 脚本继续关闭，`app_manifest_versions: []` 不变。

## 安全边界

Agent 只连接本地 Unix Socket，不使用来自环境的 `DOCKER_HOST`、不暴露远程 Docker TCP 端口。Docker Socket 按 Docker Desktop 当前登录用户访问；不要求 sudo 或系统级 root 守护进程。容器发现与日志受已配对控制端的 Bearer 授权保护。容器 start/stop/restart 的现有 `dockerengine.Act` 内部能力尚未接入持久任务端点，不能绕过审计直接开放同步 HTTP 写操作。

## 未完成

- 持久任务框架中的容器/Compose start、stop、restart 适配，以及安全的删除操作；
- 运行时 CPU/内存容器指标、Docker Desktop 启停状态的控制端展示；
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
