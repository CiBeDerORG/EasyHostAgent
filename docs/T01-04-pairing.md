# T01-04 — 一次性配对码、持久控制端授权与安全撤销

> 日期：2026-10-27
> 状态：**DONE：Go 代码、鉴权测试、Linux AMD64/ARM64 双架构构建成功**。
> 已验证 CI：[Go Agent HTTPS Identity and Pairing #40428675525](https://github.com/CiBeDerORG/EasyHostAgent/actions/runs/40428675525)（gofmt、go vet、go test -race、两架构编译及上传均 success）。
> 基准：`CiBeDerORG/EasyHostAgent` / `main`；真实 FnOS / EasyTier Rust 端配对集成仍待 T01-06/T01-08。

## 一、代码交付

| 文件 | 能力 |
|---|---|
| [internal/pairing/store.go](../internal/pairing/store.go) | 128-bit 一次性随机码、10 分钟过期、5 次失败限流、32 字节随机 Bearer Token（64 hex）、仅持久化 SHA-256、同机文件锁/0600/原子存储 |
| [internal/httpapi/httpapi.go](../internal/httpapi/httpapi.go) | `POST /v1/pairings`、`DELETE /v1/pairings/current`，HTTP 方法限制、JSON 上限 4KiB、未知字段拒绝、类型化安全错误 |
| [internal/app/app.go](../internal/app/app.go) | 启动 HTTPS 监听前校验并加载配对授权状态；文件损坏或 Agent ID 不一致即拒绝启动 |
| [cmd/easyhostagent/main.go](../cmd/easyhostagent/main.go) | 新增本地 `-pairing-code`，显示 Agent UUID、TLS SPKI SHA-256、一次性配对码与到期 UTC 时间 |
| [internal/pairing/store_test.go](../internal/pairing/store_test.go) | 同机重启持久性、5 次错误锁定、到期、重新配对旧 Token 失效、并发只成功一次、畸形文件与链接防护 |
| [internal/httpapi/pairing_test.go](../internal/httpapi/pairing_test.go) | 真实 TLS 下配对/一次性/撤销/无授权拒绝、限流及非法 JSON、Content-Type 的测试 |
| [cmd/easyhostagent/main_test.go](../cmd/easyhostagent/main_test.go) | 本地配对命令输出、安全内容及互斥参数测试 |

## 二、协议行为

**生成码**（宿主机受信任的本地终端；安装后持久目录为 root 所有）：

```bash
sudo /usr/local/bin/easyhostagent -pairing-code
```

程序显示 `Agent ID`、`TLS SPKI SHA-256`、`One-time pairing code`、`Expires (UTC)`。**配对码仅在这次本地命令输出，不通过未鉴权 HTTP 生成**。打开新码只替换旧配对窗口，不提前撤销已经生效的控制端授权。

**配对请求**：`POST https://<Agent IP>:7014/v1/pairings`，JSON `controller_id`、`device_id`、`pairing_code`。必须首先用可信本地终端提供的 SPKI 指纹确认 TLS 服务端真实身份；不能使用不检查证书的 HTTP 客户端直连，也不能将服务器自己返回的指纹当作首次信任。

- 正确且未过期：返回 201、Agent ID、控制端 ID、资产设备 ID、随机高熵 `access_token`、配对时间；**原始 Token 只在此次响应给出一次**。
- 同一个配对码不可重复使用；过期后必须从宿主机本地重新生成。
- 配对窗口内错误达到 5 次后返回 429，必须通过本地命令重新开启窗口；失败次数随 Agent 重启保留。
- 本地重新生成配对码时旧有效授权保持；**新配对成功才以原子写入方式使旧 Token 失效**。
- 控制端后续请求必须提供匹配 `Authorization: Bearer ...`、`X-EHA-Controller-ID` 和 `X-EHA-Agent-ID`，否则默认拒绝。
- 已授权的 `DELETE /v1/pairings/current` 返回 204，擦除存储的 Token 哈希、清空待使用的本地码；无法可靠写入时不谎报撤销成功。
- 当前 Agent ID 与 TLS 密钥在重新配对时**不变化**；只有删除主机本地持久身份才需要全新首次信任引导。

**持久化文件**：`<EASYHOSTAGENT_DATA_DIR>/pairing.json`，仅记录版本、Agent ID、一次性码摘要/到期/错误次数、controller/device 标识、Bearer 摘要与配对时间；不含原始码、Token。另用 `.pairing.lock` 实施跨进程互斥和原子更新。无法读取原文件、不匹配 Agent UUID、软链接或访问权限不安全时拒绝使用授权。默认宿主机目录为 `/var/lib/easyhostagent`；重装时 T01-02 安装器保留目录。

## 三、验收与真实边界

- [x] 真实 `go test -race ./...` 成功；包含高并发相同一次性码请求仅一人成功。
- [x] `gofmt`、`go vet` 全部成功。
- [x] Linux AMD64 / ARM64 静态编译及 CI 构建产物上传成功。
- [x] 配对、撤销、限制和错误场景的本机 HTTPS 集成单测通过。
- [x] 失效 Token、错误 Controller/Agent 组合无法通过验证。
- [x] 无后台心跳、自动重试或回滚；失败时保留可核查的状态。
- [ ] **尚未与 EasyTier Rust 后端实际集成 SPKI 固定指纹校验与资产设备绑定**；属于 T01-05/T01-06，不可称页面端已完成可信配对。
- [ ] **尚未在真实 FnOS systemd 安装/配置目录验证**，属于 T01-08。
- [ ] Docker/应用执行 API 尚未开放，未来新管理路由必须复用 `auth.RequireController` 与相同持久 Store；T06-03/T06-04 仍需权限与审计专项加固。

## 四、后续计划

**下一任务 T01-05：EasyTier 已有 `server` 语义设备的 Agent 接入入口。** 只复用现有资产设备、用户权限和连接记录，不新建第二台“主机”实体。T01-06 随后负责 Rust 控制端配对/指纹校验及实际 HTTP 接口联调。

附注：删除 EasyTier 设备时如果 Agent 离线，中心只能立即废弃本地 Token，不得谎称远端授权也已撤销；后续需主机本地重开窗口新配对或管理员主动清除旧授权，遵守 T00-04 的边界。
