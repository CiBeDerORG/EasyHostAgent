# T00-01 — EasyTier 现有 Agent 管理与资产模块复用审查

> 审查日期：2026-10-27
> 状态：**DONE（静态代码审查和方案交接完成）**
> 审查范围：EasyTier `feat/automation-center`、与其他公开分支的历史关系、EasyHostAgent 独立仓库、Deploy `main` 的现有目录和脚本。
> **本任务只审查不改业务代码、未构建或运行真实 FnOS。** 目标集成分支仍以开发时最新分支为准，不能将本审查等同于 merge/CI 验收。
> 对应计划：[`task-plan.md`](./task-plan.md) T00-01；对应需求：[`requirements-plan.md`](./requirements-plan.md)。

## 一、结论（技术决策）

1. **优先在 EasyTier `feat/automation-center` 已有系统上增量改造**，不要从 `main` 重新开发 Agent 管理、服务和用户权限；实际动工前先核对该分支是否有新的上游提交。
2. **复用 Agent 的现有 HTTPS 客户端/凭证密文/身份校验/任务 ID 幂等/快照/日志游标**；但独立 Agent 列表、手输 endpoint+Token、Agent 提供 app catalog 的业务模式必须迁移为“服务器设备绑定 + 一次性配对 + EasyTier 中央商店”。
3. **服务创建、服务修改、删除与专属公网映射只能经 EasyTier 资产服务逻辑处理**；Agent 不操作 EasyTier 数据库，也不直接编辑 EasyTier 网络转发配置。
4. **不要保留当前每台 Agent 一把锁的业务限制作为最终并发策略**：实际执行应支持不同应用并行、同应用互斥；按需查询状态不应被无关长任务阻塞。
5. 删除设备但保留服务与现有节点外键/删除保护存在实现冲突；必须在后续数据模型和设备删除流程里明确技术处理，不能绕过外键或默默删服务。

## 二、审查基线与 Git 历史

| 目标 | 结果 | 影响 |
|---|---|---|
| EasyTier `main` | GitHub `main` 头部 `e307dadb4812`（取样时）；基础接口仅有 auth、network、central_network 等，无 `deploy_agent.rs` | 不能以 `main` 单独作为 Agent 业务代码基线 |
| EasyTier `feat/automation-center` | 包含 `deploy_agent.rs`、`restful/deploy_agent.rs`、`DeployAgents.vue`、`deploy-api.ts`、迁移、资产与服务公开接口 | **首选集成候选分支** |
| `main...feat/automation-center` | GitHub 比较：feature ahead 399、behind 0（比较结果文件清单截断至 300，不把文件清单当完整范围） | 当前 feature 继承了 main 的基础，但包含大量业务变化 |
| `feat/node-attributes-main...feat/automation-center` | ahead 367、behind 0 | 属性/资产功能包含于 feature 历史 |
| `feat/docker-development-deploy...feat/automation-center` | diverged：feature ahead 399、behind 46 | **并非全量包含该分支改动**；后续合并需要专门审查，勿强行自动合并 |
| `feat/ecosystem-web-ui...feat/automation-center` | diverged：feature ahead 399、behind 45 | 同上 |
| EasyHostAgent `main` | 独立仓库已有需求/任务文档，无 Go Agent 业务实现 | M1 必须新建 Go 服务 |
| Deploy `main` | 有 `deploy.sh`、各应用 `deploy/install.sh`、`update.sh`、Compose、公共 env/secret 和 `lib/` | M3 以现有脚本为真实基础，不重新设计第二个部署仓库 |

**注意**：ahead/behind 是本次 GitHub 对比快照，不代表发布分支或可构建性；GitHub 比较结果的统计并非完整文件审计。

## 三、复用清单（按真实代码）

