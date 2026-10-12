# RemoteOps MVP Implementation Plan

> For agentic workers: use superpowers:subagent-driven-development for implementation and reviews. Independent modules may run in separate worktrees after the shared contract is committed, per the approved design.

**Goal:** Deliver the PRD Phase 1 controlled Windows installation workflow with Controller, Worker, MCP and Web.

**Architecture:** Go programs share protocol DTOs and signed transport. SQLite persists Controller state; tsnet supplies Worker connectivity. Vue and stdio MCP call the same management API.

**Tech Stack:** Go 1.26, modernc.org/sqlite, tailscale.com/tsnet, Vue 3, TypeScript, Vite.

**Spec:** `docs/superpowers/specs/2026-10-11-remoteops-mvp-design.md`.

## Global Constraints

- B: Windows 10/11 x64; A: macOS/Linux, with Windows development support.
- Default management endpoint: `127.0.0.1:8080`; no unauthenticated management or arbitrary shell.
- Pairing 10 minutes; authorization 60 minutes; heartbeat 10 seconds; disconnect TTL 45 seconds.
- Max concurrency 3; default task timeout 1800 seconds; maximum body/log bytes 1048576.
- Only inspect_system and install_package tasks; software catalog immutable during each session.
- Web operator alone approves; MCP cannot approve; approval binds all execution parameters.
- Credentials stay out of logs and binaries; paired private requests and responses signed with HMAC-SHA256.
- Unknown installation outcomes are queried, never automatically re-executed.
- Real tailnet, MSI installation and UAC acceptance must be reported separately from simulation/build checks.

## Task 1: Shared contract and security

**Files:** `go.mod`, `pkg/protocol/{types,validate}.go`, `internal/security/{signing,redact}.go`, their tests, `api/openapi.yaml`.

**Interfaces:** module `remoteops`; protocol Session, Device, TaskSpec, Task, Plan, Approval, CatalogEntry, Config, PairingBundle, RegisterRequest/Response, HeartbeatRequest, TaskDispatch, TaskResult, LogEntry and APIError. Security NewID(prefix), Digest(value), Redact(text), SignRequest, VerifyRequest, SignResponse and VerifyResponse. See committed Go definitions for exact field types and transport headers; agents may not change them independently.

- [ ] Write security tests verifying a valid signed request, modified body rejection, nonce replay rejection, signed-response tampering rejection and redaction.
- [ ] Run `go test ./internal/security ./pkg/protocol`; observe missing implementation failures.
- [ ] Implement canonical signatures, replay cache, JSON validation and immutable task digest.
- [ ] Run tests and `go vet` for these packages, then commit frozen contract.

Required test assertions:
```go
if err := verifier.Verify(req, alteredBody); err == nil { t.Fatal("tampered body accepted") }
if err := verifier.Verify(req, body); err == nil { t.Fatal("nonce replay accepted") }
if strings.Contains(Redact("Authorization: Bearer secret"), "secret") { t.Fatal("credential leaked") }
```

## Task 2: Controller and SQLite

**Files:** `internal/controller/*`, `cmd/controller/*`, Controller tests. Own only these paths; root owns shared protocol, dependencies, web embedding and end-to-end tests.

**Consumes:** frozen protocol/security. **Produces:** `controller.New(Options) (*Server,error)`, `Server.Handler() http.Handler`, `Server.WorkerHandler() http.Handler`, `Server.Start(ctx)` and `Server.Close() error`. Options supplies DBPath, AdminToken, ControllerName, ControllerURL, Catalog, HTTPClient, PeerResolver and NodeCleaner. Injectable peer resolver returns validated RPC address/node identity; no user-selected arbitrary URL.

- [ ] Write integration-style HTTP tests with temporary SQLite: login/CSRF, one-use pairing, expiration, approval-required installation, changed-parameter rejection, task idempotency, close revocation and restart state.
- [ ] Run tests, implement migration/store/auth/API and lifecycle with explicit transactions.
- [ ] Implement signed Worker calls, scheduler polling, limits, heartbeat expiry, bounded logs, task cancellation and cleanup retries.
- [ ] Confirm only Cookie-authenticated operator can approve; bearer MCP receives forbidden.
- [ ] Run `go test ./internal/controller/...` and commit.

Required behavior:
```go
// Same idempotency key and identical approved spec returns the existing task.
if first.ID != second.ID { t.Fatal("duplicate execution scheduled") }
// Closing a session makes a new task fail even while node deletion fails.
if response.StatusCode != http.StatusConflict { t.Fatal("closed session accepted task") }
```

## Task 3: Worker runtime, installers and Windows permissions

**Files:** `internal/worker/*`, `cmd/worker/*`, worker/installer tests. Own only these paths.

