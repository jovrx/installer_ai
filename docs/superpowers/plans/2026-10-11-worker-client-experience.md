# B 端 GUI / TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付独立 Windows 桌面客户端与中文终端 TUI，完整呈现授权、连接、真实任务进度和结束结果。

**Architecture:** 共用 client 生命周期和只读快照。执行器通过保留的内部 progress 事件流报告阶段与字节；Worker 将其转为本地任务视图，GUI/TUI 轮询快照，不阻塞执行线程。提权 helper 实时转发经过认证的进度和脱敏日志。

**Tech Stack:** Go 1.26、Windows Win32 API（golang.org/x/sys/windows）、Bubble Tea / Lip Gloss、tsnet；CGO_ENABLED=0 的交付构建。

**Spec:** docs/superpowers/specs/2026-10-11-worker-client-experience-design.md

## Global Constraints

- Windows 10/11 x64 普通用户运行；通信程序不能整体提权。
- 独立 Windows 桌面窗口，不依赖 WebView2 Runtime，不打开额外命令窗口。
- CLI 原 `--pairing 文件.json` 命令保持有效，默认 TUI，提供 `--ui=plain`。
- 默认中文，Auth Key 遮罩输入，不允许参数传入密钥，不跳过本地同意。
- 下载显示真实接收字节；目录 size_bytes 是上限，不能用作真实总量。
- 安装未知百分比时仅阶段、不定进度和已用时间；不能制造总百分比。
- 不执行真实软件安装/UAC决策，不停止现有 Controller 或远程 Worker。
- 不改变远程 TaskSpec、签名、审批和允许安装模板边界。
- GUI 最小 720×560，100%/150%/200% DPI；TUI 60×18 与 100×30。
- 本地任务显示默认20项、最多100项；事件与日志有界，终态不能被迟到事件覆盖。

## Task 1: Worker 本地进度与 helper 实时通道

**Files:** Create internal/worker/events/events.go, internal/worker/progress.go and focused tests; modify internal/worker/server.go, lifecycle.go, native.go, installers/download.go, privilege/helper.go; preserve existing Executor interface.

**Interfaces:**

```go
// internal/worker/events; no dependency on worker or privilege
type Progress struct {
 Stage string `json:"stage"`
 Message string `json:"message,omitempty"`
 CompletedBytes int64 `json:"completed_bytes,omitempty"`
 TotalBytes int64 `json:"total_bytes,omitempty"`
}
func Emit(emit func(string,string), p Progress) // reserved internal stream "progress"
func Decode(message string) (Progress,error) // stage allowlist, lengths and numeric validation
// worker
type LocalTaskView struct { Task protocol.Task; PackageName string; Progress events.Progress; Logs []protocol.LogEntry }
func (s *Server) LocalStatus() []LocalTaskView
func (s *Server) CancelLocalTask(id string) error
func (s *Server) CloseWithReason(reason string)
func (s *Server) EndReason() string
// installers.Downloader adds Progress func(stage string, completed,total int64)
```

- [ ] Write tests first: a signed dispatched task emits progress while its executor blocks; LocalStatus reports that stage before completion, retains final state despite late events, provides deep copies, supports cancellation and stable ordering. Inject the slow external executor only; use real Worker dispatch/signature paths.
- [ ] Run `go test ./internal/worker/... -count=1`; capture failures caused by missing progress/cancellation APIs.
- [ ] Implement bounded, sanitized progress decoding separate from log accounting. Stream log emitter remains the existing Executor contract. Stage strings: queued, inspecting, downloading, verifying_artifact, awaiting_uac, installing, verifying_version, cancelling and terminal task states.

```go
events.Emit(emit, events.Progress{Stage:"inspecting"})
events.Emit(emit, events.Progress{Stage:"installing"})
```

- [ ] Downloader tests use real httptest TLS streams. A deliberately larger size limit must not be displayed as total; unknown Content-Length stays unknown. Assert callbacks arrive before completion, limits/cancellation/hash verification remain enforced. Throttle updates without losing the final byte count.
- [ ] Add proof-authenticated helper `/events` with explicit small body limit, valid streams, claimed/not-finished state checks and serialized bounded forwarding. Helper posts live progress/log events during execution. Result replay must not duplicate streamed logs. Progress is informational only; it cannot alter execution request. Exercise endpoint auth, malformed/oversize input, callback-before-result and cancellation with local tests.
- [ ] Use CloseWithReason for expired, heartbeat_lost and controller_closed paths; Close retains compatibility and must not overwrite an already stored reason.
- [ ] Run focused tests and race; commit only task-owned files. Write task-1 report with RED/GREEN evidence and exact exported interfaces.

