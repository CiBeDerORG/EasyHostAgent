# T01-08 — FnOS 主机接入端到端现场验收

> 状态：**BLOCKED / 需要真实 FnOS 主机**。2026-10-28。不会因为 CI 成功而虚报真实联调完成。
> 前置：T01-01～T01-07 代码已提交，最新 Go CI #40652794807、EasyTier CI #40652667024 均成功。

## 验收环境
- 一台真实 FnOS（记录 `uname -m`、`cat /etc/os-release`、`systemctl --version`）。
- 已将同一台 FnOS 作为 EasyTier 资产设备登记，语义类型包含 `server`。
- 一台部署了 `feat/automation-center` 的 EasyTier 管理端；不能使用假数据冒充 FnOS。
- 可由 EasyTier 后端访问 FnOS 保存的局域网或组网 IP 的 TCP 7014，管理端必须从可信 FnOS 终端获取 Agent 的 TLS SPKI SHA-256。

## 手工验收记录（每条应保存真实日志/截图与时间）
- [ ] **A：原生安装**。取已验证 amd64/arm64 Agent 产物，执行 `scripts/install.sh --binary /绝对路径/产物`；`systemctl status easyhostagent` 为 active，`journalctl -u easyhostagent` 无异常。确认默认 FnOS 软件目录。
- [ ] **B：证书身份**。本地执行 `sudo /usr/local/bin/easyhostagent -identity` 两次及重启服务后再次执行，Agent UUID、SPKI 指纹完全相同。证书指纹只能从可信本地读。
- [ ] **C：配对**。本地执行 `sudo /usr/local/bin/easyhostagent -pairing-code`。EasyTier 现有服务器设备页选择已保存 IP、核对指纹、点击检测与绑定，确保不产生重复主机。错误指纹/错误配对码应被拒绝，成功后重复使用配对码必须失败。
- [ ] **D：按需资源**。主动打开服务器 Agent 面板并查询；核对 CPU、内存、磁盘、Docker Engine/Compose 状态与宿主机当前输出，采集时间为本次请求，不存在后台心跳。
- [ ] **E：目录配置**。在无受管应用时修改 `SOFTWARE_ROOT` 后重启 Agent；重新按需查询依旧为保存值。测试相对路径、符号链接、与 Agent 状态目录重叠被拒绝。
- [ ] **F：占用阻断**。使用正式受管实例登记（M4 接入后）确认存在应用时禁止直接修改软件根目录，旧数据不丢失；这一项依赖 M4 受管实例登记能力，不能只创建测试文件就宣称产品验收。
- [ ] **G：离线缓存**。Agent 停止或阻断 7014，点击手动刷新展示旧快照、明确“非实时/离线”，禁止修改，不执行自动删除；恢复后手动刷新回到实时状态。
- [ ] **H：IP 变化/旧授权失效**。主机改变已登记 IP 后，必须人工重新检测同一 UUID 和 SPKI，验证成功才更新端点；用新的配对码重新配对后旧 Bearer 无效。删除资产只解绑管理端，不卸载物理程序或 Docker 应用。

## 自动化补充（不等于真机验收）
- Go 使用真实 TLS 的 HTTP 测试、配置持久化测试、权限测试已在 #40652794807 通过。
- Rust/Vue 构建、native 回归及 GHCR 发布 #40652667024 通过。
- **缺口**：Go ↔ Rust 两个独立服务 + FnOS systemd 的真实交互、离线/重新绑定、不同系统 Docker 路径仍未执行。

## 完成门槛
只有 A～H 的适用步骤在真实主机通过并附证据时，才能把 T01-08 标为 DONE；在 M4 尚无受管应用登记前，F 项必须后补复核。不能因为无现场环境而卡住不依赖真机的 M2/M3 代码开发。
