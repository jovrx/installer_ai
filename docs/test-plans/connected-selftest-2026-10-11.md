# 已连接设备自测：2026-10-11

测试对象为当前运行的 Controller（127.0.0.1:8080，PID 31928）及真实已连接的 Windows Worker。通过独立 MCP 凭据调用实际管理 API，未修改源码或关闭用户会话。

## 实机结果

- 新建只读系统检查任务 `task_0511faeda2bafd39b3e117b13c3458d24624cd90c883f769`，状态 `succeeded`，`verified=true`。2026-10-11 北京时间 21:01:16 开始，21:01:17 完成，执行约 1.8 秒。
- 返回 Windows 10 专业版 10.0.19045、CPU、内存、磁盘及 GPU 信息，Worker 保持普通权限。
- 使用同一幂等键重试返回 HTTP 201 和原 task_id，未重复执行。
- 同一幂等键改变超时参数返回 HTTP 409 `idempotency_conflict`。
- 当前会话仅授权 `inspect_system`；尝试创建安装任务返回 HTTP 409 `session_inactive`，未执行安装。
- Agent 凭据调用批准接口返回 HTTP 403 `operator_confirmation_required`。
- 前述拒绝检查未增加任务；测试后共两条成功的系统检查任务（一条为用户先前执行，一条为本轮自测）。

## 本轮自动验证

- `go test ./... -count=1`：12 个含测试的包通过。
- `go test -race ./... -count=1`：12 个含测试的包通过，无竞态报告。
- `go vet ./...`：通过。
- `npm test --prefix web`：2 个测试文件、12 个测试通过。
- `npm run typecheck --prefix web`：通过。
- `git diff --check`：无空白错误。

## 范围和发现

本轮实机验证通过 API 完成；Chrome DevTools 自动化实例因已有诊断浏览器占用 profile 而无法接入，没有执行新的浏览器点击测试。未测试真实软件安装、UAC 或节点删除。

只读系统检查在授权范围内可由 API 直接执行，无须任务审批；当前 Web 为检查与安装统一提供计划、审批、执行流程。安装的审批约束由后端单独强制执行。

初次终端输出看似中文乱码，使用 Unicode 转义核实接口返回的是正确中文，问题出在测试终端的显示解码。初次将只读检查也应强制审批作为测试预期是不正确的；依据实际实现及 PRD 修正后，上述本轮检查全部通过。
