# T01-06 — EasyTier 已登记服务器设备的手动 Agent 检测与配对

> 日期：2026-10-27
> 代码分支：`CiBeDerORG/EasyTier` 的 `feat/automation-center`
> 状态：**IN_PROGRESS（实现已提交，Rust/Vue/原生回归及真实 FnOS 联调尚待确认）**
> 最新 CI：[GHCR image / feat-automation-center #40504366359](https://github.com/CiBeDerORG/EasyTier/actions/runs/40504366359)
> 前提：T01-03 Agent 稳定证书和身份、T01-04 一次性配对、T01-05 server 设备入口。

## 本轮实现

### 1. 用户操作流程

1. 在现有 EasyTier 资产设备（类型 `semantic_kinds` 明确包含 `server`）点击「Agent」，不会新建第二台主机。
2. 弹窗从当前设备取 overlay/LAN IP，选择一个用于手动连接；端口固定 HTTPS `7014`，禁止填写任意 URL。
3. 在宿主机可信终端执行 `sudo /usr/local/bin/easyhostagent -identity` 获取 SPKI SHA-256，复制到 EasyTier，明确确认指纹来自可信本地来源。
4. 点击「检测 Agent」，Rust 服务端的 TLS 1.3 握手**先验证对端叶证书的 SPKI**、签名和有效期，再读取 `/v1/identity`；比较 Agent UUID、响应头、版本、指纹；检测失败不保存新 IP。
5. 在宿主机执行 `sudo /usr/local/bin/easyhostagent -pairing-code`，获取最多 10 分钟、一次可用的配对码。
6. 页面手动提交配对码，EasyTier Rust 端重复验证同一 TLS 公钥和 Agent ID，再调用 `POST /v1/pairings`；核对结果后把新 Token 通过 AES-GCM 加密，只存储在数据库；浏览器仅收到设备绑定摘要。
7. 返回设备绑定状态。**状态默认是「未按需检查在线」，不启动心跳**；以后用户在 IP 更新后再次手动检测时，必须是相同 Agent UUID + 相同受信任 SPKI 才更新保存地址。

## 2. 后端新增文件及变更

| 文件 | 功能 |
|---|---|
| [`asset_agent_tls.rs`](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/asset_agent_tls.rs) | rustls TLS1.3 定制证书验证器，严格 DER 提取 SubjectPublicKeyInfo 并固定 SHA-256；TLS 握手签名验证、有效期检查及独立 ECDSA DER 测试样本 |
| [`asset_agent.rs`](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/asset_agent.rs) | 受控 IP 目标、手动检测与配对、绑定保存、IP 变更时身份核对、防重复绑定及 Token 封存 |
| [`restful/asset_agent.rs`](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/restful/asset_agent.rs) | 状态查询、手动检测、配对三个按 owner/node/device 鉴权的 HTTP 路由 |
| [`m20261027_000027_asset_agent_bindings.rs`](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/migrator/m20261027_000027_asset_agent_bindings.rs) | 全局稳定 controller UUID 和每个资产设备最多一个 Agent 绑定；Agent UUID 全局唯一；设备删除时删本地关联，不删物理应用 |
| [`deploy_agent.rs`](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/src/deploy_agent.rs) | 复用既有 credential seal/open 能力，不复用旧的 `DeployToken` 协议 |
| [`Cargo.toml` / `Cargo.lock`](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/Cargo.toml) | 显式声明与 Rustls 版本一致的加密客户端依赖，Cargo 锁文件已同步 |

三个端点，均使用现有 EasyTier 登录会话权限并复查 server 语义设备：

```text
GET  /api/v1/assets/devices/{id}/agent/status?owner_user_id=...&machine_id=...
POST /api/v1/assets/devices/{id}/agent/probe?owner_user_id=...&machine_id=...
POST /api/v1/assets/devices/{id}/agent/pair?owner_user_id=...&machine_id=...
```

探测入参为 `address: "overlay"|"lan"` 和 `trusted_spki_sha256`；配对还需要 `pairing_code`。只允许原设备保存的 IP，排除回环、未指定、组播、IPv4/IPv6 链路本地目标。HTTPS 不使用代理，不跟随重定向；没有轮询、重试和自动回滚。使用现有会话 owner/admin 权限，外部 Webhook 模式不能越权。

## 3. 前端

[ServerAgentPairingPanel.vue](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/frontend/src/components/node-assets/ServerAgentPairingPanel.vue) 已嵌入现有 [ServerAgentEntryDialog.vue](https://github.com/CiBeDerORG/EasyTier/blob/feat/automation-center/easytier-web/frontend/src/components/node-assets/ServerAgentEntryDialog.vue)。

- 检测必须有 64 位可信指纹以及用户确认。
- 检测成功才启用 32 位一次性配对码提交；更改 IP 或指纹清除检测结果。
- 后端的 Bearer Token 不返回页面；不展示 `pairing.json` 私钥状态、持久凭证密文。
- 本地已保存绑定不等同于当前 Agent 在线。
- 成功清空一次性码，失败需人工确认后重试；不自动对 Agent 发起请求。

## 4. 验收进度

- [x] 代码已提交 EasyTier 功能分支。
- [x] Cargo.lock、Rust 1.95 格式化任务同步成功，临时 CI 文件已清理。
- [x] 单元/原生回归脚本已纳入 `asset_agent`、`server_agent_entry` 测试过滤条件。
- [ ] **完整 Vue/Web build、Rust Cargo 编译与新测试实际通过：等待 #40504366359 结果。**
- [ ] **真实 Go EasyHostAgent 与 Rust EasyTier 跨服务 HTTPS 配对联调未执行**，需有测试主机或隔离集成环境。
- [ ] **真实 FnOS 上 Agent systemd 安装及证书/防火墙环境验证未执行**，归 T01-08。
- [ ] Agent 的部署、Docker/Compose 与主机系统资源接口仍不在 T01-06 范围内，依后续任务实现。

## 5. 重要边界

1. 不得信任远端自己报告的 SHA-256；身份认证凭用户从**可信服务器终端**获取的固定公钥指纹建立。
2. 配对 Token 不返回浏览器，只能由 Rust 管理端封存；旧 Agent 的 `DeployToken` API 是另一协议，不能用于新 Go Agent 配对。
3. 故障不自动回滚；可能在 Agent 已消费一次性码但数据库写入失败的情形留下远程授权，要求现场明确人工重新配对或本地撤销。
4. 删除 EasyTier 资产记录会丢失本地绑定和 Token，但并不等价于远程 revoke：需要后续生命周期接口实现明确撤销；不能虚报“远端已解除绑定”。
5. 所有连接由用户点击触发，无后台状态查询、自动搜索或离线排队。

**下一步**：等 CI 完成后修正构建/测试错误，随后再标记 T01-06 代码验收；真实 FnOS 联调必须单独注明边界。
