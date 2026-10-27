# EasyHostAgent 开发任务计划（可执行拆分 v0.1）

> 制定日期：2026-10-27。关联需求：[`requirements-plan.md`](./requirements-plan.md)。
> 状态：**开发计划执行中：M0 五项完成，M1 的 T01-01～T01-05 已提交，累计 10/50；后续 40 项未开始。T01-05 Web/Node 及原有 Rust 回归成功；新增 REST 用例尚未实际执行，完整 GHCR 打包待结果。Deploy 合约 CI 仍需恢复**。
> 三仓库：`CiBeDerORG/EasyTier` (ET)、`CiBeDerORG/EasyHostAgent` (EHA)、`CiBeDerORG/deploy` (DEP)。
> 核心约束：**只支持首期 Docker / Compose 应用；无心跳；Deploy 仅由 ET 在手动刷新时本地 git pull；Agent 不拉 Git；无自动回滚；失败保留现场。**
> 实施模式：按编号顺序进行，每项尽量对应一个小 PR/提交、单独验收。多仓库功能由集成任务收尾；不得只改 Agent 或只改界面就宣称业务完成。

## 0. 里程碑与交付门槛

| 里程碑 | 产出 | 验收门槛 | 依赖 |
|---|---|---|---|
| M0 协议与兼容基线 | API 契约、Deploy manifest、分支比对与测试样本 | 三端字段/职责无冲突，明确旧代码复用清单 | 无 |
| M1 Agent 主机接入 | Go systemd Agent、身份配对、按需主机信息、ET 设备绑定 | FnOS 主机在 EasyTier 中自动发现、一次性配对并查询 | M0 |
| M2 Docker 基础管理 | Docker SDK、Compose 分组、外部容器管理与安全接管 | 同一 Compose 不拆散，外部应用仅有授权的基础操作 | M1 |
| M3 中央商店与资源交付 | Deploy app.yaml、手动 Git 更新、模板校验、打包与分发 | EasyTier 可将固定版本 Portainer 包发送至 Agent | M0、M1 |
| M4 安装与服务闭环 | 预检、配置、任务、Docker 安装校验、自动 EasyTier 服务关联 | FnOS 可安装 Portainer，能查询日志/状态并打开服务 | M2、M3 |
| M5 生命周期闭环 | 修改配置、更新、启停、卸载、服务端口与公网同步 | 安装后不用 SSH 完成管理，失败保留现场 | M4 |
| M6 发布与可靠性 | Agent 升级、Docker 一键安装、权限、异常测试、CI | 对照需求关键场景逐项通过，生成可安装版本 | M5 |

可平行工作：M1 Agent 框架与 M3 Deploy 元数据；M2 Docker 探测与 M3 商店索引；其余涉及服务关联/数据事务的工作必须等依赖接口稳定。

## 1. M0：协议与兼容基线（5 项）

