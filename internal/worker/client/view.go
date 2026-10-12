package client

import (
	"errors"
	"strings"
	"time"
	"unicode"

	"remoteops/internal/i18n"
	"remoteops/internal/security"
	"remoteops/internal/worker"
	"remoteops/internal/worker/events"
)

// Snapshot is display data only. It never exposes the pairing bundle or credentials.
// State is idle, ready, connecting, online, disconnected or error.
type Snapshot struct {
	Language i18n.Language
	// Warning is a localized recoverable settings warning, independent of Error.
	Warning          string
	State            string
	Controller       string
	Address          string
	Scope            []string
	ExpiresAt        time.Time
	PairingExpiresAt time.Time
	Authorization    time.Duration
	Tasks            []worker.LocalTaskView
	Error            string
	Notice           string
	EndReason        string
}

func cleanText(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, security.Redact(s)))
}

// SafeText strips credential patterns and terminal/control characters from display text.
func SafeText(s string) string { return cleanText(s) }

func StateText(state string, language ...i18n.Language) string {
	switch state {
	case "idle":
		return localized(language, "worker.state.idle")
	case "ready":
		return localized(language, "worker.state.ready")
	case "connecting":
		return localized(language, "worker.state.connecting")
	case "online":
		return localized(language, "worker.state.online")
	case "disconnected":
		return localized(language, "worker.state.disconnected")
	case "error":
		return localized(language, "worker.state.error")
	default:
		return localized(language, "worker.state.unknown")
	}
}

// PrimaryTask selects an executing task, then a queued task, then the latest
// result. It preserves the supplied recent-first order within each group.
func PrimaryTask(tasks []worker.LocalTaskView) (worker.LocalTaskView, bool) {
	for _, v := range tasks {
		if v.Task.State == "running" {
			return v, true
		}
	}
	for _, v := range tasks {
		if v.Task.State == "queued" {
			return v, true
		}
	}
	if len(tasks) > 0 {
		return tasks[0], true
	}
	return worker.LocalTaskView{}, false
}

func TaskResultText(v worker.LocalTaskView, language ...i18n.Language) string {
	if !v.Task.State.Terminal() && v.Progress.Stage == "cancelling" {
		return StageText("cancelling", language...)
	}
	switch v.Task.State {
	case "running":
		if v.Progress.Stage != "" {
			return ProgressText(v.Progress, language...)
		}
		return StageText("running", language...)
	case "succeeded":
		if v.Task.Spec.Type == "inspect_system" {
			return localized(language, "worker.result.inspection")
		}
		text := localized(language, "worker.result.completed")
		if result := v.Task.Result; result != nil {
			if result.Verified {
				text = localized(language, "worker.result.verified")
			}
			if version := cleanText(result.Version); version != "" {
				text += localized(language, "worker.separator.detail") + version
			}
			if result.RebootRequired {
				text += localized(language, "worker.result.reboot")
			}
		}
		return text
	case "failed":
		text := localized(language, "worker.result.failed")
		if detail := cleanText(v.Task.ErrorMessage); detail != "" {
			text += localized(language, "worker.separator.detail") + detail
		}
		return text + localized(language, "worker.result.retry")
	default:
		return StageText(string(v.Task.State), language...)
	}
}

func TaskTitle(v worker.LocalTaskView, language ...i18n.Language) string {
	if v.Task.Spec.Type == "inspect_system" {
		return localized(language, "worker.title.inspect")
	}
	name := cleanText(v.PackageName)
	if name == "" {
		name = localized(language, "worker.title.install")
	}
	if p := v.Task.Spec.Package; p != nil {
		if version := cleanText(p.Version); version != "" {
			return name + " " + version
		}
	}
	return name
}

