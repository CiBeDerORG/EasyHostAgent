# T01-07 — 按需主机状态及 SOFTWARE_ROOT

> 2026-10-28。目标仓库：EasyHostAgent main、EasyTier feat/automation-center。
> 当前状态：**IN_PROGRESS**（Go Agent CI 已通过；EasyTier Rust / Vue 主构建及真实 FnOS 联调待验收）。

## 代码交付

### Agent (Go)
- 增加 `GET /v1/host` 和 `PATCH /v1/host/settings`，两个接口均复用已配对的控制端 Bearer + Agent ID 校验；没有未授权的主机资源接口。
- CPU 核心数、Linux `/proc/meminfo`、`statfs` 磁盘剩余、Docker Unix socket 的 Engine / Compose 版本按需读取；不执行后台同步、不使用任意网络 Docker Host。
- Agent 的默认安装目录按 `config.Load` 的 Linux/FnOS 系统规则确定。用户修改存储为 `EASYHOSTAGENT_DATA_DIR/host-settings.json`，0600 权限、同目录临时文件原子替换、重启保留。
- 更改根目录时拒绝相对路径、文件系统根、符号链接、无效路径、与状态目录重叠；`dataDir/managed-apps/` 非空时返回 HTTP 409。后续 M4 安装任务必须**在执行部署前**为受管实例建立持久目录记录，不能仅依赖 UI 禁止改根。
- 永不迁移/删除原目录内容；现有应用迁移应由用户完成并验证后，在受管记录解除占用时重新修改。

### EasyTier (Rust/Vue)
- 绑定的现有服务器设备额外提供 `/agent/host` 只在用户打开页面时查询真实 Agent；`/agent/host/settings` 由服务端转发，不将 Token、IP 或控制端凭据交给浏览器。
- 使用数据库中保存的 pin / 已验证的 endpoint / 密文解密 Bearer。对返回 Agent UUID、响应头、必要主机字段逐项核验。
- 新表 `asset_agent_host_snapshots` 保存**上次成功**的快照及 Agent ID，手动查询遇到网络故障只提供标注离线的旧快照，绝不当作实时在线、也不会删除应用或服务。
- `ServerAgentHostPanel.vue` 在成功绑定的服务器设备入口展示资源和软件目录，支持手动查询、修改，离线/缓存明确区分。

## 验收进度

- [x] Go gofmt / vet / 单元测试 / race test / Linux amd64 和 arm64 编译通过：[Actions #40652122857](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40652122857)。
- [x] Go 的 TLS 受保护路由 / 配对鉴权 / 非法路径 / 已有应用占用 / settings 持久化测试已纳入仓库。
- [ ] EasyTier 新 REST 接口、Vue 页面、数据库迁移与 native build 的 CI 最终通过。
- [ ] Go↔Rust 跨进程 TLS 配对后主机资源查询和修改的真实 E2E。
- [ ] FnOS 的 Docker socket/CLI 路径、默认软件目录、已安装应用阻断现场验证（T01-08）。

## 非范围

不添加后台心跳、周期自动刷新、Docker 容器清单、安装执行器、自动目录迁移、自动回滚或把普通 Docker 容器直接登记为受管应用；这些分别属于 M2、M4、M5。
