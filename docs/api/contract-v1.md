# T00-02：EasyTier ↔ EasyHostAgent HTTP API v1 契约

> 状态：**DONE（接口契约已定义并完成静态校验；尚无 Go/Rust 两端真实实现）**  
> 日期：2026-10-27  
> 唯一机器可读规范：[`openapi-v1.json`](./openapi-v1.json)（OpenAPI 3.1.0）。  
> 上游：[`T00-01-easytier-audit.md`](../T00-01-easytier-audit.md)、[`requirements-plan.md`](../requirements-plan.md)。  
> 范围：**EasyTier Rust 后端 → EasyHostAgent Go 原生服务**的宿主机 API。浏览器 → EasyTier 的会话/权限 API 不在此规范内。

## 1. 核心分工（必须遵守）

- EasyTier 负责：账户 session、admin / owner 权限、现有 EasyTier 设备与语义类型、中央 Deploy 商店（仅手动刷新时 git pull）、部署包生成、应用实例、EasyTier 服务记录和公网映射。
- Agent 负责：本机身份和配对、本机 Docker / Compose 观察及操作、接收并校验部署包、本地异步任务和日志、主机基本信息、Agent 自身升级动作。
- 两者只使用按需 HTTPS 请求；**无周期心跳、定时轮询、WebSocket 长连接、MQTT、离线任务队列**。仅页面打开且用户主动要求查看正在运行任务时才允许前端做短时按需查询。
- Agent **绝不 git clone/pull 中央 Deploy**，也不持有 GitHub 私有仓库凭证；`/v1/catalog` 不能作为商店源头。
- **无自动回滚**：安装、更新、卸载、配置、取消和 Agent 自升级失败后统一标记失败、保留现场和日志，由用户人工处理。

## 2. 安全接入和设备绑定

### 2.1 一次性配对

1. 用户在已标记 `server` 语义类型的 EasyTier 设备详情中点击安装指引，执行宿主机本地安装命令。Agent 初装时生成不可变 `agent_id` 及其 TLS 身份。
2. 宿主机本地 `easyhostagent pairing-code` 生成**随机一次性码**，建议至少 80 bit 熵，10 分钟内有效，单次使用，最多 5 次失败尝试/10 分钟。不得在未授权 HTTP API 提供“生成配对码”操作。
3. Agent 本地命令同时显示服务器 TLS **SPKI SHA-256 指纹**；用户必须通过可信的本地终端/可信的安装输出核对。EasyTier 管理端仅在指纹匹配后向对应设备当前 IP 的 HTTPS 端点发送配对码。**不能以 GET /v1/identity 自己返回的指纹作为信任证据；不能关闭证书验证。**
4. `GET /v1/identity` 返回协议版本、`agent_id`、主机类型/架构、指纹。只有在 TLS 信任检查通过后，Rust 调用 `POST /v1/pairings`。
5. 配对请求提供 `controller_id`（EasyTier 管理实例稳定 UUID）、`device_id`（当前 EasyTier 资产设备 UUID）、一次性码；Agent 原子消耗配对码并生成仅响应一次的高强度 Bearer Token，后端加密保存凭证及绑定。
6. Agent 记录控制端身份并核对后续 `X-EHA-Controller-ID`、`X-EHA-Agent-ID` 和 Token。请求人的 admin/owner 权限仍由 EasyTier 后端验证，**Agent 不接受浏览器直接传来的用户角色声明**。
7. 若原已绑定、用户要重新配对：必须由宿主机本地管理员授权重置/开启新配对窗口；新配对成功时旧授权失效。自动检测其他 Agent 的 IP 不构成授权。

`device_id` 是 EasyTier **资产设备** `asset_devices.id`；网络节点 `devices.machine_id` 属于上层范围键。二者不能混用；多对一归属细节由 T00-04 处理。

### 2.2 解绑与离线撤销

- `DELETE /v1/pairings/current` 是在线可用时让 Agent 撤销当前控制端授权的指令。
- 删除 EasyTier 设备时，EasyTier 应立刻本地撤销并删除自己的认证凭证、解绑该设备，不删除 Agent 程序或 Docker 应用。
- **限制必须坦诚**：Agent 离线时，EasyTier 不能声称远端 Token 已被抹除；只能够立即停止 EasyTier 端使用旧凭证。重新绑定前应由宿主机本地管理员清除旧授权或生成经授权的新配对，新配对成功后令旧 Token 失效。V1 不引入违反“不排离线任务”的后台撤销队列。
- 旧主机被更换、TLS 公钥变化或 `agent_id` 不一致时，必须拒绝当前会话并让用户重新核对，不自动信任同 IP 新设备。

## 3. 接口一览（Agent 端）

