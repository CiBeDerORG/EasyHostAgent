# T04-02 — 多任务并发与取消调度

> 仓库：`CiBeDerORG/EasyHostAgent`；分支：`feat/server-app-lifecycle`。

## 本轮实现

- 新增独立的 `taskrunner.Scheduler`，与 SQLite `taskstore` 解耦；后续 T04-03 通过 `Executor` 接口接入真实 Deploy 执行器。
- 不同应用任务可并行，默认并发数为 4，可由调用方配置，硬上限为 64。
- 同一应用所有操作共用 `application:<application_id>` 互斥锁；Docker 资源按资源 ID 互斥；主机级任务共用主机锁。
- 支持由受信任的 manifest/执行计划传入共享配置或资源锁；锁按固定顺序获取，避免多资源任务因锁顺序不同而死锁。
- 取消先进入 `cancelling`，再通过 context cause 通知执行器在安全检查点停止；完成后标记 `cancelled`，保留日志与已产生的现场，不自动回滚。
- Agent 关闭时停止接收新任务，通知执行中任务中断并等待到调用方截止时间；超时不假装已停止。
- 同任务 ID 重复提交不启动第二个 worker；任务重启恢复仍遵循 T04-01 的 `interrupted` 规则，不自动重放。

## 当前边界

该提交提供并发调度核心与取消 API（Go 方法），尚未连接真实脚本/Compose 执行器，也没有对外开放 HTTP 提交/取消路由。T04-03 将注入真实 executor 并开放提交、取消入口。

## 验收覆盖

- 不同应用任务能同时进入 executor。
- 同一应用任务严格串行。
- 不同应用使用同一共享资源锁时严格串行。
- 取消能够通知正在运行的 executor，最终状态为 `cancelled`，日志仍可查询。
- 每个测试均使用真实 SQLite 存储和可控 fake executor。
