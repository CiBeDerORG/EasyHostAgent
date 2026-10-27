# T01-06：按资产设备 IP 手动检测、TLS pin 与控制端配对

> 日期：2026-10-27
> 状态：**已提交实现；Go Agent 自动测试、竞态检测及双架构构建通过；EasyTier Rust/Web 主 CI 仍在运行，真实 FnOS 尚未验收。**
> EasyTier 实现分支：[`feat/automation-center`](https://github.com/CiBeDerORG/EasyTier/tree/feat/automation-center)
> Go 验证：[Actions #40503796744](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40503796744)，success
> EasyTier 构建：[Actions #40504366359](https://github.com/CiBeDerORG/EasyTier/actions/runs/40504366359)，最终结果待验收

## 实际实现范围

1. 复用资产设备的 `owner_user_id`、`machine_id` 和 `asset_device_id`，服务端每次检测/配对都核实账户权限、设备存在和包含 `server` 的多语义设备类型；未另建 Host。
2. 在现有 `ServerAgentEntryDialog` 中新增 `ServerAgentPairingPanel`，由用户显式选取当前设备保存的 overlay/LAN IP、输入可信宿主机终端取得的 SPKI SHA-256 指纹，然后点击检测。配对码通过密码框提交，提交前清空前端输入，不进入 URL 或本地存储。
3. Rust `asset_agent_tls.rs` 使用 TLS 1.3 自定义证书校验器，在**握手时**核对服务端证书 SPKI SHA-256，而非等到收到 HTTP 响应后才确认，尤其不能先发送配对码或 Token。
4. Rust `asset_agent.rs` 只访问保存的字面 IP 和固定 `7014` 端口，拒绝 DNS、loopback、link-local 和广播类非法目标；关闭代理、重定向及请求自动重试。
5. 先通过固定指纹的 `GET /v1/identity` 比对 Agent UUID、HTTP Header、协议版本与证书信息，再使用一次性配对码 `POST /v1/pairings`。当 Agent ID 已属于另一条资产设备绑定时禁止静默转移。
6. 新迁移 `asset_agent_controller` 保存固定控制端 UUID，`asset_agent_bindings` 保存现有资产设备与 Agent 的 1:1 绑定；Token 使用 EasyTier 已有 AES-GCM 密钥机制加密，前端、日志和只读接口不返回明文。
7. 已有绑定在 IP 变更后手动检测时，先检查相同 UUID/固定证书指纹，再经 `GET /v1/pairings/current` 验证之前的控制端 Bearer 仍有效；失败不覆写历史地址。
8. Go Agent 的受保护 `GET /v1/pairings/current` 已实现并通过本地真实 TLS 集成测试；错误控制端、无效和已撤销 Token 均被拒绝。Go 保持“新配对成功后旧 Token 失效”的行为。
9. 绑定状态只展示历史缓存；不声明 Agent 在线，不添加 heartbeat、定时轮询、队列、自动重试或自动回滚。

## 新增接口

| 所在端 | 方法与路径 | 行为 |
|---|---|---|
| EasyTier | `GET /api/v1/assets/devices/{id}/agent/status?owner_user_id=...&machine_id=...` | 仅读取本地绑定，不主动访问 Agent |
| EasyTier | `POST /api/v1/assets/devices/{id}/agent/probe?owner_user_id=...&machine_id=...` | 手动按设备已存 IP + 可信指纹验证身份；绑定后还校验 Token |
| EasyTier | `POST /api/v1/assets/devices/{id}/agent/pair?owner_user_id=...&machine_id=...` | 手动提交一次性码、固定证书公钥、创建或更新绑定 |
| EasyHostAgent | `GET /v1/pairings/current` | 仅在合法 HTTPS Bearer + Controller ID + Agent ID 下返回 `authorized=true` |

## 验收记录

- [x] Go `gofmt`、`go vet`、`go test -race ./...` 成功。
- [x] Go Linux AMD64 与 ARM64 构建并上传成功：[Run #40503796744](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40503796744)。
- [x] Rust 源码与 Cargo.lock / rustfmt 已提交，专门加入 Agent 相关原生回归过滤器。
- [ ] EasyTier 原生 Rust 回归、新 REST 路由与 Vue 主构建必须等待 [Run #40504366359](https://github.com/CiBeDerORG/EasyTier/actions/runs/40504366359) 的最终成功结果；不能仅凭其他被取消的工作流宣称通过。
- [ ] 真实 FnOS 上对 IP 连通性、可信本机证书指纹、单次配对、重连 IP 变化的 E2E 尚未执行；T01-08 补充。
- [ ] 数据迁移、重配、凭证变更等 Rust SQLite 运行时异常路径需继续专项验证；CI 未完成时本任务保持 `IN_PROGRESS`。

## 下一步

先关闭 T01-06 的 Rust/Vue CI 遗留项，然后执行 T01-07：主机状态按需读取、FnOS/Linux `SOFTWARE_ROOT` 默认配置、Docker 检测和不可变更目录保护。切勿把旧式 `DeployAgents` 的 `DeployToken` 接口错误复用成 EasyHostAgent 管理授权。
