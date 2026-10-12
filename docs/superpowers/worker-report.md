# Worker implementation and verification

Task 3 is implemented in `internal/worker/**` and `cmd/worker/**`. Dependency files are managed by the coordinating agent and are excluded from the Worker commit.

## Interfaces and behavior

- `worker.New(Options)`, `Handler`, `Close`, `Done`, `Wait`; injectable `Executor.Execute(context.Context, TaskSpec, CatalogEntry, emit)` supports safe simulated execution.
- Signed `/health`, `POST /v1/tasks`, `GET /v1/tasks/{id}`, task `/logs` and `/cancel`, and `POST /v1/session/close`. Both request and response use `controller-to-worker`; registration/heartbeat/close use `worker-to-controller`.
- Session/device/scope/expiry, nonce replay, immutable task digest, approval proof, approval consumption, and idempotency are checked before execution. Concurrent duplicate installation dispatch returns the existing Task. Each device runs one task at a time; the session task cache is capped at 1,000 entries and rejects overflow instead of evicting install identity.
- Authenticated Controller heartbeats every 10 seconds, 45-second local disconnect TTL, local session expiry, Ctrl+C, console DISCONNECT and the local browser stop button all revoke RPC before task cancellation.
- Logs are redacted and bounded by encoded JSON size; pagination exposes truncation. Errors and helper output are also bounded. Timeouts and cancellation stop controlled installer process trees with fixed Windows taskkill arguments.
- Fixed Windows machine inventory reads OS, CPU, memory, disk, GPU/driver and only scoped software versions. Empty software scope reads no package inventory. Non-Windows native execution returns unsupported_platform.
- HTTPS artifact downloads validate redirects against catalog origins/allowlisted hosts, size, disk space and SHA-256. Controlled temporary directories have restrictive Windows ACLs. MSI, maintained Git EXE and pinned winget arguments are compiled into the Worker; no catalog command or remote script/argument facility exists. Exit 3010 records reboot required without rebooting; independent version verification must succeed.
- Actual elevation-token detection; ShellExecuteEx runas invokes this same EXE's helper with an ACL-protected descriptor path. Random proof and one-use request claim bind the helper to a validated task/catalog request over an ordinary loopback listener. The elevated helper has no remote listener and watches the local cancellation channel so it can terminate its own elevated installer tree. Elevated winget resolves a protected App Installer package rather than a user-writable execution alias.
- `worker --pairing <JSON file>` displays Controller, scope, packages, expiry and administrator implications, requires local CONNECT consent and hidden terminal Auth Key entry, then joins ephemeral tsnet using a session temporary directory. Registration response signature, device/session identity, exact scope and catalog digest are checked before trust. Local UI uses a random fragment token and Origin checks; secrets are not printed. Shutdown attempts Controller close, Tailscale Logout/Close and sensitive directory cleanup.

## Automated verification

Observed initial missing-implementation RED runs, plus behavioral RED failures for winget option injection, JSON log expansion and restricted-environment Windows inventory. Corresponding fixes produced GREEN results.

Passed on Windows amd64:

- `go test ./internal/worker/... ./cmd/worker -count=1`
- `go test -race ./internal/worker/... ./cmd/worker -count=1`
- `go vet ./internal/worker/... ./cmd/worker`
- `GOOS=windows GOARCH=amd64 go build ./cmd/worker`, output placed outside the repository.
- Built EXE `--help` smoke check.
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/worker` verifies portable test/integration compilation; native installation remains Windows-only.

Tests cover unsigned/wrong/replayed identity, altered scope/digest/approval, expired/lost sessions, concurrent install replay, changed idempotency parameters, cancel/timeout, secret redaction, encoded log limits, trusted TLS download/hash/size/redirect handling, maintained installer mapping, helper request binding, signed registration/consent mismatch and local stop token/Origin checks. Windows tests also run real read-only machine inspection and a harmless sleeping-process tree cancellation.

## Runtime acceptance still required

No real tailnet Auth Key, real pairing/ACL routing, ephemeral node removal, UAC approval/decline, MSI/Git/winget installation, real reboot-needed outcome or cross-NAT scenario was exercised. The tests inject installation execution or use harmless processes; builds do not prove these manual acceptance cases.

Version checking for generic MSI/winget catalog entries requires the catalog display name to match an installed Windows uninstall-registry DisplayName; Git uses its maintained binary/version probe. Missing/mismatched records produce verification_failed. Winget requires locally installed App Installer and an available protected package for elevated execution. An outstanding OS UAC dialog can remain visible after application revocation; a late approved helper cannot obtain its consumed/closed local request channel and cannot proceed with installation.
