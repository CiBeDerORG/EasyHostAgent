# T02-01 — Docker Engine 本地适配器

> 日期：2026-10-28；目标分支 `CiBeDerORG/EasyHostAgent/main`。
> 状态：**代码及 CI 已完成**；真实 FnOS Docker Engine 待 T02-07 联调。

## 实现内容
- `internal/dockerengine` 新增按需调用的 `Client.Inspect(ctx)`。仅连接本机 Unix socket `/var/run/docker.sock`，不读取 `DOCKER_HOST`、不接受 Docker TCP 地址、禁用 HTTP 代理与重定向；支持测试时指定临时 Unix socket。
- 先访问 `GET /version`，根据服务端 `ApiVersion`/`MinAPIVersion` 选择支持的 API 版本。至少要求 API v1.24，优先 v1.41，较新服务端最小版本更高时采用其最小版本。
- 使用 `GET /v{api}/containers/json?all=1` 同时获取运行中和已停止容器；保留 ID、名称、镜像、命令、创建时间、状态、标签、端口、挂载、网络。
- 使用 `GET /v{api}/images/json?all=1` 获取全部镜像的标识、标签、digest、大小。
- 两个列表都成功才设置 `complete=true`；失败时 `complete=false`、保留已取得的部分资源与脱敏错误，供上层禁止把“查询失败”当作“应用不存在”。
- 连接、请求、JSON 响应有超时和体积上限。401/403 与 Docker 不可达、API 不支持使用不同 Go 错误类型。
- 本次仅为 M2 适配器：**没有增加对外无授权 HTTP 接口，也没有定时 Docker 扫描**。T02-02 使用此快照做 Compose 聚合，T02-03 才添加经 Bearer 授权的操作/日志。
- 开发实现采用 **Docker Engine 官方 HTTP API 的 Go 原生 Unix 客户端**，没有引入第三方 Docker Go SDK 依赖。与原任务描述中的 “Go SDK” 不完全一致；需在后续技术评审确认是否迁移至官方 Moby SDK。选择原因：当前 Agent 固定 Go 1.23，而 Moby 当前独立 client 模块 v0.6.2 要求 Go 1.24，直接引入会改变既有工具链/依赖基线。

## 测试清单
- [x] 源码中包含隔离 Unix socket 假 Engine 测试：两个不同状态的容器、镜像、标签、端口、挂载、网络。
- [x] 源码中包含版本降级/最小版本协商、无 Docker、403 权限错误、部分成功不触发误删除标记的测试。
- [x] Go 格式检查、vet、race、amd64/arm64 CI 通过：[Actions #40656579274](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40656579274)。
- [ ] FnOS 真 Docker socket 权限与大量容器性能测试。

## 下一步
T02-02 以 `com.docker.compose.project` 为首选身份对多容器项目分组；不得将无项目标签的独立容器误聚合，不得依据容器状态判定外部删除。
