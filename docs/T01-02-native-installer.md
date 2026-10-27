# T01-02 — FnOS/Linux 原生安装器、systemd 与数据保留

> 执行日期：2026-10-27
> 状态：**DONE（安装器源码、模拟根目录执行测试与 CI 成功；FnOS 实机尚未验证）**
> 源码仓库：`CiBeDerORG/EasyHostAgent` `main`
> 核心流水线：[FnOS Linux Installer Tests #40424901906](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40424901906) — **success**。
> 对应任务：[`task-plan.md`](./task-plan.md) T01-02。

## 1. 本轮实际变更

| 文件 | 内容 |
|---|---|
| [scripts/install.sh](../scripts/install.sh) | root/systemd 前置校验、Linux amd64/arm64 识别、本地 binary 安装、带 SHA-256 校验的指定 GitHub Release 下载、首次创建机密环境配置/数据目录、按需 enable/restart |
| [scripts/uninstall.sh](../scripts/uninstall.sh) | 验证安装归属标记、停止/禁用服务、删除二进制和 unit，但永久保留 Agent 配置/身份/应用数据 |
| [packaging/systemd/easyhostagent.service](../packaging/systemd/easyhostagent.service) | Go 原生 systemd 服务、root 用户、启动失败重启、日志由 journald 接收、UMask=0077 |
| [tests/installer/test_install.py](../tests/installer/test_install.py) | 11 项 Python 标准库测试，在隔离根目录模拟安装/重装/卸载/下载 SHA 校验，另验证真实 Go 二进制 |
| [.github/workflows/installer-tests.yml](../.github/workflows/installer-tests.yml) | bash -n、编译 Go 程序、运行模拟根目录系统安装测试 |
| [README.md](../README.md) | 编译测试产物的手动离线安装、未来指定 Release 安装、数据保留、systemd 管理与限制 |

本任务没有增加 Agent 的 HTTPS 端口、配对或 Docker API。没有改动 EasyTier、Deploy 的运行代码。

## 2. 安装模式与命令

当前 **GitHub Releases 为空**，不能声称已有正式可用的一键下载版本。临时使用 T01-01 / 近期 Go CI 编译出的 Linux AMD64/ARM64 测试产物，部署时由用户明确选对机器架构，并提供已经解压且可执行的文件：

```bash
git clone --depth 1 https://github.com/CiBeDerORG/EasyHostAgent.git
cd EasyHostAgent
chmod +x /absolute/path/easyhostagent-linux-amd64
sudo bash scripts/install.sh --binary /absolute/path/easyhostagent-linux-amd64
sudo systemctl status easyhostagent.service --no-pager
```

ARM64 使用相应可执行文件；发布正式 `vX.Y.Z` 后：

```bash
sudo bash scripts/install.sh --release vX.Y.Z
```

远程发布包必须包含 `easyhostagent-linux-${arch}` 与 `easyhostagent-linux-${arch}.sha256`，其中 checksum 内容为标准 `SHA256  filename` 单行格式。下载和 SHA-256 校验失败则在**替换现有程序前终止**。SHA-256 与发布资源来自同一渠道，验证文件一致性但不提供独立签名真实性保证。

特别注意，安装脚本依赖同一仓库中的 `packaging/systemd/easyhostagent.service`，所以需要克隆/获取完整安装器资源；目前不能只 `curl | bash` 下载单个安装脚本。

## 3. 系统文件和数据安全

| 目标 | 默认值/行为 |
|---|---|
| 可执行文件 | `/usr/local/bin/easyhostagent`，0755 |
| systemd unit | `/etc/systemd/system/easyhostagent.service`，0644 |
| 环境配置 | `/etc/easyhostagent/agent.env`，0600 |
| 安装归属标记 | `/etc/easyhostagent/managed-by-installer`，0600 |
| Agent 持久状态目录 | `/var/lib/easyhostagent`，0700（可初次自定义） |
| 默认 `SOFTWARE_ROOT` | FnOS `/vol3/1000/software`；普通 Linux `/opt/software`（可初次覆盖） |
| 日志 | Go slog JSON stdout，systemd/journald 捕获 |
| systemd 重启策略 | 仅进程异常退出重启（`Restart=on-failure`），不是应用自动回滚 |

- 重装同一个程序时不修改现有 `agent.env`，不丢 `/var/lib/easyhostagent` 下的任何文件（未来 Agent ID、TLS 证书、配对令牌将放在持久状态目录）。
- 更新候选文件必须先成功执行 `-version`；检验失败就拒绝替换旧二进制。这是安装**预检**，不是应用自动回滚。
- 如果 systemd 启动失败，保留文件和现场，脚本报告错误、提示 `journalctl -u easyhostagent.service`，**不自动回滚旧程序**。
- 卸载先要求安装归属标记匹配，只删对应 binary/unit/marker；保留现有 `agent.env`、Agent 目录、所有应用根目录、Docker 容器与卷。脚本没有“彻底删除数据”的选项。
- 未来需要 Docker 宿主机管理权限，当前 unit 选择 root 用户，并仅启用初步 systemd 保护；应限制宿主机安装权限，仅在可信环境执行。

## 4. 验收记录

GitHub Actions [#40424901906](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40424901906) 已实际完成：

- [x] `bash -n` 校验 `install.sh` 与 `uninstall.sh`。
- [x] CI 中以真实 Go 源码成功编译候选 Agent 程序。
- [x] **11/11** Python unittest 全通过，真实 Go 程序的 `-check-config` 测试通过。
- [x] 用 staging root 安装程序、unit、0600 配置和 0700 状态目录。
- [x] 重装时旧状态目录内的模拟 Agent ID 与旧环境配置保持不变。
- [x] 卸载仅移除 service/binary/marker，Agent 数据及配置原样保留。
- [x] 不受管理的目标服务文件不会被覆盖，已有异物不会被误卸载。
- [x] 模拟固定 GitHub Release 文件下载和 SHA-256 校验；篡改 checksum 阻止安装且原二进制不受影响。
- [x] 损坏候选二进制的 `-version` 预检会拒绝替换。
- [ ] **真实 FnOS 上执行 `systemctl enable/restart` 未测试**，没有提供目标机器登录能力；首次 FnOS 部署由 T01-08 端到端验收补齐。
- [ ] 真实 GitHub Release 安装未测试，因为仓库当前没有 release；正式签署/发布/校验由 T06-06 接续。
- [ ] Go HTTPS 服务及一次性配对尚未实现，属于 T01-03/T01-04，不应把本次安装器称为已可连接 EasyTier 的 Agent。

## 5. 下一步

**T01-03 — Agent 持久身份、HTTPS API 与授权基础。**

优先实现固定且安全持久的 Agent UUID、TLS 证书/指纹、按需 `/v1/health`、`/v1/identity`，避免开启没有认证保障的远程命令执行。一次性配对与授权在 T01-04 执行，远程 Docker 操作更后续。

T01-02 之后的所有安装及更新动作，继续遵守用户最终确认的“**不自动回滚；失败保留现场及日志**”原则。
