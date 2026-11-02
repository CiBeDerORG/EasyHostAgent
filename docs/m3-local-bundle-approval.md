# M3-03：Agent 本机 Bundle 信任批准（第一阶段）

> **本阶段只建立独立的本机批准记录与复核能力，没有任何安装、解压或任务执行接口。** 已配对的 EasyNetTopo、Git 来源白名单、ZIP 摘要与 `HEAD/PUT` 缓存成功都**不能**自动授权脚本运行。

## 安全与职责

- 控制中心依照已固定 Git Commit、Deploy v1 Schema 和语义规则生成不可变 ZIP，并使用已配对 TLS/SPKI/Token 将 ZIP **缓存**到 Agent。
- Agent 在受控数据目录的 `bundles/<sha256>.zip` 中重新校验真实字节 SHA-256、ZIP 资源文件内容、内部 `bundle-manifest.json`、`<app>/app.yaml` 的摘要、声明的文件权限、数量与位置；这些声明仍只是**包内自报**，不是签名。
- 唯有拥有 Agent 主机本地受信任终端访问权的管理员，才能执行 CLI 的批准、复核和撤销。HTTP API **未新增**发布批准接口，配对 Token 不能远程批准。
- 批准绑定 `agent_id + bundle_sha256 + source_id + app_id + git_revision + manifest_sha256`。操作者必须从独立可信渠道核对来源、版本及 ZIP 摘要，再显式输入身份信息。**提交者自报的字段不等于真实发布者身份**。
- 批准保存至 `<EASYHOSTAGENT_DATA_DIR>/approvals/<sha256>.json`（目录 0700、记录 0600），采用 `fsync` + 不可覆盖硬链接落盘。重启保留；损坏/类型错误/权限异常拒绝读取，不自动恢复或覆盖。
- `trust.Store.Check(digest)` 每次重新读取批准记录并核对已缓存 ZIP。撤销后即拒绝。来自其他 Agent 的批准记录也不被接收。
- 后续即使 `Check` 成功，仍需**再检查有效 Controller Grant、任务幂等、资源独占、Compose 行为、受控根目录和运行时密钥/数据保留**。当前绝不调用 Shell/Docker。

## 本机命令示例

以下命令均在**运行 EasyHostAgent 的主机可信终端**执行（包括相同的 `EASYHOSTAGENT_DATA_DIR` 配置）。仅为展示参数，不代表已完成批准：

```bash
# 缓存成功后，先通过独立可信渠道核对所有值，不能只复制 Git HTTP 的自报说明
easyhostagent -approve-bundle "<zip_sha256>" \
  -source-id "<source_uuid>" -app-id "sub2api" -git-revision "<40位git_commit>"

# 定期复核及手动撤销；均不涉及运行 Shell
easyhostagent -check-bundle "<zip_sha256>"
easyhostagent -revoke-bundle "<zip_sha256>"
```

这些是本地 CLI，**不经过控制端 HTTP API**。当包不存在、ZIP 元信息不可信、Git Commit 不匹配或批准记录损坏时返回错误。重复批准原内容保持幂等，不重写批准时间。

## 剩余研发门禁

1. 完成独立可信发布者签名/公钥信任模型或明确的“仅本机手动批准”运营策略，开展可信来源及更新时审批体验评审。
2. Task API 与执行器必须在**执行起点**调用 `Check` 并同时核验 Controller Grant、操作类型、资源占用、安装根目录、生命周期安全和持久化任务状态；不允许用“上传/批准成功”自动创建运行任务。
3. 真实私有 Deploy 的完整脚本/Compose 语义对照、Sub2API 在 fnOS/Linux 实机安装健康验收仍未完成。
4. 审批、撤销与未来执行任务之间需明确并发撤销语义和审计日志，防止检查后因竞争窗口继续执行。
