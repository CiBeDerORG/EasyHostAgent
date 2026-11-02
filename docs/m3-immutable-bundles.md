# M3-01：不可变部署包接收与缓存（ZIP v1）

> **本阶段只有上传、验证和缓存，绝不解压到 SOFTWARE_ROOT、运行脚本或生成安装任务。**
> 对应 [Agent OpenAPI v1](api/openapi-v1.json) 的 `HEAD/PUT /v1/bundles/{sha256}`。

## 接口

- `HEAD /v1/bundles/{sha256}`：校验该 SHA-256 对应的现存文件完整性；存在且可验证时返回 HTTP 204，缺失返回 HTTP 404，存储损坏返回 409。
- `PUT /v1/bundles/{sha256}`：`Content-Type: application/octet-stream`，最大 **64 MiB**；流式读取并比对请求路径中的小写 SHA-256，与后端存储的原始字节完全一致。
- 第一次上传返回 **201**，完全相同且已经有效落盘时返回 **200**。响应：`agent_id`、`sha256`、`size_bytes`、`already_exists`。
- 无论 HEAD 或 PUT 都经 `auth.RequireController` 验证 Bearer Token + `X-EHA-Controller-ID` + `X-EHA-Agent-ID`，并要求真实 HTTPS。
- 目录默认在 `<EASYHOSTAGENT_DATA_DIR>/bundles/`，权限为 0700，缓存文件权限为 0600；不会写到应用安装目录。

## ZIP v1 文件规则

部署包是普通 ZIP 文件，不是任意脚本字节流、TAR 或 GZIP。当前只接受 Store/Deflate 的**普通文件**与安全目录，最多 1024 条记录：

- 禁止绝对路径、路径穿越、空路径段、反斜杠、隐藏段、冒号、特殊字符；拒绝符号链接、设备文件和重复（包括大小写不同的）路径。
- 不允许 `.env`、`.secrets`、运行时数据等敏感目录和文件出现在包中。
- 单个普通文件声明解压后不得超过 64 MiB，累计声明/验证解压量不超过 256 MiB。
- 读取并校验每个普通文件的解压内容和 CRC，但**从不把 ZIP 文件条目落到磁盘**。
- 原始包通过 64 MiB 上限、SHA-256、ZIP 校验后才落盘，采用本目录临时文件写入、`fsync` 和不可覆盖的硬链接固定到最终摘要路径。
- 对现有损坏/符号链接/不匹配 SHA-256 的缓存绝不自动覆盖或“修复”；需人工调查和维护。

## 尚未解决的高权限信任

**SHA-256 只证明收到的字节与给定摘要一致，不证明其源于可信发布方。**

- Agent 当前的 Bundle Cache 不解析来自 Git 的 `source_id`、签名或批准记录。用户上传了包 **不意味着可执行**。
- 未来 `POST /v1/tasks` 的 Application 动作必须在执行前，重新验证固定来源/包内容/受控脚本批准或发布签名，并阻止第三方 Manifest 伪造“官方”权限。
- Bundle 一旦上传，不自动解压，不覆盖现有 `/software` 等目录，不调用 Docker，也不创建正在运行的任务。
- 有效 Bundle 存在不代表可安装。Deploy Sub2API 的 app.yaml/schema/包文件清单是控制端的额外校验要求，并不由本阶段上传 API 代替。
- 不实现包清理、过期管理、离线队列和自动执行。缓存容量治理须在正式发布前单独评审，避免任意长期积累导致磁盘耗尽。

## 自动化测试与交付限制

新增测试覆盖 Token 鉴权、HEAD 404/204、PUT 201/200、摘要不匹配、恶意路径/符号链接/错误压缩格式、重复文件、超量读取、缓存损坏及再次上传拒绝。

CI 中的单元测试并非 fnOS 真机验证。正式实施远程任务前还需完成可信执行授权、资源冲突判断、持久化任务状态和真实 Sub2API 安装健康测试。
