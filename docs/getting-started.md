# 部署、使用与开发

[返回项目介绍](../README.md) · [English overview](../README.en.md)

面向临时 Windows 设备的受控软件安装工具。A 运行 Controller 和官方 Tailscale，B 运行单个内嵌 tsnet 的 Worker EXE；Web 和 Codex/Claude Code 的 MCP 通过同一 API 管理会话、审批、任务和日志。

当前实现包括 SQLite 持久化、一次性配对、设备心跳、范围校验、人工审批、幂等执行、MSI/Git EXE/限定 winget 安装模板、版本验证、受控 UAC helper、取消、脱敏日志、节点清理重试、五个 Web 视图及九个 stdio MCP 工具。真实异地 tailnet、Windows 10/11 UAC 决策和软件安装的验收状态见 [人工验收矩阵](test-plans/manual-acceptance.md)。

网页、Worker GUI/TUI/plain、Controller 和 MCP 支持简体中文与 English，默认中文。网页在登录页和工作台选择，GUI 用语言下拉框或 Alt+G，TUI 按 F2，plain 在普通提示处输入 `lang en-US` / `lang zh-CN`。原生入口可用 `--lang=en-US`；优先级为 `--lang` → `REMOTEOPS_LANG` → `--config` → 当前用户偏好 → 中文。各端独立保存语言，不改变远程授权或协议。配置文件、各程序路径与保存限制见[语言设置](language-settings.md)，双语惰性验收及限制见[测试报告](test-plans/system-i18n-2026-10-12.md)。

## 构建与测试

需要 Go 1.26.3+、Node.js 22.12+。依赖版本由 go.mod/go.sum 和 web/package-lock.json 固定。

```sh
node scripts/build.mjs
go test ./...
go vet ./...
npm test --prefix web
```

构建脚本安装 Web 依赖、类型检查、编译并嵌入前端，输出 Windows x64 Worker（GUI 和 TUI）/Controller/MCP、Linux x64 和 macOS arm64/x64 Controller/MCP，以及 `dist/SHA256SUMS`。只更新两个 Worker 可运行 `node scripts/build.mjs --worker-only`，现有其他产物保留，清单重新计算；构建脚本不会启动任何程序。两个 Worker 都以 `CGO_ENABLED=0` 构建，不需要 WebView2。仓库保存一份已构建 Web 资源，因此 `go build ./cmd/controller` 也可直接构建管理端。开发前端后用构建脚本更新嵌入资源。

支持的环境可运行 `go test -race ./...`；Windows 竞态检测需要 C 编译器。修改 DTO 后运行 `go run ./scripts/schema` 重新生成 OpenAPI。

## A：启动 Controller

1. 安装并登录官方 Tailscale，取得 A 的 tailnet IP。
2. 配置 [最小网络策略](security/tailnet-policy.md)，注册端口示例 9080，Worker RPC 9081。
3. 设置高熵 `REMOTEOPS_ADMIN_TOKEN`（至少 32 字符），该值只供操作员 Web 登录。需要主动节点删除时，在 A 单独设置 `REMOTEOPS_TAILSCALE_API_KEY`。
4. 运行 Controller；以下 `100.64.0.1` 必须替换为 A 的实际地址。

```sh
./dist/remoteops-controller-linux-amd64 \
  --worker-listen 100.64.0.1:9080 \
  --name "我的控制端" \
  --catalog config/catalog.example.json
```

管理页默认 `http://127.0.0.1:8080`，使用操作员令牌登录。数据库默认位于受保护的 `remoteops-state/remoteops.db`；Controller 生成独立的每次启动 MCP 令牌到 `remoteops-state/mcp.token`，不在日志中打印。也可自行设置不同的 `REMOTEOPS_MCP_TOKEN`，使用该值启动 MCP。

管理监听配置为非回环地址时，必须同时设置 `--tls-cert` 和 `--tls-key`。首次只读测试可省略 `--catalog`；默认目录为空，不会执行软件安装。示例目录包含固定版本 Git 和 Node MSI，摘要来自官方发布记录，见 [目录说明](../config/README.md)。

## B：本地同意与连接