**Consumes:** frozen DTOs/security and catalog supplied by pairing. **Produces:** `worker.New(Options) (*Server,error)`, `Handler() http.Handler`, `Close()`, inspect/install executor; tsnet-backed executable and local consent/stop UI.

- [ ] Write tests for signature/scope/expiry rejection, concurrent duplicate task submission, cancel/timeout, digest mismatch and installer download integrity.
- [ ] Run tests; implement signed RPC, per-session cache, bounded logs and execution concurrency.
- [ ] Implement Windows system inspection, maintained MSI/Git EXE/winget mappings, trusted downloads, digest verification and post-install version checks.
- [ ] Implement token elevation detection and local-only UAC helper with one-use request proof; reject arbitrary executable/arguments and clean temporary files.
- [ ] Implement tsnet Ephemeral, secure terminal credential entry, signed registration/heartbeats, local consent UI and immediate disconnect.
- [ ] Run worker tests and `GOOS=windows GOARCH=amd64 go build ./cmd/worker`; commit.

Required behavior:
```go
if executorCalls.Load() != 1 { t.Fatal("replayed install executed") }
if err := validateArtifact(file, wrongHash); err == nil { t.Fatal("invalid artifact accepted") }
```

## Task 4: Vue management UI

**Files:** `web/{package.json,package-lock.json,index.html,vite.config.ts,tsconfig*.json,src/*}`. Own only web.

**Consumes:** frozen management API. Responses return direct DTOs, collections use JSON arrays. Login POST `/api/v1/auth/login` with token returns csrf_token; subsequent same-origin requests use Cookie and X-CSRF-Token. Lists: sessions/devices/tasks/plans/approvals/catalog/audit-events. Config GET/PUT. Plan POST uses TaskSpec; approval POST uses plan_id; approve/reject POST accepts empty object; task POST uses spec, approval_id and idempotency_key. Logs GET returns entries,next_after,truncated.

- [ ] Build login, responsive navigation and empty/loading/error states.
- [ ] Build sessions with consent scope, expiry, pairing bundle export, close and cleanup retry.
- [ ] Build devices/info, task plan/request/approve/execute, status, logs and cancellation.
- [ ] Build config and audit views; credentials remain outside localStorage and query strings.
- [ ] Run `npm ci`, `npm run typecheck` and `npm run build`; commit.

## Task 5: MCP and executable integration

**Files:** `integrations/mcp/*`, `cmd/remoteops-mcp/*`, `internal/webui/*`, executable integration tests.

**Consumes:** management HTTP API; web/dist assets. **Produces:** stdio MCP implementing initialize/tools/list/tools/call and nine structured tools; embedded management frontend.

- [ ] Write tests for MCP tool schemas, authorization, calls and error propagation.
- [ ] Run tests; implement bounded JSON-RPC stdio framing, protocol handshake and API client.
- [ ] Map list_devices/get_device_info/create_install_plan/request_task_approval/install_software/get_task_status/get_task_logs/cancel_task/close_session. Never provide approval or arbitrary command tool.
- [ ] Embed Vue assets in Controller with SPA fallback and correct cache/content headers.
- [ ] Run MCP tests, web build and executable smoke checks.

## Task 6: Integration, review and release

**Files:** `tests/integration/*`, `.github/workflows/ci.yml`, `scripts/*`, `README.md`, `docs/{security,test-plans,architecture}/*`.

- [ ] Write full flow tests against real SQLite and simulated signed Worker: pair → inspect → approve → install fake → logs → close → refusal.
- [ ] Add fault tests for heartbeat expiry, crash/restart, cleanup retry, cross-device access and unknown-task recovery.
- [ ] Run `go test ./...`, `go vet ./...`, race tests where supported, Vue build/typecheck and all required cross-builds.
- [ ] Review the final diff for signature boundaries, unsafe installation inputs, credential leaks, approval and revocation races; fix and rerun affected checks.
- [ ] Add reproducible build scripts, pinned dependencies, checksum output, ACL/deployment guides and manual acceptance matrix.
- [ ] Integrate local commits into the user's checkout; do not push, publish or connect real devices without a specific request.

## Preflight review

| Boundary | Compatibility |
|---|---|
| Task 1 → 2/3/5 | Shared DTOs/signatures frozen before module work |
| Task 2 ↔ 3 | Signed RPC paths, result/log schema and same request proof |
| Task 2 ↔ 4/5 | Direct DTO responses, strict management auth, operator-only approval |
| Task 4 → 5 | Web assets copied into embedding package after successful build |
| Task 2/3 → 6 | Injected transport, executor and cleanup enable safe simulated acceptance |

Ruling: independent modules run in isolated worktrees once Task 1 is committed; this follows the PRD parallel-development boundary and prevents shared-file conflicts. Root exclusively manages dependency files and shared contracts.
