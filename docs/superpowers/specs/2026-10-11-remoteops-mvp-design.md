# RemoteOps MVP 实现设计

需求基线：`docs/RemoteOps_PRD_v1.0.md`。本文件将 Phase 0/1 和 P0 要求落实为实现边界；P1/P2 不纳入首版。当前仓库只有 PRD，没有现有业务代码。

## 1. 交付范围与方案选择

采用 PRD 指定方案：Go Controller + SQLite、Windows Go Worker + 内嵌 tsnet、Vue 3/TypeScript 管理页、stdio MCP 适配器。Web 和 MCP 都调用同一 Controller API。

首次连接采用带外配对包。操作员独立在 Tailscale 管理后台生成短期、不可复用、ephemeral 的 Auth Key，通过安全渠道交给 B；Controller 不保存或向浏览器返回该 Key。Worker 从本地输入获取 Key，仅用于本次入网，不输出到日志。公网 HTTPS Bootstrap 和 B 主动 WSS 连接均属于后续方案，首版不引入公网服务。

首版实现全部 P0 页面和核心闭环：创建会话 → B 本地确认 → 注册/心跳 → 环境检查 → 安装计划 → 人工审批 → 执行 → 日志/版本验证 → 关闭与节点清理。配置、审计和可见的清理重试同时交付。不添加任意 Shell、GUI 自动化、系统服务、自启动或自动重启。

## 2. 程序与目录边界

| 路径 | 职责 |
|---|---|
| `cmd/controller` | A 入口、启动参数、监听与优雅退出 |
| `cmd/worker` | B 本地授权界面、tsnet 启动、退出 |
| `cmd/remoteops-mcp` | stdio JSON-RPC MCP 入口 |
| `pkg/protocol` | 协议版本、DTO、错误码和状态枚举 |
| `internal/controller` | API、SQLite、调度、审批、清理、审计 |
| `internal/worker` | RPC、身份验证、执行与结果缓存 |
| `internal/worker/installers` | MSI、受维护的 Git EXE 模板、限定 winget 适配 |
| `internal/worker/privilege` | Windows 提升令牌检测与正式 UAC 调用 |
| `internal/security` | 随机凭据、签名、重放防护、脱敏 |
| `integrations/mcp` | 工具 schema 和 Controller 客户端 |
| `api/openapi.yaml` | Controller/Worker 契约 |
| `web` | Vue 管理界面 |
| `tests/integration` | Controller + 模拟 Worker 闭环及故障测试 |
| `docs/security`、`docs/test-plans` | ACL 示例、部署与真实设备验收 |
| `.github/workflows`、`scripts` | CI、跨平台构建、产物 SHA-256 |

Controller 分别监听本机管理端口和明确指定的 tailnet 注册端口。管理端默认 `127.0.0.1:8080`；tailnet 端只挂载注册、心跳、Worker 主动关闭 API，不暴露操作员 API。Worker RPC 通过 `tsnet.Server.Listen` 在 tailnet 的指定端口服务。

Vue 构建产物嵌入 Controller；A 可单程序运行。B 交付单 EXE，通信进程默认普通权限。MCP 独立 stdio 程序，复用 A 本机 API，不启用公网 MCP 监听。

## 3. 协议与身份

协议版本为 `v1`，JSON 时间为 UTC RFC3339，ID 为带类型前缀的加密随机字符串。未知字段和错误类型拒绝，HTTP 请求体限制 1 MiB。错误结构固定为 `{"error":{"code":"session_expired","message":"…","request_id":"…"}}`。

操作员使用启动时生成或配置的管理令牌，SQLite 中仅保存令牌摘要。Web 登录换取 HttpOnly、SameSite=Strict 会话 Cookie；写请求检查 Origin 与 CSRF Token。MCP 使用独立的 Agent 令牌进行 Authorization Bearer 认证，该令牌不能换取 Web 登录身份。Agent 自报的审批无效，只有已登录 Web 操作员能够批准请求。非回环管理监听必须显式配置 TLS。

