# 协议 v1

`pkg/protocol` 是 JSON 字段、状态和类型的唯一来源。运行 `go run ./scripts/schema` 从 DTO 生成 `api/openapi.yaml`（JSON 语法也是合法 YAML）。管理 API 返回直接对象或数组，错误返回 `{"error":{"code":"…","message":"…","request_id":"…"}}`。

## 管理身份

本机 MCP 使用 `Authorization: Bearer <agent-token>`。操作员私有的管理令牌与 Agent 令牌不同；Agent 令牌不能调用登录 API 换取操作员身份。Web 将管理 token POST 到 `/api/v1/auth/login` 换取 HttpOnly、SameSite=Strict 的 `remoteops_session` Cookie 和 csrf_token；写请求同时带 `X-CSRF-Token` 和同源 Origin。安装批准/拒绝只能由 Cookie 认证操作员执行，Bearer 调用必须失败。

## 私网请求和响应

Web 创建会话成功后自动下载配对 JSON，并保留显式下载按钮。刷新或重新登录后，尚未配对且未过期的会话可通过 `POST /api/v1/sessions/{id}/pairing`（空对象请求）重新生成并下载文件；此接口仅接受浏览器操作员 Cookie、同源 Origin 和 CSRF。重新生成立即使旧配对 token 失效，清除旧签名校验器缓存，且保留原配对截止时间、授权范围、冻结软件目录和连接后授权时长。数据库只保存配对 token 摘要，审计记录 `pairing_regenerated`，不会保存或记录配对 token 明文。已配对、已关闭或已过期会话需创建新会话。

三类头为 `X-RemoteOps-Time`（Unix 秒）、`X-RemoteOps-Nonce`（随机请求标识）、`X-RemoteOps-Signature`（十六进制 SHA-256 MAC）。默认时间窗口前后 60 秒，nonce 只能使用一次。签名校验先于重放缓存写入，篡改请求不能抢占有效请求的 nonce。

方向字符串为 `controller-to-worker` 或 `worker-to-controller`。每个方向的密钥为 HMAC-SHA256(secret, `remoteops/v1/` + direction)。请求签名输入由以下七行组成，无尾部换行：

```text
v1
HTTP_METHOD
URL_ESCAPED_PATH
SORTED_ENCODED_QUERY
UNIX_TIMESTAMP
NONCE
HEX_SHA256_EXACT_BODY_BYTES
```

响应使用相同方向密钥，必须绑定对应请求的 nonce，签名输入由以下六行组成：

```text
v1-response
DIRECTION
HTTP_STATUS
REQUEST_NONCE
RESPONSE_UNIX_TIMESTAMP
HEX_SHA256_EXACT_RESPONSE_BODY
```

配对注册初始 secret 是带外可信交付的 pairing_token；注册成功响应仍用该值签名，B 必须验证响应后才能接受 session_token。随后心跳及 Worker RPC 用注册返回的 session_token。签名认证不负责加密：这些 HTTP 通信只通过加密 tailnet；公网引导未包含在 MVP 中。

## 授权与幂等

TaskSpec 的 `session_id`、`device_id`、类型、软件 ID/固定版本、超时和提权标记经规范 JSON SHA-256 产生 TaskDigest。审批同时绑定 session/device、目录摘要和有效期，单次消费。Worker 安装请求的 approval_digest 必须匹配实际 TaskSpec，并通过 Controller 的私网签名证明来源。

同一会话和 idempotency_key、同一摘要返回原任务；同键不同参数冲突。Worker 也保留会话内缓存，网络重试不二次执行。发生通信不确定时查询原 task_id，不创建新的安装请求。

任务日志使用递增 sequence。`after` 为已读取的最后游标，`limit` 默认 100、最大 500；LogsPage 返回 entries、next_after、truncated。时间统一 UTC RFC3339。每任务日志默认 1 MiB，错误、日志和审计均先脱敏。
