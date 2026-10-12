// Package protocol is the versioned, shared Controller/Worker contract.
package protocol

import "time"

const Version = "v1"
const MaxBodyBytes = 1 << 20
const WorkerPort = 9081

type TaskType string

const (
	InspectSystem  TaskType = "inspect_system"
	InstallPackage TaskType = "install_package"
)

type SessionState string

const (
	PendingPair    SessionState = "pending_pair"
	Connecting     SessionState = "connecting"
	Online         SessionState = "online"
	Running        SessionState = "running"
	Revoking       SessionState = "revoking"
	Closed         SessionState = "closed"
	CleanupPending SessionState = "cleanup_pending"
	Expired        SessionState = "expired"
	Failed         SessionState = "failed"
)

type TaskState string

const (
	PendingApproval TaskState = "pending_approval"
	Queued          TaskState = "queued"
	TaskRunning     TaskState = "running"
	Succeeded       TaskState = "succeeded"
	TaskFailed      TaskState = "failed"
	Cancelled       TaskState = "cancelled"
	TimedOut        TaskState = "timed_out"
	Unknown         TaskState = "unknown"
)

func (s TaskState) Terminal() bool {
	return s == Succeeded || s == TaskFailed || s == Cancelled || s == TimedOut
}
func (s SessionState) Active() bool { return s == Online || s == Running }

type Scope struct {
	AllowedTasks    []TaskType `json:"allowed_tasks"`
	AllowedPackages []string   `json:"allowed_packages"`
	AllowAdmin      bool       `json:"allow_admin"`
}
type Config struct {
	MaxConcurrency      int `json:"max_concurrency"`
	TaskTimeoutSeconds  int `json:"task_timeout_seconds"`
	PairingTTLSeconds   int `json:"pairing_ttl_seconds"`
	SessionTTLSeconds   int `json:"session_ttl_seconds"`
	HeartbeatTTLSeconds int `json:"heartbeat_ttl_seconds"`
	LogMaxBytes         int `json:"log_max_bytes"`
	RetentionDays       int `json:"retention_days"`
}

type NetworkStatus struct {
	State     string   `json:"state"`
	Addresses []string `json:"addresses"`
	Error     string   `json:"error,omitempty"`
}

func DefaultConfig() Config { return Config{3, 1800, 600, 3600, 45, MaxBodyBytes, 30} }

