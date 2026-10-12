# RemoteOps MVP 开发验证记录

日期：2026-10-11。开发环境：Windows x64、Go 1.26.3、Node 22.22.1、npm 10.9.4。需求范围为 PRD Phase 0/1 的代码实现；真实网络和安装验收见独立矩阵。

## 已运行验证

| 检查 | 结果 |
|---|---|
| `go test -race -json -count=1 ./...` | 42 项测试、12 个测试包通过，0 失败 |
| `go vet ./...` | 退出码 0 |
| `go mod tidy` | 退出码 0，依赖与校验和已固定 |
| `npm ci` | 成功 |
| `npm run typecheck` | 成功 |
| `npm test` | 2 个测试文件、10 项测试通过 |
| `npm audit --audit-level=low` | 0 vulnerabilities |
| `node scripts/build.mjs` | 9 个程序交叉编译成功，生成 SHA256SUMS |
| Windows Worker / Controller `--help` | 正常显示入口和参数 |
| Windows MCP initialize | stdio 返回合法 JSON-RPC、协议版本和工具能力 |
| 嵌入 Web | API 鉴权不被 SPA 路由覆盖，页面/JS/CSP 正常，缺失资源返回 404 |
| 真实 Controller + SQLite 浏览器检查 | 登录、创建配对、显示 3600 秒授权、Tailscale 状态、审计均正常；390px 布局无页面水平溢出，页面不显示配对秘密 |
| 本机 Windows 只读测试 | OS、CPU、内存、磁盘采集及普通/提升令牌识别测试通过 |
| 无害进程取消测试 | 受控进程树取消通过；未执行软件安装 |

交付程序：Windows amd64 Controller/Worker/MCP；Linux amd64 Controller/MCP；macOS amd64、arm64 Controller/MCP。编译通过不代表目标系统和真实 tailnet 上的端到端运行验收通过。

## 关键回归覆盖

- 配对一次性消费、截止时间和签名；重启后的 nonce 仍不能复用。
- Worker 双向身份、签名篡改、nonce 重放、跨设备任务、范围及明确固定版本校验。
- MCP 令牌与操作员令牌分离；Agent 不能登录 Web 自批，Bearer 不能批准安装。
- 审批摘要与目录、设备、会话、超时和管理员要求绑定；单次消费及同键不同参数冲突。
- 同一安装请求只执行一次；Controller 重启/不确定结果仅查询原 ID，禁止自动重发有副作用操作。
- 慢速 Worker 请求不阻塞关闭和其他设备心跳；任务完成保留新心跳。
- 配对后配置时长变化不扩大已经显示给 B 的本地授权，Worker 限制本地期限。
- 日志大小、游标、截断及文本/JSON/私钥/Bearer 脱敏。
- 会话关闭立即停止授权、Worker 拒绝后续任务；节点清理失败可见并可重试。
- HTTPS 安装源、重定向 allowlist、文件大小/磁盘余量和 SHA-256 校验。
- UAC helper 请求摘要、软件和权限绑定；只有受维护模板可执行。
- Windows 原生 DACL 保护 SQLite 主库、WAL/SHM/journal、Agent 令牌；不擅自修改现有共享目录 ACL。
- 审计实际区分 operator、agent、worker、system。

独立审查发现的授权时长、Windows ACL、新心跳覆盖和审计归属四个问题均已修复，针对修复的复查未发现新的关键问题。

## 未实测项目

未连接任何真实 B、未加入/修改用户 tailnet、未调用真实节点删除、未执行软件安装或 UAC 提升。Windows 10/11 新机、不同 NAT、3～10 B、实际 MSI/Git/winget、UAC 批准/拒绝和 macOS/Linux 真机运行仍需按 `manual-acceptance.md` 验收。

这些项目需要用户提供真实部署环境与凭据，不以本地模拟安装或交叉编译代替。Controller 当前只读取本机 Tailscale 状态，浏览器检查显示未连接。