配对包包含协议版本、Controller 名称和地址、会话 ID、配对凭据、到期时间、允许的任务类型和软件目录摘要；不包含管理令牌或 Tailscale 管理 Secret。配对凭据为高熵随机串，不以短数字码作为唯一认证材料，服务端仅保存摘要，使用数据库事务保证单次消费。

B 在本地展示控制方、有效时长、检查项、允许的软件及管理员权限说明，用户确认后才能注册。注册返回绑定设备和会话的短期通信凭据，Worker 只接受该会话的签名请求；配对包经可信带外渠道传递是首次信任边界。

私网业务请求使用 HMAC-SHA256，认证输入由协议版本、HTTP 方法、路径和规范化查询串、时间戳、nonce、body SHA-256 组成；响应同时签名，并绑定请求 nonce 和响应体摘要。方向使用独立派生密钥。默认时钟偏差窗口 60 秒，nonce 在有效窗口内不能复用。设备、会话和到期时间同时校验，tailnet 成员资格不能替代应用身份。

注册时验证会话约束和 Tailscale 对端身份；Controller 不连接 Worker 自报的任意 URL。RPC 地址只能从确认的 tailnet 地址与固定端口构造；节点 ID 由可信 Tailscale 身份信息绑定，避免任意地址访问及错误节点删除。

## 4. API 与数据

保留 PRD 中 `/api/v1/sessions`、`devices`、`tasks`、`audit-events` 路径。新增会话列表、任务列表、`/plans`、`/approvals`、`/approvals/{id}/approve`、`/approvals/{id}/reject`、`/config` 和 `/sessions/{id}/retry-cleanup`。Worker 注册端提供 `/api/v1/worker/register`、`heartbeat`、`close`；Worker RPC 保留 PRD 的 `/health` 和 `/v1` 路径。

SQLite 保存 sessions、devices、plans、approvals、tasks、task_logs、audit_events、cleanup_jobs 和 settings。启用外键、WAL、busy timeout，使用显式迁移版本；日志与审计按配置留存期清理。

会话保存操作员、配对截止时间、连接后授权截止时间、范围、绑定设备、状态、心跳时间、临时节点 ID 和清理结果。配对窗口默认 10 分钟，授权时长默认 60 分钟，心跳每 10 秒，45 秒未收到则撤销当前应用授权。

任务类型首版为 `inspect_system` 和 `install_package`。安装参数包含目录软件 ID、固定版本、安装模式、超时、requires_admin、approval_id 和 idempotency_key。任意命令、客户端提供的 EXE 参数、任意路径和安装 URL均拒绝。软件目录由 A 本地配置，Web 不能直接添加未经维护的命令模板。

审批绑定 session_id、device_id、任务规范化摘要、目录条目摘要、提权标记和到期时间；修改任意执行参数需重新审批。审批单次消费，任务创建与消费在同一事务内。唯一索引保证 `(session_id, idempotency_key)` 不重复；同键不同摘要返回 conflict。

任务日志采用递增 sequence 游标，接口参数为 `after`、`limit`；默认每页 100 条、最多 500 条，每任务最多 1 MiB，截断记录可见。Worker 与 Controller 都先脱敏再保存；凭据、Cookie、Auth Key 不进入日志。进度包含阶段、时间戳、退出码、重启提示和验证结果。

## 5. 状态、调度与恢复

会话状态：PendingPair → Connecting → Online/Running → Revoking → Closed/CleanupPending；未配对超时进入 Expired，连接失败进入 Failed。关闭首先事务性撤销应用授权，再取消任务、通知 Worker 和删除节点。清理失败显示原因，按退避重试，并允许操作员手动重试；节点已不存在视为成功。

任务状态：pending_approval → queued → running → succeeded/failed/cancelled/timed_out；失去 Worker 联系或 Controller 重启时，已运行任务进入 unknown，不能自动重放安装。恢复通过查询同一 task_id 的 Worker 缓存核实结果；无法核实时保留 unknown 和诊断原因。

总任务并发默认 3、可配置，同一 B 同时执行一个任务。Worker 本地再次验证范围、审批执行证明、期限与幂等摘要；任务 ID 与幂等键在本会话内缓存至关闭。同任务重试只返回已有结果，不启动第二次安装。