1. 在 A Web 创建会话，选择检查/安装范围、允许软件和管理员权限，导出配对 JSON。
2. 操作员在 Tailscale 管理后台生成**不可复用、短期、ephemeral** Auth Key，通过可信带外渠道分别交付配对 JSON 和 Auth Key。
3. B 用户以普通权限双击 `remoteops-worker-gui-windows-amd64.exe`，点击“浏览…”选择配对 JSON，再点击“读取文件”。不能以“管理员运行”启动通信程序。
4. 核对控制方、授权时长、任务及软件范围，勾选本地同意后，在遮罩输入框粘贴 Auth Key，点击“同意并连接”。密钥只在本地输入，不能通过命令行传入。程序不会自动打开浏览器。

也可在交互终端启动 TUI，旧 `--pairing` 命令保持可用；省略文件路径时界面会要求选择/输入：

```powershell
.\remoteops-worker-windows-amd64.exe --pairing .\pairing.json
.\remoteops-worker-windows-amd64.exe --ui=plain --pairing .\pairing.json
```

TUI 使用 Y 明确同意、遮罩输入密钥；plain 中文输入“同意”，English 输入 `AGREE`，密钥不回显。连接后显示任务、阶段、日志和授权倒计时；下载进度来自实际字节，总长度未知时只显示已下载字节与活动提示，安装阶段只显示阶段与用时。GUI 可取消所选任务或立即断开，TUI 使用 C 取消、D 确认断开，Ctrl+C 退出。取消和断开不保证回滚已有修改，需要重启时只作提示。需要管理员的安装由本地 Windows UAC 逐项请求，只有受控 helper 提权，通信 RPC 保持普通权限。详细按键、退出清理及限制见 [Worker 使用指南](worker-client.md)，验收证据见 [客户端测试报告](test-plans/worker-client-2026-10-11.md)。

配对 JSON 含单次敏感凭据，妥善传递并在使用后删除。程序不内置长期 Auth Key、OAuth Secret 或管理员密码。会话关闭首先撤销应用操作权限，再清理 Worker/节点；清理失败在 Web 显示 `cleanup_pending` 并可重试。

## MCP：接入 Codex / Claude Code

MCP 使用独立 Agent 令牌，不能用它登录 Web 或批准任务。以下配置中的命令及 token-file 必须替换为绝对路径。

```json
{
  "mcpServers": {
    "remoteops": {
      "command": "/absolute/path/remoteops-mcp-linux-amd64",
      "args": ["--controller", "http://127.0.0.1:8080", "--token-file", "/absolute/path/remoteops-state/mcp.token"]
    }
  }
}
```

Codex CLI 可将相同 command/args 放入 MCP server 配置，或使用：

```sh
codex mcp add remoteops -- /absolute/path/remoteops-mcp-linux-amd64 --token-file /absolute/path/remoteops-state/mcp.token
```

提供 `list_devices`、`get_device_info`、`create_install_plan`、`request_task_approval`、`install_software`、`get_task_status`、`get_task_logs`、`cancel_task`、`close_session`。规划不执行，审批请求不自动批准。操作员在 Web 审批后，Agent 才能以相同任务参数和 approval_id 下发安装。管理端重启生成新 Agent 令牌时，重启 MCP 进程读取新文件。

## 安全和行为边界

- B 只能执行明确授权的检查和维护安装模板，不能运行任意 Shell、下载任意 EXE 或接受任意静默参数。
- 网络身份经 Tailscale WhoIs 核实，业务请求和响应另有 HMAC、时效、重放及设备绑定校验。
- 审批绑定软件固定版本、设备、会话、提权、超时和目录内容；参数改变必须重新审批。
- 结果不明的安装保留 unknown，查询原 task_id，不自动重复执行。
- 取消不能保证回滚已经发生的修改；3010 显示需要重启，程序不自动重启。
- 关闭不会将 B 持久化为系统服务或开机启动。临时节点自然回收不能代替主动清理。

需求见 [PRD](RemoteOps_PRD_v1.0.md)，协议见 [v1 契约](architecture/protocol.md) 与 [OpenAPI](../api/openapi.yaml)。