| 任务 | 仓库/负责人 | 前置 | 交付与验收 |
|---|---|---|---|
| **T00-01 [DONE]** 对齐已有 EasyTier 实现 | ET · Rust 后端 | 无 | **静态审查已提交**：[T00-01-easytier-audit.md](./T00-01-easytier-audit.md)；已列出复用/迁移清单、分支关系、设备删除冲突；未构建、未更改业务代码。 |
| **T00-02 [DONE]** 定义 ET↔Agent API v1 | ET+EHA · 架构 | 无 | 已提交 [OpenAPI v1](./api/openapi-v1.json) + [契约说明与示例](./api/contract-v1.md)：15 个接口、23 个模型、7 份 JSON 示例；已校验内部引用、身份请求头、返回码和示例基本 schema。尚未实现或进行 Go/Rust 集成测试。 |
| **T00-03 [DONE]** 定义 Deploy `app.yaml` v1 | DEP+ET · 架构 | 无 | 已提交 Deploy [Schema](https://github.com/CiBeDerORG/deploy/blob/main/schema/app-manifest-v1.schema.json)、[Portainer](https://github.com/CiBeDerORG/deploy/blob/main/schema/examples/portainer.app.yaml)/[Sub2API](https://github.com/CiBeDerORG/deploy/blob/main/schema/examples/sub2api.app.yaml) 契约示例与[标准说明](https://github.com/CiBeDerORG/deploy/blob/main/docs/easyhostagent-app-manifest-v1.md)；[本轮检查记录](./T00-03-deploy-manifest.md)。完整 YAML/JSON Schema 校验器和 CI 属 T03-01。 |
| **T00-04 [DONE]** 定义持久化与身份归属 | ET+EHA · 后端 | T00-01/02 | 已提交 [数据模型设计](./T00-04-data-model.md)、[拟议 SQLite DDL](./schema/T00-04-proposed.sql)；明确所有权、设备逻辑归档、Agent 解绑保留任务/应用/服务；使用 SQLite 最小 FK 样例做静态 smoke test，尚未运行正式数据库迁移。 |
| **T00-05 [DONE，Deploy CI 待恢复]** 最小契约测试样本 | ET+EHA+DEP · 测试 | T00-02/03 | 已提交 EHA 的 OpenAPI / SQLite 14 项测试、Portainer/Sub2API/独立容器及异常 Docker fixtures；Deploy 的 JSON Schema/YAML/Compose 6 项测试与两仓库 CI 工作流。[完整验收记录](./T00-05-contract-tests.md)。[EasyHostAgent CI 已通过](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40386121798)；[Deploy workflow 未执行测试即失败](https://github.com/CiBeDerORG/deploy/actions/runs/40386319978)，非全绿验收。 |

## 2. M1：Agent 主机接入（8 项）

| 任务 | 仓库/负责人 | 前置 | 交付与验收 |
|---|---|---|---|
| **T01-01 [DONE]** Go 工程初始化 | EHA · Go | T00-02 | 已实现 `cmd/`、`internal/`、配置、JSON 日志、优雅退出、单元测试；[Go 双架构 CI 全绿](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40423379295)；[实现及验收报告](./T01-01-go-foundation.md)。无心跳、无 HTTP/Docker。 |
| **T01-02 [DONE，FnOS 实机待验收]** Linux/FnOS 安装器 | EHA · Go/运维 | T01-01 | 已提交 [原生安装器](../scripts/install.sh)、[数据保留卸载器](../scripts/uninstall.sh)、[systemd unit](../packaging/systemd/easyhostagent.service) 及 11 项隔离安装测试；[GitHub Actions 全绿](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40424901906)；[完整实现记录](./T01-02-native-installer.md)。暂未发布正式 Release，也未在真实 FnOS 执行 systemd 安装。 |
| **T01-03 [DONE]** 设备身份与 HTTPS API | EHA · Go | T01-01 | 已实现稳定 Agent UUID、0700/0600 私钥状态、TLS 1.3 自签名证书与 SPKI SHA-256、本地 `-identity`、只读 `/v1/health` 和 `/v1/identity`、默认拒绝的鉴权中间件；[Go race 测试及双架构构建全绿](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40427223740)；[实施报告](./T01-03-https-identity.md)。配对与持久授权为 T01-04。 |
| **T01-04 [DONE]** 一次性配对 | EHA · Go | T01-03 | 已实现本地随机一次性码（10 分钟/5 次限制）、HTTPS 配对、Bearer Token 哈希持久化、重配旧 Token 失效和已鉴权解绑；[race 测试及双架构 CI 通过](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40428675525)；[实施与验收报告](./T01-04-pairing.md)。EasyTier Rust 的证书指纹绑定、页面联调归 T01-06。 |
| **T01-05 [DONE，新增 REST 用例待专项运行]** 易用的服务器设备入口 | ET · Vue/Rust | T00-01/04 | 已实现已登记 server 设备的 Agent 入口、只读鉴权接口、安装引导与旧响应范围防护。[Node 测试通过](https://github.com/CiBeDerORG/EasyTier/actions/runs/40430588698)，[Web 构建/原有 Rust 回归通过](https://github.com/CiBeDerORG/EasyTier/actions/runs/40430588552)；[报告](./T01-05-server-agent-entry.md)明确新增 REST 集成用例已编译但未运行。配对仍属 T01-06。 |
| T01-06 基于设备 IP 按需发现与绑定 | ET · Rust | T01-03/04/05 | 点击检测探测 Agent，输入配对码绑定；IP 改变后按需验证唯一身份并重定位；不靠心跳。 |
| T01-07 按需主机状态和安装目录 | EHA+ET · Go/Rust | T01-06 | 系统、架构、CPU、内存、磁盘、Docker 可用性按需返回；`SOFTWARE_ROOT` 系统默认、可配置，已有应用时禁止直接变更。 |
| T01-08 主机接入端到端验收 | ET+EHA · 测试 | T01-01~07 | FnOS 完成安装、发现、配对、查询、离线缓存、解绑后重新配对；无心跳/后台定时查询。 |

## 3. M2：Docker 识别与外部应用（7 项）

| 任务 | 仓库/负责人 | 前置 | 交付与验收 |
|---|---|---|---|
| T02-01 Docker Engine 适配器 | EHA · Go | T01-01 | Go SDK 检测 Docker、API 版本和权限，列出运行中/已停止容器与镜像、端口、卷、标签。 |
| T02-02 Compose 聚合 | EHA · Go | T02-01 | 基于 `com.docker.compose.project` 聚合服务容器，不将 Sub2API+数据库错误拆为多应用；独立容器单独展示。 |
| T02-03 Docker 基础操作与日志 | EHA · Go | T02-01 | 容器/Compose 运行状态、启动、停止、重启、按需日志；稳定标识及权限验证。 |
| T02-04 应用发现、匹配和接管 | EHA+ET · 后端 | T02-02/03,T00-03 | 区分 Deploy 受管、可接管、外部 Docker；外部只允许基础操作；用户确认匹配才接管；同主机同应用不得重复受管。 |
| T02-05 Docker 列表页面与状态缓存 | ET · Vue/Rust | T02-02/04 | 页面查询时刷新；离线展示上次数据及时间；服务停止不是已删除。 |
| T02-06 “外部删除”安全判定 | ET+EHA · 后端 | T02-04/05 | Docker 查询**完整成功**且资源确已不在时才触发应用记录/服务/专属映射清理；Docker 错误不能清理。 |
| T02-07 Docker 发现回归用例 | EHA+ET · 测试 | T02-01~06 | 验证多个 Compose 项目、独立容器、已停止容器、异常查询、接管、重复扫描不重复实例。 |

## 4. M3：Deploy 中央应用商店（8 项）

| 任务 | 仓库/负责人 | 前置 | 交付与验收 |
|---|---|---|---|
| T03-01 实现 manifest 解析/校验 | DEP · 脚本/规范 | T00-03 | 可检查缺失字段、不支持的动作、路径引用、端口优先级、类型错误；CI 校验生效。 |
| T03-02 为 Portainer 增加 app.yaml | DEP · 运维 | T03-01 | 描述 Docker 安装、更新、端口、数据路径、脚本依赖；旧命令行入口不变。 |
| T03-03 为 Sub2API 增加 app.yaml | DEP · 运维 | T03-01 | 正确描述多服务 Compose、主要 Web 端口、用户配置与数据目录。 |
| T03-04 本地 Deploy 商店工作副本 | ET · Rust | T00-03 | EasyTier 后端持有只读 Git 仓库凭证；**仅人工刷新** 才 clone/pull；失败保留旧缓存，不在浏览商店时自动拉取。 |
| T03-05 商店目录 API 与页面 | ET · Rust/Vue | T03-04 | 分类、搜索、详情、安装方式、参数定义、当前本地索引版本；无需 Agent 即可浏览。 |
| T03-06 动态安装表单 | ET · Vue | T03-05 | 从 manifest 生成端口、常规变量和密钥输入；不暴露隐藏密钥；目标主机兼容性提示。 |
| T03-07 固定版本部署包生成 | ET · Rust | T03-01/04 | 只打包目标应用的必要 Deploy 资源及公共依赖，保存来源 commit+SHA-256，过滤数据/密钥/非法文件；不要求 Agent 拥有 Git 权限。 |
| T03-08 Agent 部署包接收与校验 | EHA · Go | T03-07,T01-03 | 限制大小/路径/内容，验证摘要、授权和允许的动作；安全保存原始部署包供应用下架后的受管实例使用。 |

## 5. M4：安装任务与服务自动关联（8 项）

| 任务 | 仓库/负责人 | 前置 | 交付与验收 |
|---|---|---|---|
| T04-01 本地任务数据库与状态机 | EHA · Go | T01-01,T00-02 | SQLite 持久化任务 ID/状态/退出码/日志游标；请求去重，未知提交结果可查；重启中断标失败**不自动恢复**。 |
| T04-02 多任务并发与取消 | EHA · Go | T04-01 | 不同应用并行、同一应用互斥、全局配置资源加锁；安全取消、保留现场、无自动回滚。 |
| T04-03 Deploy 执行器 | EHA · Go | T03-08,T04-01 | 严格按 manifest 允许动作执行脚本/Compose，传参数但不允许前端任意命令；写执行日志、限制超时、控制进程组。 |
| T04-04 配置文件适配器 | EHA+DEP · Go/脚本 | T03-02,T04-03 | 兼容 Deploy 统一 `.env`/`.secrets`，锁和原子写入、不丢旧用户配置、密钥绝不明文回显；新参数用模板默认值。 |
| T04-05 安装预检与历史数据选择 | EHA+ET · 后端/前端 | T02-01,T04-04 | Docker/Compose、磁盘、端口、软件根目录与已安装状态；端口冲突要求手改；历史数据可选择复用/全新，原数据不被删除。 |
| T04-06 安装和结果验证 | EHA · Go | T04-03/05 | 安装 Portainer 后核对 Compose/容器与主入口，成功才标记完成；失败留现场/退出码/日志，不自动删除新建资源。 |
| T04-07 自动创建/关联 EasyTier 服务 | ET · Rust | T04-06,T00-04 | Web 入口优先，否则其他端口第一个；有匹配服务则关联、无则创建；无端口不创建；**默认不公网公开**。服务失败不反向卸载应用。 |
| T04-08 端到端安装验收 | ET+EHA+DEP · 集成 | T04-01~07 | FnOS 经商店安装 Portainer，查看任务日志和服务入口；关页面仍执行；失败可见且无自动回滚。 |

## 6. M5：日常管理与生命周期（7 项）

| 任务 | 仓库/负责人 | 前置 | 交付与验收 |
|---|---|---|---|
| T05-01 受管应用配置编辑 | ET+EHA · 前后端 | T04-04/07 | 读取并修改用户参数，检测变化是否需要重建/重启；修改成功后复查 Docker，不能只保存表单。 |
| T05-02 受管应用更新 | EHA+DEP · Go/脚本 | T05-01 | 复用 Deploy update.sh，保留用户参数，新增值取默认；外部修改以 Deploy 更新定义覆盖；失败只标失败/留现场，不回滚。 |
| T05-03 启动/停止/重启应用 | EHA+ET · Go/前端 | T02-03,T04-07 | 使用 Docker API/Compose，清晰显示运行状态，不将停止误判为“外部删除”。 |
| T05-04 应用卸载及数据选项 | EHA+DEP+ET · 集成 | T05-03,T03-01 | 统一卸载操作；默认保留数据，彻底删除二次确认；只删除有明确归属的资源；卸载失败不提前删服务。 |
| T05-05 服务/公网映射端口同步 | ET · Rust | T05-01,T04-07 | 应用主入口变化后更新已关联服务和公网映射**目标端口**，保留公网监听配置；部分失败可见、可手动重试。 |
| T05-06 卸载服务联动与下架保留 | ET · Rust | T05-04,T03-08 | 正常卸载后删除全部关联 EasyTier 服务（含手工服务）及专属公网映射；Deploy 商店下架不影响已安装实例的基本管理。 |
| T05-07 多容器与失败场景验收 | ET+EHA+DEP · 测试 | T05-01~06 | Sub2API 多容器识别、配置更新、失败留现场、数据保留、正常卸载服务清理、Portainer 外部修改更新覆盖。 |

## 7. M6：发布与可靠性（7 项）

| 任务 | 仓库/负责人 | 前置 | 交付与验收 |
|---|---|---|---|
| T06-01 Agent 手动升级 | EHA+ET · Go/Rust | T01-08,T04-01 | 页面手动触发；执行任务时禁止升级；升级失败保留错误供手动修复，不构建自动回滚程序；持久授权和配置不得丢。 |
| T06-02 Docker 一键安装 | EHA+ET · Go/Rust | T01-07 | 未安装 Docker 时显式确认，系统支持矩阵白名单，不修改已有安装；安装后检验 Engine/Compose。 |
| T06-03 操作授权与用户归属 | ET+EHA · 安全 | T00-04,T04-03 | admin 管理全部，普通用户仅自有设备/应用/服务；绑定授权隔离，部署任务/配置读取均服务端校验。 |
| T06-04 高风险边界和审计 | ET+EHA · 安全 | T06-03 | 请求、包解压、参数、命令固定白名单；密钥加密不泄露；审计操作人/时间/结果；防 SSRF、路径穿越和重复提交。 |
| T06-05 异常一致性验收 | ET+EHA · 集成 | T05-07 | 离线缓存、重启中断失败保留、Docker 查询失败不误删、真实外部删除清理服务、公网映射失败可补办。 |
| T06-06 CI 与可安装发布包 | EHA+DEP+ET · DevOps | T06-01~05 | Go amd64/arm64 编译、静态测试、Go systemd 安装脚本、manifest CI、接口契约测试与 GitHub Release 附件。 |
| T06-07 最终产品验收 | 三仓库 · QA/产品 | T06-01~06 | 需求文件 §7 所列 A~F 全部通过；未实现项转入下一期，不把测试缺口当已完成。 |

**总数：50 项**（M0 5 + M1 8 + M2 7 + M3 8 + M4 8 + M5 7 + M6 7）。

## 8. 任务跟踪规范

每项用统一状态：`TODO` / `IN_PROGRESS` / `BLOCKED` / `DONE`；截至 2026-10-27：**T00-01～T00-05、T01-01～T01-05 [DONE]**（M0 五项 + M1 五项），余下 **40 项 [TODO]**。**遗留验收：T01-05 专项 REST 用例未执行、主镜像完整打包待完成，Deploy workflow 无步骤失败需补测**。下一个任务是 **T01-06：基于设备 IP 按需发现与绑定 Agent**；真实 FnOS systemd 安装留 T01-08。

执行每项时留下：
1. 需求 ID 与任务 ID；
2. 实际基准分支和 commit；
3. 涉及仓库、变更文件及 commit/PR；
4. 接口/脚本/数据库迁移结果；
5. 验收步骤和结果（失败要写原因）；
6. 新发现的风险与下一项依赖。

建议每一条小任务完成就提交对应仓库，不把 3 个仓库的大批更改混成一次发布；涉及协议的破坏性变更先更新契约测试并协调。

## 9. 明确不得擅自增加的工程范围

- **不做自动回滚**：包括失败安装清理、更新恢复旧镜像、取消回滚、Agent 升级自动回滚；任务失败保留现场。
- 不增加心跳、后台周期采集、自动商店同步和离线任务队列。
- 不允许 Agent 再去 `git pull` 私有 Deploy 仓库；只有 EasyTier 管理端手动刷新。
- 不为 EasyHostAgent 再建一套与 EasyTier 重复的设备、用户、服务管理系统。
- 不允许应用停止/离线或 Docker 接口报错触发删除应用与 EasyTier 服务。
- 不为首期应用商店增加原生 systemd/procd、OpenWrt apk/opkg、自动批量部署或多实例安装。

## 10. 立即可开工的第一组任务

1. **T00-01**：审查 EasyTier `feat/automation-center` 与当前目标分支的差异，写清复用列表。
2. **T00-02**：提交 ET↔EHA API v1 契约（包含一次性配对和按设备绑定）。
3. **T00-03**：冻结 Deploy `app.yaml` 初版协议。
4. **T01-01**：按冻结的协议初始化 Go Agent 骨架。
5. **T03-02**：为 Portainer 编写 manifest，作为第一套真实部署模板。

阶段验收通过再启动下一批任务。**计划完成不等于代码完成。**