func StageText(stage string, language ...i18n.Language) string {
	switch stage {
	case "running":
		return localized(language, "worker.stage.running")
	case "queued":
		return localized(language, "worker.stage.queued")
	case "inspecting":
		return localized(language, "worker.stage.inspecting")
	case "downloading":
		return localized(language, "worker.stage.downloading")
	case "verifying_artifact":
		return localized(language, "worker.stage.verifying_artifact")
	case "awaiting_uac":
		return localized(language, "worker.stage.awaiting_uac")
	case "installing":
		return localized(language, "worker.stage.installing")
	case "verifying_version":
		return localized(language, "worker.stage.verifying_version")
	case "cancelling":
		return localized(language, "worker.stage.cancelling")
	case "succeeded":
		return localized(language, "worker.stage.succeeded")
	case "failed":
		return localized(language, "worker.stage.failed")
	case "cancelled":
		return localized(language, "worker.stage.cancelled")
	case "timed_out":
		return localized(language, "worker.stage.timed_out")
	default:
		return localized(language, "worker.stage.unknown")
	}
}

func onlineNotice(tasks []worker.LocalTaskView, language ...i18n.Language) string {
	if v, ok := PrimaryTask(tasks); ok && (v.Task.State == "running" || v.Task.State == "queued") {
		return TaskResultText(v, language...) + localized(language, "worker.notice.task")
	}
	return localized(language, "worker.notice.online")
}

func ProgressText(p events.Progress, language ...i18n.Language) string {
	if p.Stage != "downloading" {
		return StageText(p.Stage, language...)
	}
	completed := max(p.CompletedBytes, 0)
	if p.TotalBytes <= 0 {
		return localized(language, "worker.progress.bytes", float64(completed)/(1<<20))
	}
	completed = min(completed, p.TotalBytes)
	return localized(language, "worker.progress.ratio", 100*float64(completed)/float64(p.TotalBytes), float64(completed)/(1<<20), float64(p.TotalBytes)/(1<<20))
}

var (
	errConnection     = errors.New("connection_failed")
	errPairingFile    = errors.New("pairing_file")
	errPairingInvalid = errors.New("pairing_invalid")
	errPairingExpired = errors.New("pairing_expired")
	errNotReady       = errors.New("not_ready")
	errActive         = errors.New("active_connection")
	errKey            = errors.New("invalid_auth_key")
	errNotOnline      = errors.New("not_online")
	errTask           = errors.New("task_unavailable")
	errPlatform       = errors.New("unsupported_platform")
	errElevated       = errors.New("elevated_process")
)

// ErrorText deliberately maps unknown errors to a fixed message. Network and
// parser errors can contain arbitrary credentials, paths or server-controlled text.
func ErrorText(err error, language ...i18n.Language) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, errPairingExpired):
		return localized(language, "worker.error.pairing_expired")
	case errors.Is(err, errPairingFile):
		return localized(language, "worker.error.pairing_file")
	case errors.Is(err, errPairingInvalid):
		return localized(language, "worker.error.pairing_invalid")
	case errors.Is(err, errNotReady):
		return localized(language, "worker.error.not_ready")
	case errors.Is(err, errActive):
		return localized(language, "worker.error.active")
	case errors.Is(err, errKey):
		return localized(language, "worker.error.key")
	case errors.Is(err, errNotOnline):
		return localized(language, "worker.error.not_online")
	case errors.Is(err, errTask):
		return localized(language, "worker.error.task")
	case errors.Is(err, errPlatform):
		return localized(language, "worker.error.platform")
	case errors.Is(err, errElevated):
		return localized(language, "worker.error.elevated")
	default:
		return localized(language, "worker.error.connection")
	}
}

func normalizedReason(reason string) string {
	switch reason {
	case "expired", "heartbeat_lost", "controller_closed", "local_closed":
		return reason
	default:
		return "local_closed"
	}
}
func endNotice(reason string, language ...i18n.Language) string {
	var text string
	switch reason {
	case "expired":
		text = localized(language, "worker.end.expired")
	case "heartbeat_lost":
		text = localized(language, "worker.end.heartbeat_lost")
	case "controller_closed":
		text = localized(language, "worker.end.controller_closed")
	default:
		text = localized(language, "worker.end.local_closed")
	}
	return text + localized(language, "worker.end.detail")
}

func localized(language []i18n.Language, key string, args ...any) string {
	lang := i18n.Chinese
	if len(language) > 0 {
		lang = language[0]
	}
	return i18n.Text(lang, key, args...)
}
