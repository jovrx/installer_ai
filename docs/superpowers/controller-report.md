# Controller implementation report

Implementation commit: `0952920` (`feat: add durable controller approval and signed worker lifecycle`). Only `internal/controller/**` and `cmd/controller/**` were committed; dependency files remain root-owned.

Implemented all management paths from the frozen OpenAPI and the separate signed registration, heartbeat and Worker-close listener. SQLite migration enables foreign keys, WAL, busy timeout, durable single-use pairing, unique session/idempotency keys, transactional approval consumption, restart recovery, logs, audit, cleanup retries and retention. Nonces persist for the signature freshness window across restarts. Catalog and scope snapshots remain immutable within a paired session.

`Options` includes the planned fields plus `AgentToken`, `Now`, and `TickInterval`. `PeerResolver.Resolve(context.Context, *http.Request) (Peer,error)` returns `Peer{Address,NodeID}`; `NodeCleaner.DeleteNode(context.Context,string) error` deletes only the bound node. Function adapters support integration tests. Production uses Tailscale LocalAPI WhoIs, checks the observed IP against the returned node addresses, and constructs RPC URLs with the fixed `protocol.WorkerPort`. Direct transport disables environment proxies and redirects.

The operator token can log into Web; the separate MCP token cannot. Bearer credentials cannot approve or reject. Cookie writes require same-origin and CSRF. CLI requires `REMOTEOPS_ADMIN_TOKEN`; `REMOTEOPS_MCP_TOKEN` can be supplied separately or a per-run credential is atomically written to `--mcp-token-file` (default `remoteops-state/mcp.token`). Optional `REMOTEOPS_TAILSCALE_API_KEY` enables deletion; absent credentials leave cleanup visibly pending.

Close commits application revocation immediately and returns `cleanup_pending` for a bound node. `Start(ctx)` runs asynchronous cleanup with persisted backoff, scheduler polling, expiration and retention; `retry-cleanup` schedules the next immediate attempt. Slow dispatch, poll, cancellation, cleanup, and LocalAPI calls do not hold the management mutex. Results recheck current authority and task state before updating. Unknown tasks only poll their existing ID; a queued Worker acknowledgement remains dispatched and cannot return to the execution queue. Unknown outcomes block another task on the same device.

The executable defaults management to `127.0.0.1:8080`, requires an explicit tailnet IP in `--worker-listen`, and requires TLS for non-loopback management. The catalog defaults empty, permitting inspection only; `--catalog` loads an operator-maintained pinned JSON catalog. Root should wrap `s.Handler()` in its Web embedding handler after integration.

Verification passed:

- `go test -race ./internal/controller ./cmd/controller`
- `go vet ./internal/controller ./cmd/controller`
- Linux amd64 Controller build with CGO disabled
- macOS arm64 Controller build with CGO disabled
- Staged diff whitespace check

Tests cover pairing reuse/expiry, signed registration response identity, heartbeat nonce replay including restart, cross-device heartbeat, browser origin/CSRF, separate MCP identity, approval binding/single consumption, concurrent idempotency, changed parameters, close and heartbeat-expiry revocation, cleanup retry, restart unknown state, bounded redacted logs, unknown-result no-redispatch, and management/heartbeat progress while a dispatch blocks. The unknown-result and blocked-dispatch regressions failed before their fixes.

Live tailnet identity, real Tailscale node deletion, and operational TLS were not exercised. SQLite contains session communication secrets needed to sign Worker requests, so backup files require the same protection as the protected state directory. Cleanup and retention require `Start(ctx)` to be running. Scheduler calls are serialized with bounded client timeouts; they do not block operator authentication or heartbeats.

## Review fixes

- Task completion rereads the active session under the lock before changing its state. A regression blocked dispatch, delivered a newer signed heartbeat, then completed the task; it failed before the fix and now passes without overwriting the heartbeat.
- State defaults to `remoteops-state/remoteops.db`; generated MCP credentials default to `remoteops-state/mcp.token`. `SecureStateDirectory` creates and protects a new dedicated leaf directory, while an existing directory must pass permission inspection. Existing shared directories are rejected without changing their ACL or mode. Custom paths must name an existing private directory or a new leaf under an existing parent; symbolic links and Windows reparse points in state paths are rejected.
- Windows protection uses native `SetNamedSecurityInfo` to assign the current user as owner and a protected DACL granting access only to that user, Administrators and SYSTEM. Directory entries inherit to both files and subdirectories, protecting newly created SQLite WAL/SHM/journal files before their contents are written. Existing DB and sidecars are protected before opening SQLite. Generated token temporary files are protected before writing and then atomically renamed. Unix uses directory mode 0700 and file mode 0600.
- Audit context is request-scoped. Agent calls record `agent`, browser/operator calls `operator`, signed Worker registration/close `worker`, and lifecycle/cleanup work `system`; asynchronous cancellation retains its original request actor without a mutable global field.
- Fresh Windows race tests and vet passed, including native ACL inspection of the actual SQLite database/WAL/SHM, shared-parent rejection without ACL mutation, heartbeat preservation, and agent/worker/system audit attribution. Linux amd64 and macOS arm64 CGO-disabled builds passed after these changes.
