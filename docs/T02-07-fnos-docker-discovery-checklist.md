# T02-07 FnOS Docker 发现真实验收清单

> 2026-10-28；此文档是**现场执行说明**，不是当前会话的真机验收结果。
> 自动化虚拟 Socket 测试已提交，并由 Go Actions #40666918325 验证通过。
> 真实 FnOS 需要实际可用的主机与授权控制端；未运行的项目一律保持 BLOCKED。

## 前置条件

- 使用经过允许的测试 FnOS 主机，并安装/配对原生 EasyHostAgent（M1 T01-08）。
- 事先准备**专门的测试容器**（例如 Portainer 单容器 Compose、Sub2API API + PostgreSQL + Redis 三容器 Compose、一个非 Compose 独立容器）。不可对生产容器随意 stop/rm。
- 在 FnOS 本地命令行检查 `sudo docker version`、`sudo docker ps -a` 与 `sudo docker compose ls -a`。以上命令只读；确认应用均实际存在后再继续。
- 在 EasyTier 的原有服务器设备里检查 Agent UUID、固定 TLS 公钥指纹与绑定状态；不能新建第二套 Host 主数据。

## 应执行并截图留存的场景

| 编号 | 场景 | 预期行为 | 自动化覆盖 |
|---|---|---|---|
| F01 | 单一 Portainer Compose 项目 | 单个 Compose 卡片；保持项目 ID，不拆散容器 | Go T02-07 |
| F02 | Sub2API + PostgreSQL + Redis 三容器 | 同一个 Compose 项目、三个成员，逐个有状态；端口独立显示 | Go T02-07 |
| F03 | 两个不同 Compose 项目、相同镜像 | 分成两个项目，绝不凭镜像合并 | Go T02-07 |
| F04 | 两个独立容器使用相同镜像/名称 | 两个独立资源，不创建虚构项目 | Go T02-07 |
| F05 | 只停止一个 Compose 成员 | 整项目为 partial；停止成员仍在列表 | Go T02-07 |
| F06 | 所有成员停止但没有删除 | 项目为 stopped；**不生成缺失或服务删除候选** | Go + Rust |
| F07 | 人工连续点击查询四次 | 项目/资源不会重复生成；位置顺序稳定；缓存保持单行 | Go + Rust |
| F08 | 断开 Agent 网络，再查询 | 展示同 Agent UUID 的历史快照，标注离线且只读；禁止接管 | Rust DB + 需实机 |
| F09 | Docker daemon 短暂不可用、API 不完整 | 保留最后成功的快照；缺失计数不增长，不清理服务 | Go + Rust DB |
| F10 | 用户从外部卸载**可丢弃的测试项目** | 首次完整扫描为“待复查”，下一次完整扫描为“确认缺失”；所有 `cleanup_allowed=false`，服务/公网映射不动 | Rust DB + 需实机 |
| F11 | 可丢弃测试项目重新出现 | 取消缺失候选，不遗留“已删除”标志；不得由缓存生成冲突 | Rust DB + 需实机 |
| F12 | 两台 FnOS 主机同 Compose 项目名称 | 按 Agent UUID + 资产设备 ID 分隔，不会相互覆盖 | Go + Rust DB |
| F13 | 用户确认接管、错误应用 ID、重复提交 | 未确认的请求拒绝；同主机相同应用/资源不能双重占用；意向不提升至 managed | Rust + 需实机 |
| F14 | 解绑、换 Agent UUID 后再次配对 | 旧历史快照不能作为新 Agent 实时数据，旧缺失证据不得复用 | Rust DB + 需实机 |
| F15 | 查看已有人工服务与公网映射 | 即使 Docker 检测到资源不见也不删用户服务/端口转发；直到 M4/M5 证明独占归属 | Rust DB + 需实机 |

## 验收证据

真实主机测试时留存：EasyHostAgent commit 与二进制版本、EasyTier Docker 镜像 digest、Agent UUID（可打码）、Docker API 版本、Compose 版本、每次按钮查询时间、Compose/容器前后状态截图、最后一次成功快照时间、离线缓存/缺失候选、服务及公网端口映射保持情况。**不得上传配对码、Bearer、JWT、密钥、数据库 .secrets 等。**

## 不可直接在生产机自动执行的动作

- 不自行执行 `docker rm`、`docker compose down`、`systemctl stop docker` 或修改生产容器状态。
- F10/F11 仅由授权人员操作明确可丢弃的测试资源；并将动作与现场证据记录在验收报告。
- M4 可信应用归属及 M5 关联服务/公网映射删除尚不存在，不能在本任务声称已通过“实际自动清理”验收。

## 当前结论

- Go 模拟 Engine T02-07 测试：已在 CI #40666918325 通过。
- EasyTier Rust 数据库回归：已提交并接入原生 CI，结果按最新 Actions 复核。
- FnOS / 整站 UI / 真实安全接管 / 外部资源删除现场验收：**BLOCKED — 当前没有授权的真实 FnOS 环境连接**。
