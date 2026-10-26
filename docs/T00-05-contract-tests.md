# T00-05：API / Deploy / SQLite 最小契约测试样本

> 日期：2026-10-27
> 状态：**DONE（测试样本、可执行测试脚本、两仓库 CI 工作流已交付；Deploy 的托管 Runner 执行仍受阻）**
> 范围：不改 EasyTier 业务、Go Agent 或真实 Docker 应用；不添加自动回滚。
> 关联任务：[task-plan.md](./task-plan.md) T00-05。

## 一、交付清单

| 仓库 | 文件 | 内容 |
|---|---|---|
| EasyHostAgent | [tests/contracts/test_api.py](../tests/contracts/test_api.py) | OpenAPI schema/安全请求头/7 份 JSON 示例、三种 Docker 清单、失败清单和非法任务动作 |
| EasyHostAgent | [tests/contracts/test_sqlite.py](../tests/contracts/test_sqlite.py) | 用 SQLite 内存库建立最小 legacy 父表，执行 **整份** [T00-04-proposed.sql](./schema/T00-04-proposed.sql)，测试外键、唯一性、归档、解绑 |
| EasyHostAgent | tests/contracts/fixtures/portainer.json | 真实端口语义：Portainer 单容器 Compose |
| EasyHostAgent | tests/contracts/fixtures/sub2api.json | Sub2API + PostgreSQL + Redis 三容器 Compose |
| EasyHostAgent | tests/contracts/fixtures/standalone.json | 无 Compose 项目的独立 nginx 容器 |
| EasyHostAgent | tests/contracts/fixtures/docker-error.json | Docker 查询失败：resources 为空但 complete=false |
| EasyHostAgent | [tests/contracts/README.md](../tests/contracts/README.md) | 完整离线运行命令 |
| EasyHostAgent | [.github/workflows/contract-tests.yml](../.github/workflows/contract-tests.yml) | 自动执行 Python unittest，依赖 jsonschema |
| Deploy（私有） | [tests/contracts/test_manifest_v1.py](https://github.com/CiBeDerORG/deploy/blob/main/tests/contracts/test_manifest_v1.py) | JSON Schema、Portainer/Sub2API YAML 示例、Compose 项目/服务/端口、真实依赖文件、数据挂载、env/secrets 来源与拒绝不安全输入 |
| Deploy（私有） | [.github/workflows/app-manifest-contracts.yml](https://github.com/CiBeDerORG/deploy/blob/main/.github/workflows/app-manifest-contracts.yml) | 自动执行 YAML/JSON Schema Python unittest |

## 二、已验证的场景

### EasyHostAgent（14 项 unittest 方法）

- [x] OpenAPI 中各模型可由 JSON Schema 2020-12 解释
- [x] 每个 API 操作含 2xx 响应、唯一 operationId、路径参数以及已鉴权请求头
- [x] 文档中的 7 组 JSON 请求/响应示例符合相应模型
- [x] Portainer 单容器 Compose 识别样本
- [x] Sub2API 三容器 Compose 识别样本
- [x] 独立 Docker 容器识别样本
- [x] Docker 查询失败不能视为资源已经删除
- [x] 非法 Shell 执行动作被 schema 拒绝；短配对码被 schema 拒绝
- [x] 将**完整拟议 SQL** 在简化 legacy 父表内实际执行，PRAGMA foreign_keys 与 foreign_key_check 正常
- [x] 删除有服务的资产被 guard 拒绝；归档+解绑后应用、服务、历史任务仍存在
- [x] 错误所有者关系、重复应用、重复 Agent identity 均被约束拒绝
- [x] 同一个应用的第二个主服务被拒绝，仍可附加非主服务
- [x] 独立删除关联服务只移除相应链接，不删除应用实例
- [x] 直接硬删除 Host 被限制，避免级联丢失历史任务

**GitHub Actions 已确认实际执行成功：** [EasyHostAgent 合约测试 Run 40386121798](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40386121798)；job 的 checkout、Python 安装及测试步骤均显示 success。

### Deploy（6 项 unittest 方法）

- [x] 已**创建** JSON Schema 本身、两个 YAML 示例、文件路径/动作依赖、Compose 服务与真实全局配置模板的本地 Python 断言。
- [x] 通过 GitHub 读取当前 Deploy 文件并单独运行 JavaScript 静态检查器（主流 JSON Schema 关键字、依赖声明、环境变量和端口关系），Portainer/Sub2API 两个样本结果 **0 错误**。
- [x] 检查中发现 Sub2API 示例 SUB2API_ADMIN_EMAIL 错误标为普通 .env 字段；实际 deploy/.secrets.example 包含此项，已修正示例为 secret 类型、target secrets。commit d46c2947。
- [ ] **Deploy 的 GitHub Actions 没有成功执行测试命令**：[Run 40386319978](https://github.com/CiBeDerORG/deploy/actions/runs/40386319978) 首次和一次手动重跑都失败，job 均无任何 steps 记录；下载 job log 返回 404 BlobNotFound。本次不能宣称 Deploy Python 运行时测试已通过。之前 Deploy 的其他 workflow 也存在“job failed + 0 steps”现象，原因尚未确定。
- [ ] 待 Deploy Runner 可运行后，必须以该工作流的 Python unittest 实际成功结果解除此 CI 阻塞。

## 三、离线复现方式

EasyHostAgent（依赖首次安装时需联网；安装后测试过程完全离线）：

~~~bash
python3 -m pip install "jsonschema>=4.20,<5"
python3 -m unittest discover -s tests/contracts -p "test_*.py" -v
~~~

Deploy（在 Deploy 私有仓库本地克隆目录执行）：

~~~bash
python3 -m pip install "PyYAML>=6,<7" "jsonschema>=4.20,<5"
python3 -m unittest discover -s tests/contracts -p "test_*.py" -v
~~~

这些脚本不连接运行中的 Agent、Docker 或 EasyTier；不可能改动真实主机容器。

## 四、下一任务和 M0 交付边界

- M0 五项设计与测试样本**已交付**：T00-01 审查、T00-02 HTTP API、T00-03 Deploy manifest、T00-04 数据关系、T00-05 合约样本与执行脚本。
- **不能误报为全链路 CI 绿灯**：EasyHostAgent 合约 CI 已通过；Deploy CI Runner 无步骤失败仍需排查和复测。
- 下一项开发 **T01-01**：初始化 Go Agent 工程骨架（cmd/internal、版本/配置/结构化日志、amd64/arm64 编译）。不提前实现配对、Docker 和应用安装业务。
- 真实数据库 SeaORM 迁移、HTTP Go/Rust 实现和 FnOS 宿主机集成测试属于后续开发任务，T00-05 的 SQLite fixture 不替代生产迁移验证。
