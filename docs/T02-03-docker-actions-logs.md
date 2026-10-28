# T02-03 — Docker 基础操作与按需日志

> 2026-10-28；仓库 `CiBeDerORG/EasyHostAgent`；分支 `main`。
> 状态：**代码及自动化用例已提交，GitHub Actions 最终验证待本轮后核对；FnOS 真机仍待验收**。

## 交付范围

1. `internal/dockerengine/actions.go`：新增 `Client.Act(ctx, resourceID, action)`，仅允许 `start`、`stop`、`restart`。
2. 每次操作都根据**完整的当前 Engine 清单**解析之前发现的 `container:<id>` 或 `compose:<project>` 稳定资源标识，不允许用户传容器路径、Shell 字符串或任意 Docker URL。
3. Compose 操作遍历项目的**所有现有容器（含停止中的容器）**。调用 Docker Engine 对应容器的受限 POST 端点；失败立即终止、记录已完成 ID，并保留现场；不会自动回滚或隐藏部分成功。全部操作结束后重读清单，对原容器集合和目标状态进行验证；未全部符合预期则 `ErrPartialOperation`。
4. 对于 Compose 的本轮动作是 **对已有容器进行 start/stop/restart**，不是 `docker compose up`，不会创建丢失的容器、pull 镜像、重建服务、恢复环境配置或自动处理服务依赖顺序；这些能力不得冒充已经完成。
5. `internal/dockerengine/logs.go`：使用 `GET /containers/{id}/logs`，固定 `follow=0`，`tail` 限制 1～500，最多返回 256 KiB；兼容 Docker stdout/stderr 多路帧和 TTY 原始日志，Compose 资源按成员组织日志。
6. `GET /v1/docker/inventory`、`GET /v1/docker/resources/{resource_id}/logs`：在 Agent HTTPS 服务上注册，并复用已配对控制端 Bearer、Agent UUID、Controller UUID 的授权校验。完整读取失败时只返回非权威的 `complete=false` 快照，不触发删除。
7. `GET /v1/docker/inventory` 返回既有 OpenAPI 中定义的 `resource_id/kind/name/state/containers/ports/managed` 字段。未接管的 Docker 资源标记 `managed=false`。
8. **没有新建同步执行 POST 动作路由**：协议规定启动、停止、重启必须通过 `POST /v1/tasks`，由 T04-01/02 的持久化、幂等和资源锁执行器调用 `Client.Act`，不能绕过任务审计/互斥直接对外执行。仅代码实现本地操作，不代表前端已具备操作按钮。

## 自动化验收

- [x] 新增 Go 测试覆盖真实 Unix Socket 模拟 Engine、完整 Compose 资源动作、操作后的真实状态确认、单成员失败后的部分成功记录。
- [x] 测试覆盖不允许的动作、资源标识不存在、日志截断、Docker stream 解复用与 TTY 文本。
- [x] TLS 端到端测试覆盖先配对再读取 inventory 和 logs、其他控制端/无 Bearer 被拒、非法日志参数与不存在的同步操作路由。
- [ ] Go gofmt/vet/race 与 AMD64/ARM64 构建最新结果：本轮不等待；下次新任务开始前按全局规则检查并优先修复失败。
- [ ] FnOS Docker Engine 权限、Compose 多服务依赖顺序与真实日志需 T02-07 现场验收。
- [ ] `POST /v1/tasks`、同应用资源互斥和异步日志持久化属于 M4，不能将 T02-03 误标为完整用户端闭环。

## 下一项

T02-04 应用发现、Deploy 模板匹配与用户确认接管；应复用现有 Compose/独立容器身份，不得未经用户确认提升外部应用为受管应用。
