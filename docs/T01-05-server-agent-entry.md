# T01-05 — EasyTier 已有服务器资产设备的 Agent 接入入口

> 日期：2026-10-27
> 实现分支：`CiBeDerORG/EasyTier` / `feat/automation-center`
> 代码提交：[8e67f1f9](https://github.com/CiBeDerORG/EasyTier/commit/8e67f1f9e67c8413afe920b083e05074f7ff5666)
> 状态：**DONE（入口实现、Vue/Web 构建与 Node 单测通过，Rust 测试目标编译及原有回归测试通过；新增 REST 运行时用例尚未单独执行，完整 GHCR 打包仍在运行）**。
> 本任务不执行 Agent 配对、不修改设备或服务记录、不额外创建主机。

## 一、开发实现

| 位置 | 功能 |
|---|---|
| [AssetDeviceTable.vue](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/frontend/src/components/node-assets/AssetDeviceTable.vue) | 只在 `semantic_kinds` 明确包含 `server` 时呈现“Agent”入口。单选 OpenWrt、phone、旧 `semantic_kind` 不得意外启用 |
| [NodeAssetsPage.vue](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/frontend/src/components/NodeAssetsPage.vue) | 点击现有资产设备后打开接入 Dialog，切换 owner/network node 时自动关闭，旧设备请求不得污染新上下文 |
| [ServerAgentEntryDialog.vue](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/frontend/src/components/node-assets/ServerAgentEntryDialog.vue) | 显示稳定资产设备 ID、所属网络节点、overlay/LAN 地址、状态“未检测”及本地安装/查看指纹/本地配对码指引 |
| [server-agent-messages.ts](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/frontend/src/modules/server-agent-messages.ts) | 简体中文、英文引导文本 |
| [server-agent-entry.ts](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/frontend/src/modules/server-agent-entry.ts) | 服务器语义判断、服务端读取结果是否匹配当前 owner/node/asset 的保护 |
| [api.ts](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/frontend/src/modules/api.ts) | 复用现有 cookie 权限和 `AssetScope`，只读调用服务器设备入口 |
| [server_agent_entry.rs](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/restful/assets/server_agent_entry.rs) | 新增 GET 只读入口；调用现有 `scoped_context` 做 owner/admin 鉴权，复查实际设备 `semantic_kinds`，拒绝不满足 server 条件的请求 |
| [assets/mod.rs](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/restful/assets/mod.rs) | 注册 `/api/v1/assets/devices/{id}/agent-entry` |
| [assets/tests.rs](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/restful/assets/tests.rs) | 追加后端路由集成测试：owner/admin、其他用户403、未登录401、webhook模式403、非server409、设备不存在404 |
| [server-agent-entry.test.mjs](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/frontend/tests/server-agent-entry.test.mjs) | 检测显式 server 多语义和跨 owner/node/asset/状态的旧响应 |
| [.github/workflows/server-agent-entry-tests.yml](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/.github/workflows/server-agent-entry-tests.yml) | 专用 Node 22 测试工作流，不依赖 Docker/Agent/配对码 |

## 二、实际 API 合约

```http
GET /api/v1/assets/devices/{asset_device_id}/agent-entry?owner_user_id={owner}&machine_id={node}
Cookie: <existing authenticated EasyTier session>
```

返回代表**已登记的设备上下文**而非 Agent 连接情况：

```json
{
  "owner_user_id": 10,
  "machine_id": "registered-easytier-node-uuid",
  "asset_device_id": "existing-asset-device-uuid",
  "device_name": "NAS",
  "overlay_ip": "10.80.0.8",
  "lan_ip": "192.168.0.8",
  "agent_status": "not_checked",
  "pairing_available": false,
  "source": "existing_asset_device"
}
```

- 所有用户/设备关系由现有 `scoped_context` 及 `AssetsStore::get_device` 决定；管理员可访问其他 owner，但不改变 owner。
- 只有真实 `semantic_kinds` 数组含 `server` 的设备可通过接口。仅名称叫 NAS/服务器、仅旧 `semantic_kind` 字段不能冒充。
- 只读请求不会调用 Go Agent HTTP、不会拉取 Deploy Git、不会生成一次性码或变更任何数据库资源。
- 前端不根据“未检测”推断 Agent 未安装或离线，也不提供伪造的“已配对”标记。
- 准备安装需手动下载对应架构的 Go CI 测试版二进制；**GitHub Release 当前未正式发布**，并未提供一键安装假链接。
- 绑定可信度必须来自 T01-06 中 EasyTier Rust 对 Agent TLS 公钥 SPKI 的验证；远端返回的 SHA256 文本本身不是可信来源。

## 三、测试与验收

- [x] Node 22 的服务器候选判断、owner/node/device 匹配测试已执行成功：[Run 40430588698](https://github.com/CiBeDerORG/EasyTier/actions/runs/40430588698)。
- [x] Rust 单元判断 `server_agent_eligible` 已写入代码（仅明确多语义 server）。
- [x] Rust 实际路由集成测试已写入代码，但**尚未获得此新测试的 CI 运行结果**。
- [ ] Vue `vue-tsc` 与完整 Rust / GHCR 构建验收：以对应最新主镜像 CI 结果为准（[CI 入口](https://github.com/CiBeDerORG/EasyTier/actions)）。
- [ ] FnOS / Linux 真实设备 HTTPS 探测、证书固定指纹、一次性配对以及绑定后主机状态均是 T01-06/T01-08，不属于 T01-05。
- [ ] 解绑时归档保护、保留服务和历史任务对应后续数据库迁移与删除生命周期任务，本项尚不改变现有删除逻辑。

## 四、后续开发接口

**T01-06**：在此只读入口原地增加“检测 Agent”与“输入配对码”流程，Rust 端从设备 IP 访问 Agent 并用可信 SPKI pin、Agent UUID、绑定身份及会话权限完成配对与稳定记录。不能把老的手动新增 `DeployAgents` 流程视为 T01-06 已完成。

遵守总体需求：无周期心跳，不让 Agent 访问 GitHub，不自动回滚，失败保留现场。