会话关闭、到期或心跳丢失后调度立即停止；Worker 自身也检查授权到期和 Controller 心跳。超时与取消终止受控进程树，安装已经产生的系统变更不会宣称已回滚。

## 6. Windows 执行与本地控制

B 提供本机回环界面显示会话、任务、权限状态和“立即断开”，控制入口使用随机本地令牌及 Origin 校验。控制台也支持 Ctrl+C，关闭通过同一个撤销流程执行。tsnet 使用会话专属临时数据目录，设置 Ephemeral，关闭尝试 Logout/Close 并清除敏感临时文件。

系统检查限于 OS、CPU、内存、磁盘容量、GPU/驱动以及目录中指定的软件版本，不收集用户个人文件。非 Windows 实现只用于测试，安装执行明确返回 unsupported_platform。

下载仅允许目录指定 HTTPS 来源及允许的重定向域，限制大小、检查剩余空间，执行前校验固定 SHA-256。临时文件名和目录由 Worker 生成，拒绝路径穿越。MSI 使用维护的 msiexec 参数，Git EXE 使用维护的静默模板；winget 仅支持目录指定的 ID、source 和版本。0 为成功；3010 为成功且需要重启，不自动重启。

管理员需求通过 Windows 提升令牌检测，不能用管理员组成员资格代替。需要提升时，本地界面明确提示，用户通过 ShellExecuteEx 的 runas 正式调用本 EXE 的受控 helper。helper 只消费单次、绑定摘要的安装请求，IPC 使用访问控制和随机会话证明；不启动新的高权限远程 RPC。用户拒绝返回 permission_denied。

执行后运行目录中的版本验证，记录安装退出码和独立验证结果。版本验证失败不能报告安装已验证成功。输出有界，采用允许的环境变量和固定工作目录。

## 7. Web 与 MCP

Web 包含设备、会话、任务、配置、审计五个视图，提供创建配对、任务规划、审批、日志分页、取消、断开和清理重试。刷新通过受控轮询实现，避免首版引入额外事件通道。Secret 不在配置接口返回。

MCP 提供 PRD 的九个工具，完成 initialize、tools/list、tools/call 和结构化错误处理。create_install_plan 只规划；request_task_approval 创建待审请求；install_software 必须引用 Web 批准的审批。get_device_info 通过受控检查任务获取环境信息；所有执行调用经过 Controller 同一权限路径。MCP stdout 只写协议数据，诊断写 stderr。

## 8. 验证与发布标准

自动化测试覆盖配对过期/复用、错误签名/响应身份、nonce 重放、跨设备访问、审批参数变更、幂等并发竞争、超时取消、关闭拒绝新任务、日志脱敏与截断、数据库恢复、未知任务不重放、清理失败重试、下载来源/摘要和 UAC 拒绝映射。模拟传输只能验证业务逻辑，不代替真实 tailnet。

CI 执行 Go 单元与集成测试、竞态检测（支持的平台）、go vet、协议兼容检查、Vue 类型检查和构建、Windows x64 Worker 与 macOS/Linux Controller 编译；锁定依赖并输出构建产物 SHA-256。

人工验收记录 Windows 10/11 的单 EXE、本地同意、UAC 批准/拒绝、真实 MSI/Git 安装验证、不同 NAT 连通、临时节点清理、ACL 阻止 B↔B、断线/崩溃和三个 B 顺序接入。无可用 tailnet 凭据及真机时，这些项目标记为未实测，不能据编译或模拟测试声称通过。

## 9. 实施顺序

1. 冻结共享协议、状态机和 OpenAPI，补齐安全契约测试。
2. 实现 SQLite、Controller 鉴权、配对/会话/设备、审批和调度。
3. 实现 Worker 签名 RPC、环境检查、tsnet 及本地授权/断开。
4. 实现受控下载、安装适配器、UAC helper、日志和取消。
5. 实现 MCP 与 Web，接入同一 API。
6. 集成恢复/清理测试、CI、发布脚本、部署与真实网络验收文档。

协议冻结后 Controller、Worker、Web 可按 PRD 的职责划分在隔离 worktree 中并行实现；主流程负责共享契约、MCP、集成和最终验证。