| 方法 | 路径 | 鉴权 | 描述 |
|---|---|---|---|
| GET | `/v1/health` | 无 | 仅查询 HTTP 服务存活，不用于心跳 |
| GET | `/v1/identity` | 无 | Agent 唯一身份、TLS 指纹、协议版本 |
| POST | `/v1/pairings` | 配对码 + 已验证 TLS | 获取一次性 Bearer 授权 |
| DELETE | `/v1/pairings/current` | Bearer | 主动撤销当前授权 |
| GET | `/v1/host` | Bearer | 主机 CPU、内存、磁盘、Docker 状态 |
| PATCH | `/v1/host/settings` | Bearer | 修改 `SOFTWARE_ROOT`（无受管应用时） |
| GET | `/v1/docker/inventory` | Bearer | 完整/不完整 Docker 项目与独立容器快照 |
| GET | `/v1/docker/resources/{resource_id}/logs` | Bearer | 按需查看外部 Docker 资源日志 |
| HEAD | `/v1/bundles/{sha256}` | Bearer | 检查部署包是否已缓存 |
| PUT | `/v1/bundles/{sha256}` | Bearer | 上传固定内容哈希的部署包，**只存储不执行** |
| POST | `/v1/tasks` | Bearer | 创建异步任务；同 ID 同请求幂等 |
| GET | `/v1/tasks` | Bearer | 查询最近任务，用于重进页面 |
| GET | `/v1/tasks/{task_id}` | Bearer | 查询具体任务当前状态 |
| GET | `/v1/tasks/{task_id}/logs` | Bearer | 使用字节偏移增量取日志 |
| POST | `/v1/tasks/{task_id}/cancel` | Bearer | 请求安全取消，不回滚 |

所有已鉴权 JSON 响应的 Agent 身份要与预期 `agent_id` 一致；Authenticated 请求应带 `X-EHA-Controller-ID`、`X-EHA-Agent-ID`，并使用 `Authorization: Bearer <access_token>`。前端只调用 EasyTier 的同源 session API，不能直接读取 Token 或调用 Agent 高权限接口。

## 4. 固定请求和响应示例

以下 UUID、IP、Token 均为示例占位，**不是有效配对码或真实系统凭据**。

### 4.1 Agent 身份

```http
GET /v1/identity HTTP/1.1
Host: 192.0.2.10:7014
```

```json
{
  "protocol_version": 1,
  "agent_id": "11111111-1111-4111-8111-111111111111",
  "hostname": "fnos-lab",
  "platform": "fnos",
  "arch": "amd64",
  "version": "0.1.0",
  "tls_spki_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "observed_at": "2026-10-27T00:00:00Z"
}
```

### 4.2 配对（TLS 指纹已可信确认）

```http
POST /v1/pairings HTTP/1.1
Content-Type: application/json
```

```json
{
  "controller_id": "22222222-2222-4222-8222-222222222222",
  "device_id": "33333333-3333-4333-8333-333333333333",
  "pairing_code": "EXAMPLE-ONE-TIME-CODE"
}
```

成功返回 `201`（凭证只显示一次）：

```json
{
  "agent_id": "11111111-1111-4111-8111-111111111111",
  "controller_id": "22222222-2222-4222-8222-222222222222",
  "device_id": "33333333-3333-4333-8333-333333333333",
  "access_token": "EXAMPLE-ONLY-NOT-A-REAL-ACCESS-TOKEN",
  "paired_at": "2026-10-27T00:01:00Z"
}
```

### 4.3 Docker 清单（防止误删）

```json
{
  "agent_id": "11111111-1111-4111-8111-111111111111",
  "observed_at": "2026-10-27T00:02:00Z",
  "complete": false,
  "status": "error",
  "resources": [],
  "error": "Docker Engine query failed"
}
```

**绝不能**将以上 `resources: []` 解释成“应用已被删除”。只有 `complete=true` 且 `status=ok` 的完整查询，才能作为外部删除判定的必要前提；业务层还要对照之前确定的稳定标识及受管记录。

### 4.4 先传包，再发任务

上传前由 EasyTier 生成目标应用及共享依赖的部署包，计算原始 bytes SHA-256；Agent 收到后边读边计算并校验。最大建议 64 MiB，不允许路径逃逸、符号链接指向外部、设备文件或任意外部 Shell 命令。

```http
PUT /v1/bundles/{sha256} HTTP/1.1
Authorization: Bearer <TOKEN>
X-EHA-Controller-ID: 22222222-2222-4222-8222-222222222222
X-EHA-Agent-ID: 11111111-1111-4111-8111-111111111111
Content-Type: application/octet-stream

<VALIDATED-ARCHIVE-BYTES>
```

