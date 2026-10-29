# T03-08 — 受控部署包接收、验证与不可变保留

> 新开发分支 `feat/server-app-lifecycle`；三仓库 EasyTier/EasyHostAgent/deploy 同名分支。2026-10-29。
> 代码已提交，**Agent Go 竞态测试和 amd64/arm64 编译已通过**：[CI #40809855033](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40809855033)。
> EasyTier 同名新分支的 CI 正在执行；真实 FnOS/端到端联调尚未执行，T03-08 仍是 IN_PROGRESS。

## 真实业务链路

1. EasyTier 管理员在中央商店**手动刷新** Deploy，只读克隆固定 `main` Git Revision。
2. EasyTier 生成 `<app-id>-<40hex-git-sha>.tar`：限量的 `bundle.json`、`manifest/app.yaml` 和 manifest 列明的必要文件。bundle 包含每个源文件的 SHA-256、目标路径、大小、权限及 `allowed_actions`（script install/update 或 compose_down uninstall）。
3. 已登录并被授权访问某台**既有服务器资产**的操作者调用 EasyTier：
   `POST /api/v1/deploy/catalog/{app_id}/transfer/{device_id}?owner_user_id=N&machine_id=...`
   不接受任意 URL、包路径、shell 命令或配置/密码内容。
4. EasyTier 必须从数据库验证这个服务器设备归属、有效配对身份和已存储的 TLS SPKI 指纹。使用加密存储的特定设备 Bearer Token，通过 pinned TLS 1.3 将固定包传给 Agent，严格验证返回的 Agent-ID 与包回执。
5. Agent 仅将验证通过的压缩包保存至私有 `EASYHOSTAGENT_DATA_DIR/deploy-packages`，留存 `SHA256.tar` 和 `app-id-commit.json`。应用从商店下架不删除已经接收的原始包。相同版本相同内容重复上传是幂等的；同版本不同内容返回 409，绝不覆盖。
6. **目前不解压、不执行脚本、不操作 Docker、不写用户的 `.env`/`.secrets`，也不授予任何 managed 权限**。安装任务属于 M4，需要再验证任务授权和包摘要，不能把已上传当作已安装。

## Agent HTTPS API

| 方法 | 路径 | 权限 | 返回 |
|---|---|---|---|
| POST | `/v1/deploy/packages` | 现有配对控制端 Bearer、X-EHA-Agent-ID、X-EHA-Controller-ID、TLS 1.3 | 201 新包 / 200 完全相同重试，返回只读 Receipt |
| GET | `/v1/deploy/packages/{app_id}/{git_revision}` | 同一配对控制端 | 200 已保存包回执；不存在 404 |
 
POST Content-Type 为 `application/x-tar`，必须携带 `X-EHA-Package-SHA256: <64 lowercase hex>`，正文是固定字节 tar 包。请求大小最多 26 MiB，单一包文件最多 5 MiB，清单最多 128 KiB，元数据最多 256 KiB，最多 128 个被打包文件。

主要响应错误：400 Digest 格式错误；401 未配对/错误控制端；413 体积超限；415 类型不支持；422 SHA、内容或契约不合法；409 版本冲突或已保存内容被篡改；503 私有包目录不可用。以上错误不回显私有磁盘路径、token、脚本内容或密码。

## 安全规则及保存语义

- `archive/tar` 中只接受**普通文件**，拒绝符号链接、硬链接、目录、设备、重复文件和额外文件；归档成员必须与 `bundle.json` 完全匹配。
- 绝对路径、`..`、隐藏文件、无效分隔符和 `data/state/backup/runtime/workspace` 等保护目录禁止进入部署包；`.env`、`.secrets` 不得打包。软件根目录以外的脚本不接受。
- JSON 解码拒绝未知字段及尾随多个 JSON 对象；`format_version=1`、应用 ID、固定 Git Revision 和摘要必须是正确格式。安装、更新只接受 `script` 策略，卸载只接受 `compose_down`。动作脚本必须随包以可执行 0755 模式提供，Compose 文件也必须在清单中。
- 校验整体 tar SHA-256、原始 manifest SHA-256、每个文件 SHA-256/长度/权限；验证后使用私有目录 0700、文件 0600、独占硬链接提交，避免覆盖同名已保存版本。
- Agent **不连接 GitHub**，无需 Deploy Git Token；不接收浏览器直接上传（只接受配对控制端）。
- 这是一套摘要及授权验证，不是非对称数字签名：固定 Git 来源信任根仍是 EasyTier 的已配对控制端。M4 执行前必须重新验证持久文件、动作策略、用户/设备归属和运行环境。

## 回归验收

Agent `go test -race ./...` 覆盖合法包、损坏摘要、篡改 Shell 文件、未知动作、路径穿越、特殊成员、重复文件、多余文件、过大文件、已存包篡改、相同版本不可覆盖，以及实际 TLS 配对双控制端隔离和幂等上传/回执查询。

- [x] Go 原生 CI 格式、vet、race 通过。
- [x] Linux amd64 和 arm64 静态编译成功。
- [ ] EasyTier 新分支 Rust 原生/Web/镜像 CI 完整通过。
- [ ] 真实 EasyTier 通过 pinned TLS 向 FnOS Agent 传输 Portainer/Sub2API 包，现场验证无执行副作用。
- [ ] 部署任务与数据归属安全边界（M4，不计作本项完成）。

**下一任务**：先核实 EasyTier `feat/server-app-lifecycle` CI，有错误先修复；随后进入 M4 T04-01 SQLite 持久化任务和状态机，保持“失败保留现场、无回滚”规则。