type Session struct {
	ID               string       `json:"id"`
	Operator         string       `json:"operator"`
	ControllerName   string       `json:"controller_name"`
	Scope            Scope        `json:"scope"`
	State            SessionState `json:"state"`
	DeviceID         string       `json:"device_id,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
	PairingExpiresAt time.Time    `json:"pairing_expires_at"`
	ExpiresAt        time.Time    `json:"expires_at"`
	LastHeartbeat    time.Time    `json:"last_heartbeat,omitempty"`
	ClosedAt         *time.Time   `json:"closed_at,omitempty"`
	NodeID           string       `json:"node_id,omitempty"`
	CleanupError     string       `json:"cleanup_error,omitempty"`
}
type Device struct {
	ID            string      `json:"id"`
	SessionID     string      `json:"session_id"`
	Hostname      string      `json:"hostname"`
	OS            string      `json:"os"`
	ClientVersion string      `json:"client_version"`
	Address       string      `json:"address"`
	Online        bool        `json:"online"`
	ConnectedAt   time.Time   `json:"connected_at"`
	LastHeartbeat time.Time   `json:"last_heartbeat"`
	Info          *SystemInfo `json:"info,omitempty"`
}
type SystemInfo struct {
	OS          string            `json:"os"`
	Hostname    string            `json:"hostname"`
	CPU         string            `json:"cpu"`
	MemoryBytes uint64            `json:"memory_bytes"`
	Disks       []DiskInfo        `json:"disks"`
	GPUs        []GPUInfo         `json:"gpus"`
	Software    map[string]string `json:"software"`
	Elevated    bool              `json:"elevated"`
}
type DiskInfo struct {
	Name       string `json:"name"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
}
type GPUInfo struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
}
type CatalogEntry struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Installer     string   `json:"installer"`
	URL           string   `json:"url,omitempty"`
	SHA256        string   `json:"sha256,omitempty"`
	SizeBytes     int64    `json:"size_bytes,omitempty"`
	AllowedHosts  []string `json:"allowed_hosts,omitempty"`
	WingetID      string   `json:"winget_id,omitempty"`
	Source        string   `json:"source,omitempty"`
	RequiresAdmin bool     `json:"requires_admin"`
}
type PackageRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}
type TaskSpec struct {
	SessionID      string      `json:"session_id"`
	DeviceID       string      `json:"device_id"`
	Type           TaskType    `json:"type"`
	Package        *PackageRef `json:"package,omitempty"`
	TimeoutSeconds int         `json:"timeout_seconds"`
	RequiresAdmin  bool        `json:"requires_admin"`
}
type Task struct {
	ID             string      `json:"id"`
	Spec           TaskSpec    `json:"spec"`
	State          TaskState   `json:"state"`
	ApprovalID     string      `json:"approval_id,omitempty"`
	IdempotencyKey string      `json:"idempotency_key"`
	Digest         string      `json:"digest"`
	CreatedAt      time.Time   `json:"created_at"`
	StartedAt      *time.Time  `json:"started_at,omitempty"`
	FinishedAt     *time.Time  `json:"finished_at,omitempty"`
	Result         *TaskResult `json:"result,omitempty"`
	ErrorCode      string      `json:"error_code,omitempty"`
	ErrorMessage   string      `json:"error_message,omitempty"`
}
type TaskResult struct {
	ExitCode       *int        `json:"exit_code,omitempty"`
	RebootRequired bool        `json:"reboot_required"`
	Verified       bool        `json:"verified"`
	Version        string      `json:"version,omitempty"`
	Info           *SystemInfo `json:"info,omitempty"`
}
type Plan struct {
	ID            string    `json:"id"`
	Spec          TaskSpec  `json:"spec"`
	Digest        string    `json:"digest"`
	CatalogDigest string    `json:"catalog_digest"`
	Steps         []string  `json:"steps"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}
type Approval struct {
	ID         string     `json:"id"`
	PlanID     string     `json:"plan_id"`
	SessionID  string     `json:"session_id"`
	DeviceID   string     `json:"device_id"`
	Digest     string     `json:"digest"`
	Status     string     `json:"status"`
	Operator   string     `json:"operator,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
}
type LogEntry struct {
	Sequence  int64     `json:"sequence"`
	Timestamp time.Time `json:"timestamp"`
	Stream    string    `json:"stream"`
	Message   string    `json:"message"`
}
type LogsPage struct {
	Entries   []LogEntry `json:"entries"`
	NextAfter int64      `json:"next_after"`
	Truncated bool       `json:"truncated"`
}
type AuditEvent struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	SessionID string    `json:"session_id,omitempty"`
	DeviceID  string    `json:"device_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	Result    string    `json:"result"`
}
type PairingBundle struct {
	ProtocolVersion      string         `json:"protocol_version"`
	ControllerName       string         `json:"controller_name"`
	ControllerURL        string         `json:"controller_url"`
	SessionID            string         `json:"session_id"`
	PairingToken         string         `json:"pairing_token"`
	ExpiresAt            time.Time      `json:"expires_at"`
	AuthorizationSeconds int            `json:"authorization_seconds"`
	Scope                Scope          `json:"scope"`
	Catalog              []CatalogEntry `json:"catalog"`
	CatalogDigest        string         `json:"catalog_digest"`
}
type CreateSessionRequest struct {
	Scope Scope `json:"scope"`
}
type CreateSessionResponse struct {
	Session Session       `json:"session"`
	Pairing PairingBundle `json:"pairing"`
}
type RegisterRequest struct {
	ProtocolVersion string `json:"protocol_version"`
	SessionID       string `json:"session_id"`
	PairingToken    string `json:"pairing_token"`
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	ClientVersion   string `json:"client_version"`
	Port            int    `json:"port"`
}
type RegisterResponse struct {
	Session      Session        `json:"session"`
	Device       Device         `json:"device"`
	SessionToken string         `json:"session_token"`
	Catalog      []CatalogEntry `json:"catalog"`
}
type HeartbeatRequest struct {
	SessionID string `json:"session_id"`
	DeviceID  string `json:"device_id"`
}
type CreateTaskRequest struct {
	Spec           TaskSpec `json:"spec"`
	ApprovalID     string   `json:"approval_id,omitempty"`
	IdempotencyKey string   `json:"idempotency_key"`
}
type CreateApprovalRequest struct {
	PlanID string `json:"plan_id"`
}
type TaskDispatch struct {
	Task           Task   `json:"task"`
	ApprovalDigest string `json:"approval_digest,omitempty"`
}
type LoginRequest struct {
	Token string `json:"token"`
}
type LoginResponse struct {
	CSRFToken string `json:"csrf_token"`
}
type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func (e APIError) Error() string { return e.Code + ": " + e.Message }

type ErrorEnvelope struct {
	Error APIError `json:"error"`
}