## Task 2: 共享客户端生命周期与视图模型

**Files:** Create internal/worker/client/{app.go,transport.go,view.go,app_test.go,view_test.go}; do not change cmd or UI packages yet.

**Interfaces:**

```go
type Options struct { PairingPath string; Connect Connector }
type Connector func(context.Context,protocol.PairingBundle,[]byte)(Connection,error)
type Connection interface {
 Snapshot() []worker.LocalTaskView
 Session() protocol.Session
 CancelTask(string) error
 Disconnect(string)
 Done() <-chan struct{}
 Wait()
 EndReason() string
}
type App struct { /* owns context, connection, copied consent and transient key */ }
func New(ctx context.Context, opts Options) *App
func (a *App) LoadPairing(path string) error
func (a *App) ConsentAndConnect(key []byte) error // starts background connection, copies and clears transient input
func (a *App) Snapshot() Snapshot
func (a *App) CancelTask(id string) error
func (a *App) Disconnect(reason string)
func (a *App) Wait()
// Snapshot has State, Controller, Address, Scope []string, ExpiresAt,
// PairingExpiresAt, Authorization time.Duration, Tasks []worker.LocalTaskView,
// Error, Notice, EndReason. No pairing/session/auth token fields.
func TaskTitle(v worker.LocalTaskView) string
func StageText(stage string) string
func ProgressText(p events.Progress) string
func ErrorText(err error) string
```

- [ ] Write first tests proving no transport starts when merely loading or reading a pairing file, expired/malformed input is rejected, cancelled in-flight connection cannot become online, and secret input never appears in Snapshot or messages. Use temp JSON bundle fixtures with actual worker.ValidatePairing.
- [ ] Run `go test ./internal/worker/client -count=1`; record RED before implementing.
- [ ] Implement connection state machine: idle, ready, connecting, online, disconnected, error. LoadPairing validates one strict bounded JSON document and frozen scope. ConsentAndConnect validates ready and transient key then starts a background operation; Disconnect cancels any connection attempt immediately. Use generation identifiers to reject late completion; connection cleanup must still run if the connector returns after cancellation.
- [ ] Move tsnet registration/RPC/heartbeat and protected temporary directory ownership from existing cmd main into default Connector, preserving its security checks and deadlines. Connection cleanup closes Worker, waits, notifies Controller with bounded timeout, logs out tsnet and removes its owned temporary directory. Session closure must remain observable to GUI without exiting the application.
- [ ] Implement sanitized Chinese view helpers: download ratio uses TotalBytes only if positive; other stages use no percentage; errors include actionable next steps. Pass current catalogue display names into local task view if needed without changing the remote contract. Latest task ordering comes from Task 1.

```go
if p.TotalBytes <= 0 { return fmt.Sprintf("已下载 %.1f MB",float64(p.CompletedBytes)/(1<<20)) }
// The test fixture Progress{Stage:"installing",CompletedBytes:50,TotalBytes:100}
// must NOT be rendered as "50%".
```

- [ ] Test reset via LoadPairing after disconnect/error, failed connection cleanup, repeated Connect prevention, missing consent boundary and snapshots detached from caller mutations. Never automatically retry a consumed pairing.
- [ ] Run package tests/race; commit only client files. Report exact APIs for UI implementers.

## Task 3: 中文终端 TUI 与 plain 模式

**Files:** Create internal/worker/ui/tui/{model.go,view.go,plain.go,model_test.go}; modify go.mod/go.sum for pinned mature Bubble Tea/Lip Gloss dependencies only.

**Interfaces:** `func Run(ctx context.Context, app *client.App, plain bool) error`. Both UI modes use Task 2 App; they do not manage tsnet or share raw credentials via snapshots.

- [ ] Write model tests for visible consent, password masking, Ctrl+C cleanup during every step, task cancel selection, no shortcut execution during text entry, narrow layout width, and unknown progress. Test observable View and update effects, not library internals.
- [ ] Run package tests to see failures before code. Add pinned library dependencies with go get and inspect go.mod changes.
- [ ] Implement steps path input, scope summary/explicit consent, password input and connected dashboard. Enter must not silently consent while choosing a file. Paste-safe inputs; mask Auth Key and clear buffers after submitting. Use a tick to refresh copied snapshots and countdowns.
- [ ] Dashboard shows current software, ordered stages, downloaded bytes/progress, elapsed time, recent tasks and collapsed log panel. L toggles logs, C cancels selected task, D asks to disconnect, ? shows help; Ctrl+C always immediately calls Disconnect. Support task navigation and history expansion up to100; strip control characters from rendered logs and controller/catalogue values.
- [ ] TUI detects terminal capabilities; plain mode uses Chinese prompts and masked key via x/term.ReadPassword. Refuse redirected/noninteractive consent rather than auto-connect. Plain mode prints meaningful stage changes and final results, with emergency Ctrl+C handled by shared app.
- [ ] Test 60×18 and 100×30 views, Chinese cell width, NO_COLOR/TERM=dumb behavior and genuine password secrecy. Run tests/race, commit task-owned files and report.