| 模块/路径（EasyTier `feat/automation-center`） | 已有实现 | 处理意见 | 后续任务 |
|---|---|---|---|
| `easytier-web/src/deploy_agent.rs` | 独立 Agent 记录、按需 `reqwest`、凭证 AES-GCM 密文、校验 `X-Deploy-Agent-ID`、不自动重试、任务提交/续查询/日志游标 | **保留底层机制，重构数据关系和 API 调用层** | T00-02、T01-06、T04-01 |
| `easytier-web/src/restful/deploy_agent.rs` | `/api/v1/deploy/agents`、`/tasks`、probe/discover/catalog 等 REST 路由 | **复用路由框架与认证中间件；调整资源语义**，不要盲目破坏已有客户端 | T00-02、T01-06 |
| `easytier-web/src/migrator/m20261024_000011_deploy_agents.rs` | `deploy_agents`、`deploy_tasks`、按 user 范围记录/查询 | **增量迁移数据表**、加现有设备关联/唯一约束，不能再创建不相干的第二套管理表 | T00-04 |
| `easytier-web/frontend/src/components/DeployAgents.vue`、`modules/deploy-api.ts` | Agent 列表、手动操作、应用参数表单、任务日志面板 | **复用组件和 API 客户端框架，重构入口到“服务器设备”与商店** | T01-05、T03-05、T03-06 |
| `easytier-web/src/db/assets/types.rs` + 语义类型迁移 `m20261025_000024_device_type_semantic_kinds.rs` | `semantic_kinds` JSON 多选、类型管理 | **直接复用**，识别拥有 server 类型的资产设备 | T01-05 |
| `easytier-web/src/db/assets/devices.rs` | 资产设备、machine_id/owner 与拓扑网络关联 | **复用设备资产主键**；须区分 `devices.machine_id` 网络节点与 `asset_devices.id` 设备资产 | T00-04、T01-05 |
| `easytier-web/src/restful/assets/mod.rs` | 现有节点/设备/服务 CRUD、admin/owner 认证、资产路由 | **直接通过业务服务层调用**，避免 Agent 模块独立 SQL 绕过校验 | T04-07、T05-05 |
| `easytier-web/src/db/assets/services.rs` | `create_service`、`update_service`、`delete_service`、协议端口唯一性与事务 | **保留并封装内部应用管理适配器**，不要前端发两个独立请求假装原子完成 | T04-07、T05-05、T05-06 |
| `easytier-web/src/db/assets/publications.rs` | 已有公网映射所有权、冲突保护、`remove_service` 精确删关联规则 | **直接复用**，仅删除当前服务真正拥有的映射 | T05-05、T05-06 |
| `easytier-web/src/restful/assets/publication_apply.rs` | 网络配置 runtime apply/snapshot/retry 管道 | **直接复用运行时映射同步**，不可仅写 DB 视为已生效 | T04-07、T05-05、T06-05 |

## 四、现有协议与最终产品要求的差距

### G-01 主机归属（必须在 M1 前解决）

- 当前 `NewAgent` 是 `{alias,endpoint,ca_pem,token}`，手工登记一个独立 `deploy_agents` 连接 ID；SQL 用 `user_id` 限定访问，没有强制设备关联。
- 新规则：只有资产设备有 `server` 语义类型，才从现有 EasyTier 设备入口绑定 Agent；使用已知设备地址自动探测、一次性配对码，之后用 Agent 稳定 ID 防误连；admin 管全部，普通用户只管自己的。
- **方案**：给现有 Agent 连接关系增加 `owner_user_id`、`machine_id`、`asset_device_id`（明确外键/唯一索引策略），把当前手动添加作为兼容旧记录的单独迁移路径；不可将独立 `deploy_agents.id` 当作 EasyTier `machine_id`。

### G-02 目录源头冲突（必须在 M3 前解决）

- 当前 `refresh("catalog")` 向 Agent 发送 `GET /v1/catalog`，并把远端目录写入 `deploy_agents.catalog_json`。
- 新规则：**EasyTier 中心手动刷新 Deploy git 工作副本**，形成全站商店；Agent 不访问私有 Deploy 仓库，也不承担商店源头。
- **方案**：将商店读写从 `deploy_agents` 解耦，中心缓存 repository revision/manifest；Agent 仅处理已认证部署包及已安装版本描述。旧 Agent catalog 接口不要当成未来中央商店。

### G-03 执行任务层（M4）

- 当前 `deploy_tasks` 记录任务请求密文、`outcome_unknown` 初始状态及本地日志缓存，通过 `task_id` 幂等提交，提供查询 Agent 的日志游标。
- **保留**：这些恰好适合按需 HTTP 和未知结果补查。
- **改造**：本地 Agent 必须有自己的任务 SQLite、子进程管控、重启后中断标失败、不同应用并行/同应用互斥。现有 Rust `operation(user,id)` 对每台 Agent 的手动调用采用 `try_lock_owned()`，不能在应用层变成整机长时间串行锁。
- 无自动回滚：失败只保存错误和日志，既有容器及数据保持现场。

### G-04 Agent HTTP 安全（M0 契约明确）

- 当前 `validate_endpoint` 仅校验 HTTPS origin、无 URL 凭据/路径/查询；请求禁用代理、禁重定向、禁自动 retry，并验证响应 Agent ID。
- 需要补齐设备地址与授权的绑定、证书轮换策略、配对挑战过期/单次使用、固定连接超时与总体超时、限制响应大小/部署包上传大小、防任意地址 SSRF、仅允许白名单动作。
- **不得直接复用 `DeployToken` 作为“用户填任意地址即可执行”的最终配对流程。**

