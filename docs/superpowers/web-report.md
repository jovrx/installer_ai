# RemoteOps Web delivery report

Branch: `feat/remoteops-web`. Scope: `web/**` plus this requested report. Shared Go protocol, API definitions and Go dependencies were not changed.

## Delivered behavior

- Vue 3 / TypeScript / Vite management UI in Chinese, with responsive sidebar navigation, mobile horizontal navigation, empty/loading/error states and five views: devices, sessions, tasks/approvals, configuration and audit.
- Login exchanges the management token for an operator Cookie and in-memory CSRF proof. Token input clears before the request completes. All API calls use same-origin credentials; writes carry `X-CSRF-Token`. No bearer authentication, localStorage, sessionStorage or credential query strings are used. Logout and expired authentication clear the CSRF proof and pairing bundle.
- Sessions support inspection/install scopes, selected catalog packages and explicit admin permission. Creation presents pairing expiry and exports the returned PairingBundle as JSON; credentials never appear in page text. Bundle exists only in page memory until cleared, expired or logged out. Closing immediately requests authorization revocation; cleanup-pending rows show diagnostics and manual retry.
- Device details show OS, CPU/memory, disk capacity, GPU/driver, software versions, permission state and heartbeat. Active authorized devices can start an inspection or installation plan.
- Plans show device/session IDs, package/version, timeout, admin requirement, execution steps and immutable digests. Each plan has a distinct request-approval, operator approve/reject and execute action. Approval uses an explicit confirmation dialog showing execution parameters. Task creation sends the original plan spec, approval ID and a stable per-plan idempotency key, including after a transport failure. Consumed approvals remain unusable even when their status is still `approved`.
- Task list and detail provide status, installation exit code, independent version verification, reboot hint, inspection result, errors, paginated logs and cancellation. Unknown outcomes are clearly marked and are not automatically re-executed. Log cursor advances using `next_after`; duplicate sequences are filtered. Backend truncation and the current visible entry count are displayed.
- Configuration inputs match protocol.ValidateConfig ranges. Local unsaved edits survive polling. Audit rows show actor/action, related resources and result. Polling runs at 5-second intervals and stops after logout/unmount.

## API assumptions

- Same-origin `/api/v1` API, as frozen by `api/openapi.yaml` and `pkg/protocol/types.go`. Development Vite proxy targets `http://127.0.0.1:8080` and binds loopback.
- Login POST `{token}` returns `{csrf_token}` and sets the operator Cookie; logout POST `{}` clears that cookie. Page reload requires another login because no CSRF recovery endpoint is defined.
- Lists return direct JSON arrays (Go nil arrays are handled as empty collections). Individual endpoints return direct DTOs. Create-session returns `{session,pairing}`.
- Plan POST body is a direct TaskSpec; approval POST body is `{plan_id}`; approval approve/reject bodies are `{}`. Task POST body is `{spec,approval_id,idempotency_key}`. POST close/retry-cleanup/cancel bodies are `{}`. Config PUT is the complete Config DTO.
- Approval statuses supported: `pending` (also tolerant of `pending_approval`), `approved`, `rejected`, `consumed`; `consumed_at` independently disables execution.
- Task logs GET uses `after` and `limit=100`, and returns `{entries,next_after,truncated}`. Up to the latest 3,000 received entries remain in browser memory; backend log cap remains authoritative.
- API errors use `{error:{code,message,request_id}}`; request IDs remain visible for diagnosis. Authentication 401 returns to login. Frontend restrictions complement Controller checks; Cookie-only approval and digest/idempotency enforcement must still be enforced server-side.
- Session package catalog is immutable on the server. The UI uses the management catalog for planning and relies on server rejection if a later Controller catalog configuration differs from the session catalog.

## Validation

On Node `v22.22.1`, npm `10.9.4`, Windows x64:

- `npm ci --no-audit --no-fund`: exit 0, 146 packages installed from the default registry. An initial attempt while the local Vite server was running hit Windows' esbuild executable lock; stopping that worktree's dev process allowed clean installation.
- `npm run typecheck`: exit 0.
- `npm test`: exit 0; 2 files, 7 tests. Real HTTP API boundary tests verify CSRF without bearer credentials, direct arrays and structured server diagnostics. Component tests verify login cleanup/error rendering, explicit approval, frozen execution parameters, stable retry idempotency and consumed-approval gating.
- `npm run build`: exit 0; 13 transformed modules. Output: `web/dist/index.html`, `web/dist/favicon.svg`, `web/dist/assets/index-CEpM-5i4.js` (105,464 bytes) and `web/dist/assets/index-DY9g4lFp.css` (17,312 bytes). Assets are ignored in Git and ready for root-agent embedding.
- Playwright fixture smoke: desktop and 390×844 viewport; login token input removed, all five views navigable without document horizontal overflow, unknown-task diagnostic and cursor logs visible. Inspected screenshots are local ignored artifacts in `web/test-artifacts/`.
- Production source check found no localStorage/sessionStorage/Authorization/Bearer references; `git diff --check` passed.

Browser smoke uses complete simulated API DTOs, not a running real Controller/Worker. Real tailnet, Windows installation and UAC acceptance were not exercised by this task.

## Integration follow-up

The final UI binds pairing duration to required authorization_seconds and shows Tailscale status from GET /api/v1/network-status with isolated failure handling. Pinned packages updated to Vue 3.5.43, Vite 7.3.7, Happy DOM 20.14.6 and Vitest 4.1.11. Fresh clean npm ci/typecheck/build and 10 tests passed; npm audit reported zero vulnerabilities. Final JavaScript asset is index-ofkKauQ4.js. Root also verified login, session creation, exact duration, status and audit against a running real Controller with SQLite; no real Worker installation was run.
