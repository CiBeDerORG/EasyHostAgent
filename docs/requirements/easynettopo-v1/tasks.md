# T001 · EasyHostAgent 对接 EasyNetTopo V1（汇总任务）

> 2026-11-02 整编，关联 [R001 需求](requirement.md)。状态综合当前 `main` 已存在的 Go 源码与 M3/M4 文档保守标注；尚未进行本轮 FnOS/Linux 集成验收。旧 [T00～T06 计划](../../task-plan.md) 是以 EasyTier 为控制端的历史阶段计划，保留原编号作证据/迁移线索，不作为本任务的实时状态源。

**T001 汇总状态：进行中；已验证完成 0 / 有效叶子 8。** “待验证”表示对应源码/单测存在但未核对当前业务完整验收；完成数是**新控制端交付 DoD**口径，不代表既有 Go 单测均未通过。

| 任务编号与名称 | 父级 | 关联需求 | 依赖 | 状态 | 现有证据 / 仍需核查 |
| --- | --- | --- | --- | --- | --- |
| T001 · V1 受管 Agent（汇总） | — | R001 | — | 进行中 | 0/8；端到端闭环未验收 |
| T001.01 · 主机身份、HTTPS 与重新配对 | T001 | R001.01、R001.02 | 无 | 待验证 | `internal/identity/`、`internal/pairing/`、`internal/httpapi/pairing_test.go`，真实指纹验证待核实 |
| T001.02 · 主机信息和 Docker/Compose inventory | T001 | R001.03 | T001.01 | 待验证 | `internal/dockerengine/`、`internal/httpapi/docker_routes.go`，FnOS/不完整快照待复核 |
| T001.03 · 不可变 bundle、摘要与解包安全 | T001 | R001.04 | T001.01 | 待验证 | `internal/bundles/`、`internal/httpapi/bundle_routes.go`、`docs/m3-immutable-bundles.md` |
| T001.04 · 主机本地包信任批准与权限核验 | T001 | R001.04 | T001.03 | 待验证 | `internal/trust/`、`docs/m3-local-bundle-approval.md`，与真实脚本执行授权贯通待复核 |
| T001.05 · 持久任务接收、幂等与状态查询 | T001 | R001.05 | T001.01 | 待验证 | `internal/tasks/store.go`、`internal/httpapi/task_routes.go`、`docs/m4-persistent-task-admission.md` |
| T001.06 · 资源预检、归属隔离与恢复策略 | T001 | R001.06 | T001.02、T001.05 | 待验证 | `internal/tasks/guard.go`、`recovery.go`、`docs/m4-resource-preflight-recovery.md` |
| T001.07 · Sub2API 受控执行、健康和脱敏日志 | T001 | R001.07 | T001.03～T001.06 | 阻塞 | 需要真正执行器及信任策略联合验收；不可只凭包已上传就宣布安装完成 |
| T001.08 · 安装更新卸载和 FnOS/Linux 实测 | T001 | R001.08 | T001.07 | 待办 | 验证三服务健康、数据保留、失联恢复、失败保留现场、取消与重复请求 |

## 历史映射（仅追溯，不自动继承 DONE）

- 原 `T00-02` OpenAPI、`T01-03/04` 身份/配对 → T001.01；原 `T02-01/02/03` Docker → T001.02。
- 原 `T03-07/08` 包/下发设计 → T001.03/04；新 M3 不可变包及本地授权以对应 `docs/m3-*.md` 为优先现实证据。
- 原 `T04-01/02` 任务状态机、`T04-05` 预检 → T001.05/06；新 M4 准入/资源防护以对应 `docs/m4-*.md` 及现行 Go 测试为准。
- 原 `T04-03/04/06/08` 执行/配置/安装闭环、`T05` 生命周期和 `T06` 实测 → T001.07/08；不得继承旧 EasyTier 作为控制端、服务/公网自动关联等已被新需求移除的目标。

## 状态维护

每个 T 编号跨阶段不变，同一父级新增子任务继续递增且不复用历史编号。开发/阻塞/验证/返工写入本文件对应叶子并重新计算父级，不另建多份 Task 状态；关联 R 需求的交付状态与人工验收分开记。涉及跨仓集成时引用 EasyNetTopo、Deploy 对应 R/T 文件，不复制其动态状态。
