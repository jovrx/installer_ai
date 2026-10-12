package security

import "regexp"

var privateKey = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
var structuredSecret = regexp.MustCompile(`(?i)"(?:authorization|cookie|password|passwd|secret|token|api[_-]?key|auth[_-]?key)"\s*:\s*"(?:\\.|[^"\\])*"`)
var bearerSecret = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;"']+`)
var sensitive = regexp.MustCompile(`(?i)(authorization\s*:\s*|cookie\s*:\s*)[^\r\n]+|tskey-[a-zA-Z0-9_-]+|((?:password|passwd|secret|token|api[_-]?key|auth[_-]?key)\s*[=:]\s*)[^\s,;]+`)

func Redact(s string) string {
	s = privateKey.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	s = structuredSecret.ReplaceAllString(s, `"redacted":"[REDACTED]"`)
	s = bearerSecret.ReplaceAllString(s, "Bearer [REDACTED]")
	return sensitive.ReplaceAllString(s, "[REDACTED]")
}
