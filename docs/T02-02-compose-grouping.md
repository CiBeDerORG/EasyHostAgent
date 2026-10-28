# T02-02 — Docker Compose 项目聚合

> 2026-10-28；代码分支 `CiBeDerORG/EasyHostAgent/main`。
> 进度：**代码和测试已提交；GitHub Actions #40657179844 本轮已触发，最终结果下次任务开始时检查**。

## 设计与实现

- 新增 `internal/dockerengine/group.go`，`Group(Inventory)` 基于 Docker 容器标签 `com.docker.compose.project` 聚合同一个 Compose 项目的全部容器。一个 Compose 项目在同一主机上只展示为一个资源。
- `com.docker.compose.service` 收集为去重且排序的服务列表；保留全部容器详情、状态、端口、挂载、镜像和网络信息。
- 无 Compose project 标签的容器都保持独立资源，稳定 ID `container:<docker-container-id>`；Compose 稳定 ID `compose:<project-name>`。不同主机必须由上层设备/Agent ID 隔离，禁止跨主机聚合。
- 无论容器是否运行，都参与聚合；`running` 表示所有容器运行，`stopped` 表示全部已停止或未启动，`partial` 表示混合运行状态，其它无法可靠推断的返回 `unknown`。
- 分组结果按稳定资源 ID、容器 ID 和 Compose service 名排序；Docker 返回顺序变化不影响结果。
- `Inventory.Complete=false` 时返回 `ErrIncomplete` 和 `complete=false`、**空的非权威聚合列表**，防止扫描失败被误判为外部删除。缺少容器 ID 或重复容器 ID 也拒绝生成完整快照。
- 增加 `Client.InspectGrouped(ctx)`，按需调用真实 Docker Engine 适配器，然后执行聚合；Docker 请求失败时同时保留 `ErrIncomplete` 和原始错误类型。没有心跳、后台监控、自动执行操作或公开未鉴权的外部 HTTP 接口。

## 测试

- [x] 代码包含 Sub2API Web + PostgreSQL 多容器 Compose 归并、同镜像独立容器不误归并、不同 Compose 项目隔离。
- [x] 代码包含停止容器、部分运行、完全停止、空项目、未知状态、乱序快照、重复 ID、缺失 ID、非完整快照验证。
- [x] 代码包含 Mock Docker Engine Unix socket → InspectGrouped → Compose 聚合端到端隔离测试，以及 Engine 离线不生成权威清单测试。
- [ ] GitHub Actions gofmt、vet、race 与 Linux amd64/arm64 CI：[Run #40657179844](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40657179844)，本轮不等待完成，下次任务开始前检查并修复。
- [ ] 真正 FnOS + Sub2API 容器现场检查，归 T02-07。

## 下一步

T02-03：基于已取得的稳定容器和 Compose 资源 ID，实现按需查看日志、启动、停止、重启；必须验证 Bearer 授权和资源归属，严禁任意 Shell 或任意路径参数。Compose 操作需保证操作范围是整个项目，不能只启动其中一个服务就声称已启动全部。
