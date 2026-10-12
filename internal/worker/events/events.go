// Package events defines informational internal execution progress, with no execution authority.
package events

import (
	"encoding/json"
	"errors"
	"io"
	"remoteops/internal/security"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Progress struct {
	Stage          string `json:"stage"`
	Message        string `json:"message,omitempty"`
	CompletedBytes int64  `json:"completed_bytes,omitempty"`
	TotalBytes     int64  `json:"total_bytes,omitempty"`
}

func Decode(message string) (Progress, error) {
	var p Progress
	if len(message) > 4096 || !utf8.ValidString(message) {
		return p, errors.New("progress exceeds limit")
	}
	dec := json.NewDecoder(strings.NewReader(message))
	dec.DisallowUnknownFields()
	if dec.Decode(&p) != nil || dec.Decode(new(any)) != io.EOF {
		return Progress{}, errors.New("invalid progress")
	}
	switch p.Stage {
	case "queued", "inspecting", "downloading", "verifying_artifact", "awaiting_uac", "installing", "verifying_version", "cancelling", "succeeded", "failed", "cancelled", "timed_out", "unknown":
	default:
		return Progress{}, errors.New("invalid progress stage")
	}
	if len(p.Message) > 2048 || p.CompletedBytes < 0 || p.TotalBytes < 0 || p.TotalBytes > 4<<30 || p.CompletedBytes > 4<<30 || (p.TotalBytes > 0 && p.CompletedBytes > p.TotalBytes) || (p.Stage != "downloading" && (p.CompletedBytes != 0 || p.TotalBytes != 0)) {
		return Progress{}, errors.New("invalid progress values")
	}
	p.Message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, security.Redact(p.Message))
	return p, nil
}

func Emit(emit func(string, string), p Progress) {
	if emit == nil {
		return
	}
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	clean, err := Decode(string(b))
	if err != nil {
		return
	}
	b, _ = json.Marshal(clean)
	emit("progress", string(b))
}
