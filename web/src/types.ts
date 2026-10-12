export type TaskType = 'inspect_system' | 'install_package'
export interface Scope { allowed_tasks: TaskType[]; allowed_packages: string[]; allow_admin: boolean }
export interface Session { id: string; operator: string; controller_name: string; scope: Scope; state: string; device_id?: string; created_at: string; pairing_expires_at: string; expires_at: string; last_heartbeat?: string; cleanup_error?: string }
export interface SystemInfo { os: string; hostname: string; cpu: string; memory_bytes: number; disks: { name: string; total_bytes: number; free_bytes: number }[]; gpus: { name: string; driver: string }[]; software: Record<string, string>; elevated: boolean }
export interface Device { id: string; session_id: string; hostname: string; os: string; client_version: string; address: string; online: boolean; connected_at: string; last_heartbeat: string; info?: SystemInfo }
export interface CatalogEntry { id: string; name: string; version: string; installer: string; url?: string; sha256?: string; size_bytes?: number; allowed_hosts?: string[]; winget_id?: string; source?: string; requires_admin: boolean }
export interface TaskSpec { session_id: string; device_id: string; type: TaskType; package?: { id: string; version: string }; timeout_seconds: number; requires_admin: boolean }
export interface TaskResult { exit_code?: number; reboot_required: boolean; verified: boolean; version?: string; info?: SystemInfo }
export interface Task { id: string; spec: TaskSpec; state: string; approval_id?: string; idempotency_key: string; digest: string; created_at: string; started_at?: string; finished_at?: string; result?: TaskResult; error_code?: string; error_message?: string }
export interface Plan { id: string; spec: TaskSpec; digest: string; catalog_digest: string; steps: string[]; created_at: string; expires_at: string }
export interface Approval { id: string; plan_id: string; session_id: string; device_id: string; digest: string; status: string; operator?: string; created_at: string; expires_at: string; consumed_at?: string }
export interface LogEntry { sequence: number; timestamp: string; stream: string; message: string }
export interface LogsPage { entries: LogEntry[]; next_after: number; truncated: boolean }
export interface AuditEvent { id: string; timestamp: string; actor: string; action: string; session_id?: string; device_id?: string; task_id?: string; result: string }
export interface Config { max_concurrency: number; task_timeout_seconds: number; pairing_ttl_seconds: number; session_ttl_seconds: number; heartbeat_ttl_seconds: number; log_max_bytes: number; retention_days: number }
export interface NetworkStatus { state: 'connected' | 'disconnected' | 'unavailable'; addresses: string[]; error?: string }
export interface PairingBundle { protocol_version: string; controller_name: string; controller_url: string; session_id: string; pairing_token: string; expires_at: string; authorization_seconds: number; scope: Scope; catalog: CatalogEntry[]; catalog_digest: string }
export interface CreateSessionResponse { session: Session; pairing: PairingBundle }