## Task 4: Windows 桌面窗口

**Files:** Create internal/worker/ui/gui/{gui_windows.go,winapi_windows.go,paint_windows.go,layout.go,layout_test.go,gui_other.go,gui_windows_test.go}. May split files further by clear responsibility.

**Interfaces:** `func Run(ctx context.Context, app *client.App) error`; use existing x/sys/windows only; all Windows UI access on a locked OS thread.

- [ ] Write layout/state tests first: minimum window bounds, DPI scale, controls not overlapping/escaping, expired input disables connect, task choices remain isolated. Headless layout math tests should not launch a window; actual Windows smoke verifies real artifacts.
- [ ] Implement Win32 window and message loop with native file dialog, edit/password fields, consent checkbox, buttons, tab navigation and focus. GUI calls App methods only. A UI timer reads Snapshot; task execution/networking must never block the message loop.
- [ ] Draw tasteful navy/teal header and status badge, light background and white cards with readable Chinese fonts. Connecting/unknown installation uses indeterminate animation; downloads use actual ratio; idle view is friendly. Resize and DPI changes recompute layout; use DPI-aware Windows calls when available with fallback.
- [ ] Current task card shows title/stage/elapsed; recent tasks can be selected; detailed logs collapse/expand and scroll. Native controls need accessible captions, high-contrast readable states and keyboard actions. History selection supports up to100 local task views, default20.
- [ ] Closing invokes immediate Disconnect and exits after bounded application shutdown; clicking disconnect keeps results visible, offers new pairing and exit. Before resetting, never lose an active session silently. Clear password edit promptly on connect/error/close; don't display tokens, raw JSON or full installer commands.
- [ ] Test window creation, form input, selecting invalid file, consent rejection, and Close cleanup. Run real GUI at minimum/normal sizes and 100%/150%/200% equivalent layout, save screenshots. Simulate task snapshots through test-only harnesses; do not add a hidden production demo/auto-consent flag.
- [ ] Run package tests/vet, commit files and report.

## Task 5: Entrypoint、构建与端到端验收

**Files:** Modify cmd/worker/main.go/main_test.go, scripts/build.mjs and README.md; add worker client guide and test report; keep existing local diagnostic handler/tests compatible.

- [ ] Add CLI tests for parser behavior, old --pairing command, supported --ui modes and helper exclusivity. Watch failure before replacement entrypoint.
- [ ] Parse flags before creating UI. Build-time `defaultUI` is "tui" for console EXE and "gui" for GUI EXE. Handle --elevated-helper before terminal/window setup; reject conflicting arguments. Signal context is shared with App.

```go
var defaultUI = "tui" // overridden with -X main.defaultUI=gui
// --ui=gui|tui|plain; default from defaultUI
```

- [ ] Console artifact retains existing name; GUI artifact compiles same cmd with `-H=windowsgui -X main.defaultUI=gui` plus existing trimpath/strip flags and CGO_ENABLED=0. GUI error uses native message/error area instead of invisible stderr. Keep diagnostic local browser route available through an explicit flag or document retained handler for future diagnostic use; no default browser launch.
- [ ] Run `go test ./... -count=1`, `go test -race ./... -count=1`, `go vet ./...`, frontend tests/typecheck once after integration if shared dependencies/contracts affected. Build Worker and GUI Windows artifacts without replacing active Controller. Hash both and update SHA256SUMS consistently.
- [ ] Actual artifact smoke: help exits normally; GUI PE subsystem is Windows GUI; both file-selection/consent paths and TUI masking are exercised with test-only fixtures. Use inert test execution only, no software changes or real UAC decisions. Verify live Controller/remote Worker remains unaffected.
- [ ] Update README with GUI double-click steps and CLI TUI/plain commands, clear progress semantics and cancellation behavior. Record test evidence, screenshots and limitations in docs/test-plans/worker-client-2026-10-11.md.
- [ ] Commit scoped feature changes. Review whole feature for spec compliance and code quality, fix confirmed findings, then integrate onto current checkout preserving its existing uncommitted changes.
