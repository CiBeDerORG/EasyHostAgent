# T01-03：稳定 Agent 身份、TLS 1.3 HTTPS 与安全接口基础

> 日期：2026-10-27
> 状态：**DONE — Go 代码已提交；gofmt、go vet、go test -race、linux/amd64 与 linux/arm64 CI 全部通过。**
> 范围：本轮仅实现身份与只读 HTTPS 发现、受保护接口鉴权框架；不实现配对码或任何有副作用的远程动作。
> 已验证工作流：[Go Agent Foundation and HTTPS Identity #40427223740](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40427223740)
> 功能首次提交：[0bb6a63](https://github.com/CiBeDerORG/EasyHostAgent/commit/0bb6a63af1e67ff071f3fe5dbcdabb81360a1828)；后续格式化与测试修复已合并至 main。

## 一、实际交付

| 模块 | 源码路径 | 结果 |
|---|---|---|
| 持久身份 | `internal/identity/identity.go` | 首次生成密码学随机 v4 UUID；ECDSA P-256 私钥、自签名 TLS 服务证书、SPKI SHA-256 十六进制指纹；写入 `identity.json`，0600；状态目录必须为 0700 |
| 并发与错误恢复边界 | `internal/identity/identity.go` | `.identity.lock` + Linux `flock` 防同机重复首次创建；原子写入并同步磁盘；已有文件损坏、证书过期或权限异常时拒绝启动，绝不自动替换身份 |
| 本地身份查看 | `cmd/easyhostagent/main.go` | `-identity` 输出 Agent ID 和 TLS SPKI SHA-256；用于宿主机可信终端人工核对，不会输出 TLS 私钥 |
| HTTPS 身份与健康接口 | `internal/httpapi/httpapi.go` | `GET /v1/health`、`GET /v1/identity`，JSON 与 OpenAPI v1 字段保持一致；加 `X-EHA-Agent-ID`、`Cache-Control: no-store` 与 `nosniff` |
| 认证中间件 | `internal/auth/auth.go` | Bearer Token + `X-EHA-Controller-ID` + `X-EHA-Agent-ID` 校验框架；无持久配对凭证时默认拒绝，避免未授权管理；Token 检查不写入日志 |
| Go 原生服务 | `internal/app/app.go` | Go `http.Server` 走 TLS 1.3；请求头/请求/响应超时与上限；监听配置、SIGTERM 退出及优雅关闭 |
| 配置 | `internal/config/config.go` | 新增 `EASYHOSTAGENT_LISTEN_ADDRESS`，默认 `0.0.0.0:7014`，支持绑定指定 IP；FnOS/Linux 元数据字段 |
| 测试 | `internal/*/*_test.go`、`cmd/easyhostagent/main_test.go` | 身份重复载入、并发首次创建、恶意软链接与损坏身份、JSON 身份格式与 SPKI、实际 HTTPS 握手、请求方法与明文拒绝、认证失败与配置校验 |
| systemd | `packaging/systemd/easyhostagent.service` | 更新注释与当前服务范围；继续由 T01-02 的原生安装器托管 |

本轮没有引入 Redis、消息队列、定时心跳、Git 仓库下载、Docker 或自动回滚。

## 二、身份与证书行为

首次调用 `easyhostagent -identity` 或 systemd 启动时，状态目录默认为 `/var/lib/easyhostagent`。文件：

```text
/var/lib/easyhostagent/
├── .identity.lock   # 同机进程间锁（0600）
└── identity.json    # UUID + PEM 服务证书 + PEM 私钥（0600）
```

**新身份只在原文件不存在时创建。** 后续启动从原文件加载，并检测 UUID、证书与密钥匹配、证书有效期及原文件权限。数据目录为 0700，否则拒绝启动。不使用备用随机身份绕过错误。旧 Agent 重新安装可沿用原数据目录；若用户主动删除身份数据导致生成新的 Agent ID，EasyTier 必须要求重新配对（T01-04/T01-06）。

本地查询命令：

```bash
sudo /usr/local/bin/easyhostagent -identity
```

示例输出（UUID/指纹为占位示例，不是任何真实主机）：

```text
Agent ID: 11111111-1111-4111-8111-111111111111
TLS SPKI SHA-256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
```

首次证书为自签名证书，`localhost`、宿主机名称及回环 IP 具有 SAN；**EasyTier 通过设备 IP 连接时，不得直接跳过 TLS 校验**：后续控制端必须实现与在宿主机可信终端获得的 SPKI 指纹相匹配的 TLS pinning；IP 改变后校验稳定指纹和 Agent UUID。HTTP `/v1/identity` 自己返回的指纹不能建立首次信任。

## 三、当前可访问的 API

```text
GET https://<host-ip>:7014/v1/health
GET https://<host-ip>:7014/v1/identity
```

响应字段按 `docs/api/openapi-v1.json` 的 `Health`、`Identity` 定义。无任何命令执行、安装、授权、Docker、机密查询或写入 API 被开放。未实现的 `POST /v1/pairings` 与 `/v1/tasks` 当前返回 404。

默认监听 `0.0.0.0:7014`，**已能被同一网络内设备访问**；应由宿主机/路由器防火墙限制可信管理来源，**不要做未经认证的公网端口发布**。配置环境变量 `EASYHOSTAGENT_LISTEN_ADDRESS=127.0.0.1:7014` 可限制为本地回环。仅 TCP/HTTPS，没有后台网络轮询。

## 四、鉴权状态与后续衔接

`internal/auth.RequireController` 必须有合法 TLS、目标 Agent ID、控制端 ID 和由 `Verifier` 判断有效的 Bearer Token，否则拒绝请求；T01-03 目前没有注册任何需认证的高权限路由，因此它作为**安全可复用组件**通过单元测试，不等于“持久配对授权已经实现”。

下一步 **T01-04** 在此基础上实现：本地一次性随机码、过期/尝试次数、POST 配对、持久化授权、主动撤销及新配对轮换旧 Token。没有配对成功前，客户端不能远程执行任何操作。

## 五、CI 验收

GitHub Actions [#40427223740](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40427223740) 已完成且结果为 **success**：

- [x] `gofmt` 静态检查成功。
- [x] `go vet ./...` 通过。
- [x] `go test -race ./...` 通过（含真实本地 HTTPS TLS 握手和身份/API/权限测试）。
- [x] Linux AMD64 静态编译成功，测试产物 `easyhostagent-foundation-linux-amd64` 已上传。
- [x] Linux ARM64 静态编译成功，测试产物 `easyhostagent-foundation-linux-arm64` 已上传。
- [x] T01-02 原生安装器在引入 HTTPS 前后的隔离回归 CI 通过（[Run #40426383260](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40426383260)）。
- [ ] 尚未在真实 FnOS 系统中安装验证本机防火墙、systemd 服务状态和网络路由，留 T01-08。
- [ ] 尚未将 EasyTier Rust 客户端改为 SPKI 指纹绑定与配对，留 T01-06。
- [ ] 未实现一次性配对或管理请求权限（T01-04），没有发布可生产管理 Docker 的 Agent。

**结论：T01-03 代码与可自动化验收范围完成，可进入 T01-04。**
