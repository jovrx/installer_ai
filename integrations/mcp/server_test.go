package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"remoteops/internal/i18n"
	"strings"
	"testing"
)

func TestLanguagePreservesToolContractsAndProtocolFraming(t *testing.T) {
	zh, en := ToolsForLanguage(i18n.Chinese), ToolsForLanguage(i18n.English)
	if len(zh) != 9 || len(en) != 9 {
		t.Fatal("tool count changed")
	}
	for index := range zh {
		if zh[index].Name != en[index].Name || !reflect.DeepEqual(zh[index].InputSchema, en[index].InputSchema) || !reflect.DeepEqual(zh[index].Annotations, en[index].Annotations) || zh[index].Description == en[index].Description {
			t.Fatalf("tool contract changed: %s", zh[index].Name)
		}
	}
	for _, lang := range []i18n.Language{i18n.Chinese, i18n.English} {
		s := NewWithLanguage("http://127.0.0.1:8080", "", nil, lang)
		var output bytes.Buffer
		if err := s.Serve(strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"get_task_status\",\"arguments\":{\"task_id\":\"bad/secret\"}}}\n"), &output); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
			if !json.Valid([]byte(line)) {
				t.Fatalf("non-protocol stdout: %q", line)
			}
		}
		if lang == i18n.English && !strings.Contains(output.String(), "Invalid task_id") {
			t.Fatalf("English human error missing: %s", output.String())
		}
		if strings.Contains(output.String(), "bad/secret") {
			t.Fatal("untrusted argument leaked")
		}
	}
}

func TestInitializeAndTools(t *testing.T) {
	s := New("http://127.0.0.1:8080", "test-token", nil)
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\"}}\n{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n")
	var output bytes.Buffer
	if err := s.Serve(input, &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("unexpected protocol output: %s", output.String())
	}
	var reply struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Result.Tools) != 9 {
		t.Fatalf("tools=%d", len(reply.Result.Tools))
	}
	for _, tool := range reply.Result.Tools {
		if strings.Contains(tool.Name, "shell") || tool.Name == "approve_task" {
			t.Fatalf("unsafe tool %s", tool.Name)
		}
	}
}

func TestToolsShareAuthenticatedAPI(t *testing.T) {
	var calls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing shared authentication")
		}
		if r.Method != "GET" || r.URL.Path != "/api/v1/devices" {
			t.Errorf("unexpected API call %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"dev_one"}]`))
	}))
	defer api.Close()
	s := New(api.URL, "secret", api.Client())
	result, err := s.Call(t.Context(), "list_devices", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result), "dev_one") || calls != 1 {
		t.Fatal("API result not returned")
	}
	if _, err = s.Call(t.Context(), "install_software", json.RawMessage(`{"spec":{"type":"inspect_system"},"idempotency_key":"abcdefgh"}`)); err == nil {
		t.Fatal("unapproved installation accepted")
	}
	if calls != 1 {
		t.Fatal("invalid install reached API")
	}
}

func TestToolErrorsAndUnknownArguments(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"code":"approval_required","message":"human confirmation required"}}`))
	}))
	defer api.Close()
	s := New(api.URL, "secret", api.Client())
	if _, err := s.Call(t.Context(), "list_devices", json.RawMessage(`{"command":"whoami"}`)); err == nil {
		t.Fatal("unknown tool parameter accepted")
	}
	_, err := s.Call(t.Context(), "list_devices", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "approval_required") {
		t.Fatalf("API error lost: %v", err)
	}
}