### G-05 设备删除与服务保留的**实际冲突**（需要架构专项）

- 需求：删除 EasyTier 设备时解除 Agent 授权，主机 Docker 与 EasyTier 服务保留。
- 现有 `CentralNetworkService::delete_device` 在节点有资产时主动拒绝，报 `node_has_assets`；资产 `asset_devices` 的外键引用网络节点且限制删除；服务 `asset_services` 又通过外键引用 `asset_devices`。
- 因此不能只在现有删除接口末尾调用 `unbind_agent` 就声称完成，也不能强制 `DELETE` 造成服务损失。
- **推荐实现**：服务资产独立持久保留、节点从活跃设备视图解除，维护“原始节点身份/孤立资产”关系（可采用软删除/归档记录，详见 T00-04）；解绑应独立幂等，重新加入主机时允许新码配对。
- **禁止**：为了删除节点临时关闭 FK 校验、直接删除服务，或把“解除 Agent 绑定”假冒成“删除 EasyTier 设备”。

### G-06 服务同步与外部删除（M5）

- 现有服务 CRUD 可创建/更新/移除专属映射，`publication_apply` 已有运行时下发、冲突保护、重试逻辑。
- 仍需 AppInstance ↔ Service 的**明确关联表**与事务外补偿状态；只有 Agent 确认 Docker 完整查询成功且应用确已不存在，才能执行“外部删除”联动；Docker 查询失败不能删除任何资产。

### G-07 UI 与并发体验（M4）

- `DeployAgents.vue` 中 `busy` 是整个页面的共享布尔开关，旧 UI 一次只显示一个操作忙碌。与不同应用任务同时提交、按设备异步查询目标不一致。
- **复用组件内容而非现有串行状态模型**：将 loading/错误/日志按 host/app/task 粒度维护；长任务仅返回 task_id，不阻塞页面其他操作。

## 五、暂时不改的已有功能

- EasyTier 网络拓扑、VPN / 端口转发底层数据结构。
- 资产设备现有权限模型：admin/owner 即可，无需新 RBAC。
- 通用 Compose 和 Docker 脚本的全量替换；Agent 做适配器，Deploy 逐应用增加 manifest。
- 不引入心跳、MQTT、WebSocket、Redis、额外中心服务、自动回滚引擎。
- 旧 `deploy_agents` 数据不能在迁移时悄悄丢弃，需备份/可重复的迁移规则。

## 六、执行建议与后续顺序

| 优先级 | 下一任务 | 要解决的核心问题 |
|---|---|---|
| P0 | **T00-02 API v1** | `GET /v1/identity`、配对挑战/确认、设备绑定、`POST /v1/tasks`、日志游标、部署包上传；区分轻查询/长任务。 |
| P0 | **T00-04 数据关系** | 两级设备 ID 及删除设备保留服务规则、服务关联和 task owner；不要等到写 CRUD 时才处理外键约束。 |
| P0 | **T00-03 Deploy manifest** | 商店不依赖 Agent 目录，描述脚本/文件依赖、端口、数据卷、更新/卸载与兼容性。 |
| P1 | T01-01 Go Agent 工程 | 先完成身份与服务健康端点，再加 Docker。 |
| P1 | T03-02 Portainer app.yaml | 第一个集成用例应有真实可校验的单应用。 |

## 七、审查验收结论

- [x] 找到现有 Agent 后端、前端、数据库及测试文件。
- [x] 审查设备、资产类型、服务和公网映射的可复用业务路径。
- [x] 核实功能分支的历史关系，定位未合并/分叉范围。
- [x] 罗列复用/必须改造/必须新增模块及下一任务映射。
- [x] 识别设备删除保留服务冲突和现有按整台 Agent 锁定问题。
- [ ] 实际编译、CI、FnOS 操作测试（非 T00-01 范围；后续任务完成）。

**结论：T00-01 静态审查完毕，可进入 T00-02。不得把本次任务误报为 EasyHostAgent Go 程序已开发。**

## 源码入口（静态审查凭证）

- [Agent 后端](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/deploy_agent.rs)
- [Agent REST](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/restful/deploy_agent.rs)
- [Agent 界面](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/frontend/src/components/DeployAgents.vue)
- [Agent 迁移](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/migrator/m20261024_000011_deploy_agents.rs)
- [资产服务模块](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/db/assets/services.rs)
- [公网映射管理](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/db/assets/publications.rs)
- [公网映射运行时应用](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/restful/assets/publication_apply.rs)
- [设备删除入口](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/central_network/service.rs)
- [资产外键建模](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/migrator/m20261023_000012_node_assets.rs)
- [Deploy 主部署脚本](https://github.com/CiBeDerORG/deploy/blob/main/deploy.sh)
