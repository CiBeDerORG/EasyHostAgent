# M4-02：Docker 资源预检与旧配对任务的本机可信恢复

> **本阶段仍只有准备任务，没有执行任务。** `POST /v1/tasks` 继续返回 404；所有预检只是对特定时刻的资源/授权状态作出只读判断，绝不等于可以执行 ZIP 内脚本。

## 准入链

`POST /v1/task-preparations` 调用当前唯一 Controller Grant 校验，以及 `trust.Check(bundle_sha256)` 对本机批准与 ZIP 字节校验；生产环境使用 `tasks.NewGuarded` 并要求真实 Docker Unix Socket `InspectGrouped` 返回**完整快照**。程序从不可变 ZIP 的 `<app>/app.yaml` 本地字节提取 `runtime.compose_project`、每个声明端口的默认 Host IP/端口/协议、命名卷和 data 数据目录，持久化在准备记录中。

- 拒绝重复键、YAML Alias、多个 YAML 文档、未固定的端口/监听地址、非法 IPv4、危险路径、异常项目名与声明冲突；**目前不支持在 Task 请求里动态覆盖端口**。
- 与其他持久化的 `prepared` 资源声明冲突时返回 409。单应用独占仍然有效，且同名 Compose 项目、覆盖的 TCP/UDP Host IP/端口、共享命名卷、重叠 data 路径均视为冲突。
- 同时查看 Docker **全部状态的容器**。对已占用宿主机端口、已挂载命名卷、占用数据目录的其他资源，拒绝建立预留；新安装遇到同名 Compose 项目也拒绝。更新/卸载不会在此阶段确认项目所有权，执行器必须另行核验。
- Docker 不可用或扫描不完整时返回 503，**绝不把空清单视为可用**。未来执行器不得复用先前获得的成功预检结果；必须在获得资源锁后重新检查、将状态原子转成运行中，再执行。
- `tasks.Store.Preflight` 为只读内部 Go 接口：在任务文件锁内重新核验当前 Grant 的 controller/device/配对纪元、本机 ZIP 批准、固定 Manifest 资源指纹、所有其他未取消预留和最新完整 Docker 清单。**不暴露为 HTTP 运行许可**。

## 主机本地旧任务恢复

控制中心重新配对后，旧 Grant 下的 `prepared` 原先会长期占用应用资源。现在提供仅本机可信终端的命令：

```bash
easyhostagent -recover-task "<task_uuid>" \
  -expected-controller "<previous_controller_uuid>" \
  -expected-app "sub2api"
```

CLI 验证 Agent 身份与任务记录、用户显式提供的任务/控制中心/应用三元组，再由配对存储确认任务的**原配对纪元已经失效**。仅这种情况下将 `prepared` 原子变更为 `superseded`，保留原记录/时间戳并释放应用预留。若原配对仍有效，必须通过授权的 HTTP DELETE 准备任务接口取消；不能绕过或自动删除。此命令**不删除容器、数据、卷、目录，也不停止应用进程**，仅解除准备阶段的记录。

## 剩余高权限验收

**尚未解决的资源安全**：Docker 报告的端口不覆盖任意宿主机非 Docker 进程、内核临时端口、Docker 没有报告的 stopped 容器宿主机绑定；实际工作目录及符号链接、Compose 项目所有权和历史数据归属、重叠 named volume driver 语义、IPv6 双栈策略需要执行器做更严格的检查。

**撤销并发**：`Preflight` 只是只读结果，它返回后本机批准或 Controller Grant 可能被撤销。真正的执行器必须设计单一资源锁及任务状态事务，使撤销、授权检查和启动动作之间无可绕过窗口；目前没有安装/更新/卸载执行器，也没有自动重试。

**真实环境**：未在 fnOS/Linux 真实 NAS 启动 Docker、Shell 或 Sub2API；只有合成 ZIP、模拟 Docker 完整/缺失/冲突快照的 Go CI 测试。
