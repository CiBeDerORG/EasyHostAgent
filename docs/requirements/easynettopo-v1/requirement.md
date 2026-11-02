# R001 · EasyHostAgent 作为 EasyNetTopo V1 受管执行端（汇总需求）

> 整编日期：2026-11-02。现行控制端定位以 [EasyNetTopo V1.0 规格](https://github.com/CiBeDerORG/EasyNetTopo/blob/main/docs/requirements/v1-platform-scope.md) 与本仓库已合并的 M0/M3/M4 契约为依据。**旧 [requirements-plan.md](../../requirements-plan.md) 以 EasyTier 为控制端，是历史方案，不应覆盖现行 V1**。本规格是新一代控制端协作的需求入口，不代表已进行 FnOS 真机验收。

## R001 分级需求

| 需求编号与名称 | 父级 | 确认状态 | 交付状态 | 约束/核验 |
| --- | --- | --- | --- | --- |
| R001 · EasyNetTopo 受管 Agent（汇总） | — | 已确认（V1 协作方向） | 进行中 | 实际端到端仍待验收 |
| R001.01 · 稳定主机身份和 TLS 直连 | R001 | 已确认 | 待验收 | 通过 IP:port 访问，可信端核验 TLS 指纹 |
| R001.02 · 一台 Agent 唯一有效控制端 | R001 | 已确认 | 待验收 | 一次性码配对，成功重配撤销旧授权；失败不改变旧授权 |
| R001.03 · 主机能力与 Docker/Compose 发现 | R001 | 已确认 | 待验收 | 包含停止容器；不完整查询不等于删除 |
| R001.04 · 不可变部署包接收与本机信任核验 | R001 | 已确认 | 待验收 | SHA-256、资源白名单、本机可信批准；不受外部 source_id 自称影响 |
| R001.05 · 任务持久化、幂等、状态与日志 | R001 | 已确认 | 待验收 | 请求 UUID/摘要冲突处理；Agent 本地存储，断网不重复执行 |
| R001.06 · 资源归属预检与保护 | R001 | 已确认 | 待验收 | Compose、端口、根目录、历史数据、密钥与冲突防护 |
| R001.07 · 受控 Sub2API 安装执行 | R001 | 已确认 | 待验收 | 仅授权脚本/Compose，验证 sub2api/postgres/redis 健康 |
| R001.08 · 生命周期、恢复与真实设备验收 | R001 | 已确认 | 待验收 | 更新保留配置/数据，失败保留现场，FnOS/Linux 实测 |

## 边界和来源

1. EasyHostAgent 是独立的 Go 原生主机服务，不读取私有 Deploy Git 凭据，不实现 EasyTier 的网络控制，也不接受任意 Shell。
2. 唯一控制端、配对、Docker snapshot、bundle/tasks 协议以本仓库 [OpenAPI v1](../../api/openapi-v1.json) 与实际 Go 代码为准；协议文本不自动代表端点全部可用。
3. 最新 M3/M4 设计材料：[不可变包](../../m3-immutable-bundles.md)、[本机批准](../../m3-local-bundle-approval.md)、[持久任务准入](../../m4-persistent-task-admission.md)、[资源预检与恢复](../../m4-resource-preflight-recovery.md)；实际行为需以测试和运行证据验证。
4. 历史以 EasyTier 为中心的 `R-01`～`R-xx`、`T00-xx`～`T06-xx` 保留原文以维护可追溯性，**不把历史 DONE 直接映射为本 R001 已验收**。跨仓控制端语义变更应独立核查兼容和迁移。

## 汇总与验收

各有效子需求业务方向已确认，所以汇总需求“已确认”；子需求尚无本次真实业务验收证据，**交付状态只能是进行中**。技术任务的状态与完成率以 [T001 权威任务文件](tasks.md) 为准，需求整体验收以 FnOS/Linux 的实际操作和安全负例为准。
