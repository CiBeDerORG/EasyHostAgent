# T00-03：Deploy `app.yaml` v1 协议标准化结果

> 日期：2026-10-27  
> 状态：**DONE — 标准、Schema、兼容示例和静态检查已提交；部署实现与 CI 校验器未开始**  
> 对应任务计划：[`task-plan.md`](./task-plan.md) T00-03。
> 责任：Deploy（标准源）+ EasyTier / EasyHostAgent（协议消费者）。

## 1. 本任务的实际改动

本次在**Deploy 私有仓库 `main`** 写入四个文件：

1. `schema/app-manifest-v1.schema.json`：JSON Schema Draft 2020-12，定义 14 个顶层属性及字段/执行/配置安全约束。commit `bb5e6627`。
2. `schema/examples/portainer.app.yaml`：Portainer 单 Compose 服务、普通 Web 端口与必填管理员密钥、保留数据和脚本执行契约示例。commit `4ef4ff07`。
3. `schema/examples/sub2api.app.yaml`：Sub2API+PostgreSQL+Redis 的 Compose 场景、9 个表单配置字段、4 个脚本生成密钥、共享镜像源脚本和完整健康验证。commit `8d380370`。
4. `docs/easyhostagent-app-manifest-v1.md`：字段含义、包依赖、真实 Deploy 路径兼容、配置所有权、应用数据保护、跨字段检查、安全边界及后续工作。commit `0aec3a7e`。

**没有修改** `portainer/deploy/install.sh`、`sub2api/deploy/install.sh`、`deploy.sh` 或其他实际安装行为；也没有擅自提前创建正式应用 `portainer/app.yaml`、`sub2api/app.yaml`（分别留给 T03-02/T03-03）。

## 2. 重要技术决策

| 方面 | v1 行为 |
|---|---|
| 应用适配范围 | Linux/FnOS 的 Docker Compose；本期不引入原生 systemd/host/openwrt 安装模式 |
| Source of truth | 中央 Deploy 私有仓库，由 EasyTier 按用户显式“刷新商店”才 git pull |
| Manifest 位置 | 目标应用目录的 `<app-id>/app.yaml`；示例暂放 `schema/examples/` |
| 应用 ID | 必须与 Deploy 顶层目录名称相同 |
| 部署文件 | `package.files` 列明 `source` (Deploy 仓库相对路径) → `destination` (SOFTWARE_ROOT 相对路径) |
| 文件依赖 | 必须将脚本引用的 `lib.sh`、`image-sources.sh` 等共享依赖显式列入包 |
| 配置 | 普通值 `SOFTWARE_ROOT/deploy/.env`、敏感值 `.secrets`，密钥不回显、不覆盖历史数据 |
| 配置增量 | 更新保留用户值，新字段采用模板默认值；脚本自生成型 secret 保留原值 |
| 服务端口 | 第一个 Web 端口优先，否则第一个其他端口；无端口则无自动服务；默认不公网公开 |
| 执行 | manifest 白名单 `install` / `update` 脚本及可选 `compose_down` 卸载，不提供任意命令 |
| 验证 | Compose 列明必要服务及 `running`/ `healthy` 成功条件 |
| 数据 | 明确保护 `data.paths` 与自有卷，卸载默认保留数据；彻底删除需二次确认 |
| 失败 | **标记失败，保留现场和日志，不做自动回滚** |
| 商店生命周期 | 已安装实例保存部署包版本，商店下架也可继续管理 |

## 3. 本次静态验证

- [x] 两份 YAML 示例用轻量 YAML 子集解析逻辑转为对象（不等同于全量 YAML 规范校验器）。
- [x] 由 JSON Schema 定义执行**主要关键字**的静态校验：类型、必填、枚举、正则、范围、额外字段、allOf/oneOf/if-then 等；无报错。
- [x] 两份示例均检查：配置 key 无重复，端口键引用存在且类型正确，`actions` 入口脚本/Compose 均列入部署包，目标路径不覆盖 secrets/data。
- [x] 使用 GitHub 当前真实文件校验 8 个 source 依赖均存在：
  `portainer/deploy/{docker-compose.yaml,install.sh,update.sh}`、
  `sub2api/deploy/{docker-compose.yaml,install.sh,update.sh,lib.sh}`、
  `lib/image-sources.sh`。
- [x] 将 manifest 变量和端口与实际 `*.sh`、`docker-compose.yaml`、`.env.example`、`.secrets.example` 比对；没有要求原脚本并不存在的 rollback 接口。
- [ ] 未安装并调用专业 JSON Schema/YAML 解析器、未触发 CI、未在 FnOS 上执行真实安装；以上归入 T03-01/T03-02/T03-03/T04-08，不误报通过。

## 4. 后续必须实现的内容

- **T03-01**：以 Deploy 私有仓库中的 `schema/app-manifest-v1.schema.json` 为唯一规范来源，编写可执行 YAML 解析器 + 2020-12 JSON Schema 运行时校验 + Compose/脚本跨字段校验，并接入 CI。
- **T03-02**：把 Portainer 示例调至真实 `portainer/app.yaml`，验证与脚本兼容、端口冲突和数据保护。
- **T03-03**：把 Sub2API 示例调至真实 `sub2api/app.yaml`，验证 PostgreSQL+Redis 安装、生成型 secret 与健康检查。
- **T03-07/T03-08**：固定最终部署包格式（本任务只定义文件清单），在 EasyTier 生成包，在 Agent 端逐文件/摘要验证并持久化版本。
- **T04-04/T05-04**：实现共享配置写入/数据删除保护和运行时兼容行为。

## 5. GitHub 文件地址

- [JSON Schema](https://github.com/CiBeDerORG/deploy/blob/main/schema/app-manifest-v1.schema.json)
- [Portainer 契约示例](https://github.com/CiBeDerORG/deploy/blob/main/schema/examples/portainer.app.yaml)
- [Sub2API 契约示例](https://github.com/CiBeDerORG/deploy/blob/main/schema/examples/sub2api.app.yaml)
- [Deploy 正式标准说明](https://github.com/CiBeDerORG/deploy/blob/main/docs/easyhostagent-app-manifest-v1.md)

> Deploy 为私有仓库，链接需要拥有相应 GitHub 访问权限。

**结论：T00-03 规范和契约例子已完成；可进入 T00-04 — 主机、Agent、应用实例、服务与权限的数据关系设计。**
