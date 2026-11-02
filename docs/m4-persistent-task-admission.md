# M4-01：部署任务持久化准入（仅准备，不执行）

> 当前版本的 `POST /v1/task-preparations` **不会安装、更新或卸载任何应用**。本阶段仅提供有审计身份的部署意图登记、幂等请求、应用级资源预留、查看与取消；任务状态 `prepared` **绝不等于运行中或安装完成**。

## 1. 已实现的授权流程

```text
已配对 EasyNetTopo 通过 HTTPS/SPKI
        │
        ▼
auth.RequireController（Bearer Token + controller_id + agent_id）
        │
        ▼
读取当前唯一配对 Grant（controller_id / device_id / paired_at）
        │
        ▼
本地 trust.Check(bundle_sha256)
        ├─ 无批准、已撤销、缓存内容变动 → 拒绝创建
        └─ Agent / SHA-256 / Git 来源与 Manifest 绑定一致
        │
        ▼
进程间文件锁 → 扫描所有持久任务
        ├─ 同 request_id、相同 Grant 和参数 → 返回既有任务（200）
        ├─ 同 request_id 但身份或参数不同 → 冲突（409）
        ├─ 同一应用已有 prepared 预留 → 冲突（409）
        └─ 符合准入 → 原子写入任务元数据（201）
        │
        ▼
status=prepared, execution_enabled=false
        （没有任务工作器，不解压、不调用 Docker/Shell）
```

- 只有通过 `trust.Store.Check` 且仍由**当前有效控制中心 Grant**授权，才允许建立准备任务；本机 ZIP 可信批准不由远程任务请求创建。
- 任务记录绑定 Agent、Controller、Device、配对时间 `grant_at`、请求幂等 UUID、动作、不可变 ZIP SHA-256、来源、应用与固定 Git Commit。
- 同一 Agent 内按 `application_id` 互斥预留；**暂不声称已完成 Docker 端口、命名卷、真实进程与多应用跨资源冲突检测**。
- 任务元数据落盘至 `<EASYHOSTAGENT_DATA_DIR>/tasks/<task_uuid>.json`（目录 0700，文件 0600），文件锁与 fsync 提供进程间排他和崩溃恢复；损坏/符号链接或未知任务文件会使准入失败关闭，而非偷偷删除。
- 重启后 `prepared` 仍存在，**不会自动执行或重放**。取消任务释放本应用预留，保留原记录可供审计。
- 重新配对后旧 Token 和旧配对纪元无法读取/取消旧任务；为了避免误释放资源，旧 `prepared` 预留不会被新控制中心隐式清除，需后续可信本机任务恢复/转移流程。
- 每个任务请求由控制端提供客户端生成的 `request_id` UUID，发生响应丢失时重试原请求而非生成另一个 ID；Agent 全局拒绝同 ID 不同参数。

## 2. HTTPS API

本阶段使用独立的 `/v1/task-preparations` 命名空间，**不占用** OpenAPI 已规划的 `/v1/tasks`（真正异步执行任务）路径；后者仍没有实现也没有公开。

**POST /v1/task-preparations**（Content-Type: application/json，最大 4096 字节；要求 Agent 现有配对认证）

```json
{
  "request_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "action": "install",
  "bundle_sha256": "<64 位小写 SHA-256>"
}
```

`action` 接受 `install`、`update`、`uninstall`，但只记录意图，不执行。来源及应用名称从 Agent 本机批准的不可变 ZIP 身份推导，不能由调用者任意填入。首次创建返回 201，幂等重试 200，应用或幂等键冲突 409；未获本机批准 403，未配对/配对失效 401；响应包含 `task`、`idempotent_replay`、`execution_enabled:false`。

**GET /v1/task-preparations**：只返回当前 Controller + Device + 配对纪元的任务，含已取消记录。

**GET /v1/task-preparations/{task_id}**：按授权身份查询一项任务；其他控制中心的任务不可见（404）。

**DELETE /v1/task-preparations/{task_id}**：幂等取消当前控制中心的 `prepared` 任务。只修改任务记录并释放预留，**不等于停止容器或中断运行程序**（本阶段没有运行程序）。

所有路径要求 Agent HTTPS，并由统一的 `auth.RequireController` 校验 Bearer / X-EHA-Controller-ID / X-EHA-Agent-ID。未知动作、JSON 额外字段/多 JSON 文档、尺寸超限等均拒绝。

## 3. 后续安全研发门禁

1. **执行期再次授权**：必须在真实操作前检查当前 Controller Grant 与持久化任务纪元、`trust.Check`，且本机撤销与执行并发须有明确锁顺序和中断策略。
2. **资源冲突**：真实 Docker 项目名、端口、卷、已有容器与未知状态的保守冲突判定；资源独占、幂等、恢复与执行状态需要同一原子状态机。
3. **运行时**：受控工作目录、Compose 资源限定、脚本受控加载、配置/密钥初始化、数据保留、异常终止与失败恢复，不允许后台任务自动从 `prepared` 启动。
4. **运维治理**：当前最多 2048 条任务记录、不会自动压缩/删除；需设计本机安全清理及旧控制中心遗留预留恢复命令，避免长期死锁或人为删除文件。
5. **实机验收**：Linux / fnOS 的 Sub2API 安装、升级、卸载与真实健康检查，本阶段尚未执行。

## 4. 验收范围

Go Race/并发测试验证重复提交、同 ID 参数冲突、同应用同时申请、重启持久化、取消幂等、Grant 替换后隔离、任务文件被篡改/符号链接拒绝。真实 HTTPS 路由测试验证匿名拒绝、201/200/409、查询、取消和不提供任务启动操作。没有实际执行 Docker/Shell。
