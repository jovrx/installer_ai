package events

import (
	"strings"
	"testing"
)

func TestProgressValidationAndSanitization(t *testing.T) {
	for _, bad := range []string{
		`{"stage":"arbitrary_command"}`, `{"stage":"downloading","completed_bytes":-1}`,
		`{"stage":"downloading","completed_bytes":2,"total_bytes":1}`,
		`{"stage":"installing","total_bytes":12}`, `{"stage":"inspecting","command":"cmd"}`,
		`{"stage":"installing"} {}`, `{"stage":"installing","message":"` + strings.Repeat("x", 2049) + `"}`,
	} {
		if _, err := Decode(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	p, err := Decode(`{"stage":"downloading","message":"token=private\u001b[31m\nnext","completed_bytes":12}`)
	if err != nil {
		t.Fatal(err)
	}
	if p.CompletedBytes != 12 || p.TotalBytes != 0 || strings.Contains(p.Message, "private") || strings.ContainsAny(p.Message, "\x1b\n") {
		t.Fatalf("unsanitized progress: %+v", p)
	}
	var stream, message string
	Emit(func(s, m string) { stream, message = s, m }, Progress{Stage: "verifying_artifact"})
	if stream != "progress" {
		t.Fatal("wrong stream")
	}
	if p, err := Decode(message); err != nil || p.Stage != "verifying_artifact" {
		t.Fatalf("roundtrip %+v %v", p, err)
	}
}