注意 `{sha256}` 为实际包字节的 64 位小写十六进制摘要；PUT 上传后仅缓存，不自动执行。

提交任务：

```json
{
  "task_id": "44444444-4444-4444-8444-444444444444",
  "kind": "application",
  "action": "install",
  "application_id": "portainer",
  "bundle_sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "catalog_revision": "0123456789abcdef0123456789abcdef01234567",
  "variant": "docker",
  "parameters": { "PORTAINER_HTTP_PORT": "9000" },
  "secrets": {},
  "options": { "retain_data": true }
}
```

返回 `202`（持久化成功，不表示已安装成功）：

```json
{
  "agent_id": "11111111-1111-4111-8111-111111111111",
  "task_id": "44444444-4444-4444-8444-444444444444",
  "kind": "application",
  "action": "install",
  "application_id": "portainer",
  "status": "accepted",
  "created_at": "2026-10-27T00:03:00Z",
  "updated_at": "2026-10-27T00:03:00Z",
  "log_offset": 0
}
```

重复提交**完全相同的任务 ID 与规范化请求**返回已有任务，不重新执行；同 ID 不同内容返回 `409`。如果 EasyTier 超时，先通过 `GET /v1/tasks/{task_id}` 查询；确认不存在后才允许用相同任务 ID 补交，不能生成新 ID 无条件重试。

### 4.5 增量日志

```http
GET /v1/tasks/44444444-4444-4444-8444-444444444444/logs?offset=0
```

```json
{
  "agent_id": "11111111-1111-4111-8111-111111111111",
  "task_id": "44444444-4444-4444-8444-444444444444",
  "offset": 0,
  "next_offset": 16,
  "text": "Step 1 started\n",
  "eof": false,
  "truncated": false
}
```

`offset` 是日志存储**字节偏移**，不能按字符数累计；服务器必须在 UTF-8 边界切片（例子 ASCII），客户端以 `next_offset` 为下一次请求参数。返回每次至多 256 KiB，密钥与配对码必须在持久化日志前脱敏。

## 5. 任务语义

所有 Docker 更改都创建持久任务，**不在 HTTP 连接内执行完长脚本**。

| 字段 | 值与约束 |
|---|---|
| `kind` | `application`、`docker_resource`、`host` |
| `action` | `install/update/uninstall/configure/adopt/start/stop/restart/install_docker/upgrade_agent`；按 kind/manifest 做组合白名单校验 |
| `task_id` | 调用方产生 UUID，幂等键；请求摘要包含全部规范化参数与 secret 值的安全摘要 |
| `bundle_sha256` | install/update/configure/adopt 和 Agent 升级需按对应允许动作验证部署资源，不允许指向任意路径 |
| `options.retain_data` | 卸载默认 `true`；彻底删除需要 `retain_data=false` 且 `confirm_delete_data=true`，数据路径必须受限于已归属资源 |
| `status` | `accepted`、`running`、`cancelling`、`succeeded`、`failed`、`cancelled`、`interrupted` |
| `outcome_unknown` | **EasyTier 本地代理提交状态**，Agent 未确认收到前使用，不应作为 Agent 实际执行状态 |
| 重启中断 | 已开始但因宿主机/Agent 重启中断的任务进入 `interrupted` 终态，**UI 展示“失败（中断）”**，不自动重试或回滚 |
| 并发 | 不同应用可并行，同应用互斥；操作共享配置与 Docker 全局资源时额外加锁 |
| 取消 | 请求安全停止，可能到达检查点前不能立即生效；失败和取消均保留现场 |

合法组合至少包括：
- `application`: install、update、uninstall、configure、adopt、start、stop、restart；由 app manifest 决定是否支持某动作。
- `docker_resource`: start、stop、restart；日志为专用读取端点；接管必须在验证对应 `application` manifest 后进行。
- `host`: install_docker、upgrade_agent；要求额外风险校验与受支持 OS/架构白名单。

## 6. 错误码、超时与容量

成功：`200` 查询、`201` 配对或首次包上传、`202` 长任务已持久化接收、`204` 无响应体的删除或 HEAD 已存在。

失败（JSON `{code,message,retryable,request_id?}`）：

| HTTP | 语义 |
|---|---|
| 400 | 错误参数 / 非法格式 |
| 401 | 未授权 / Token 失效 |
| 403 | 对应 Agent/controller/device 绑定不符合 |
| 404 | 未知任务、资源或部署包 |
| 409 | 端口/资源冲突、重复 task ID 不同请求、任务状态不允许 |
| 413 | 上传包或响应体太大 |
| 422 | 平台不兼容、动作不支持、配置校验不通过 |
| 429 | 配对尝试过多 |
| 500 | Agent 内部错误（不返回敏感堆栈） |
| 503 | Docker 或执行能力不可用 |

建议可配置上限（OpenAPI 中有默认建议值）：

| 类型 | 建议值 |
|---|---|
| TCP 连接超时 | 3 秒 |
| 普通请求总超时 | 15 秒 |
| 任务提交 | 15 秒，仅接收持久化，不等待任务完成 |
| 上传包 | 180 秒 |
| JSON 最大响应 | 8 MiB |
| 部署包最大体积 | 64 MiB |
| 每次日志响应 | 256 KiB |

不要自动重试有副作用的接口；任务丢失响应通过**原 task_id** 查询/重发。HTTP 客户端应禁用自动重定向和环境代理，并禁止转发用户 session/明文密钥。

## 7. 现有 EasyTier 代码的迁移对照

| 现有代码约定（`feat/automation-center`） | 新契约处理 |
|---|---|
| Rust `NewAgent {alias,endpoint,ca_pem,token}` + `POST /api/v1/deploy/agents` | Rust 侧重构为“现有服务器设备的 Agent 配对”，不直接把浏览器 Token 给 Agent |
| Rust `GET /v1/identity` | **保留路径**；增加 TLS SPKI 指纹/版本协商，首次配对必须本地验证 |
| Rust `POST /v1/discovery` | Docker 应用资源改用 `GET /v1/docker/inventory`；一般系统服务发现不在本期 Docker 商店 API 内 |
| Rust `GET /v1/catalog` | **不再从 Agent 提供商店**；由 EasyTier 中央本地 Deploy 工作副本提供 |
| `Authorization: DeployToken <base64>` | 新协议 `Authorization: Bearer <opaque-token>`；Rust 客户端需改造，旧接口是否短期兼容由实现迁移计划决定 |
| `X-Deploy-Agent-ID` | 统一为 `X-EHA-Agent-ID`；配合响应体 `agent_id` 及持久证书信任进行识别 |
| `POST /v1/tasks` / `GET /v1/tasks/{id}` | 保留任务模式和 URL，使用新 `kind/action/bundle_sha256` 字段及状态约束 |
| `GET /v1/tasks/{id}/logs?offset=` | **保留**日志游标模式，明确“偏移量按字节” |
| Rust `deploy_agents` / `deploy_tasks` SQLite | **复用/迁移**，加入设备绑定与应用实例数据；不新增与原来平行的第二套中心任务系统 |
| Rust `operation(user,agent_id)` 全 Agent `try_lock_owned` | 只保护短时间内的控制端敏感修改；并发、资源锁由 Agent 按应用/共享资源管理 |
| Rust 现有 assets service/publications | 仍由 EasyTier 统一创建/更新/删除服务和专属映射；Agent 无该 API 的写权限 |

## 8. 需要给后续任务的明确实现要求

- **T00-03** 为 Deploy `app.yaml` 定义应用 ID、动作、依赖、密钥、数据卷和端口；OpenAPI 仅运输与任务，不替代商店 Manifest。
- **T00-04** 需要解决 `devices.machine_id`、`asset_devices.id`、`agent_id` 多级归属及删除节点保留服务的外键冲突；本文件不假装已经解决。
- **T01-03/04** 实现 HTTPS 信任引导、证书轮换及真正的一次性配对；只返回指纹但没有用户可信核对不是安全配对。
- **T03-07/08** 固定二进制包格式（tar/zip、manifest 与完整依赖清单）、包大小/路径安全规则；M0 契约只固定 PUT 按摘要传字节。
- **T04-01** Agent 持久化任务，统一幂等和重启中断策略；没有自动回滚。
- **T06-03** 现有用户会话权限和设备归属由 Rust 验证，Agent 只识别已配对控制端和允许动作。

## 9. T00-02 自检与验收记录

- [x] 提交 **OpenAPI 3.1 JSON**，包含身份、配对、主机、Docker、部署包、任务/日志/取消共 **15 个 Agent 端操作**。
- [x] 文档中明确成功/错误响应、状态机、超时、认证、请求体限制与示例。
- [x] 在生成规范时完成结构自检：本地 `$ref` 均能解析、各 operationId 唯一、每个路由有 2xx 响应、未授权路由显式标明 `security: []`。
- [x] 明确 Rust 旧接口的复用与迁移对应关系，以及与中央商店模式的改变。
- [ ] 真实 Rust/Go 合约测试、Go 服务运行、FNOS 连接、外部 OpenAPI 规范解析器校验：**后续实现和 CI 阶段**，当前不声称已经完成。

**结论：T00-02 接口契约可供 Go/Rust 实施；下一项 T00-03 是 Deploy `app.yaml` 规范。**
