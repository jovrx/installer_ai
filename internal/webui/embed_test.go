package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedUIAndAPIBoundary(t *testing.T) {
	called := false
	handler := Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(401) }))
	api := httptest.NewRecorder()
	handler.ServeHTTP(api, httptest.NewRequest("GET", "/api/v1/devices", nil))
	if !called || api.Code != 401 {
		t.Fatal("UI bypassed API authentication")
	}
	index := httptest.NewRecorder()
	handler.ServeHTTP(index, httptest.NewRequest("GET", "/", nil))
	if index.Code != 200 || !strings.Contains(index.Body.String(), "id=\"app\"") {
		t.Fatalf("embedded Vue app missing: status %d body %s", index.Code, index.Body.String())
	}
	if index.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("UI clickjacking protection missing")
	}
	var script string
	fs.WalkDir(assets, "assets", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".js") {
			script = "/" + strings.TrimPrefix(path, "assets/")
		}
		return nil
	})
	if script == "" {
		t.Fatal("production JS asset missing")
	}
	js := httptest.NewRecorder()
	handler.ServeHTTP(js, httptest.NewRequest("GET", script, nil))
	if js.Code != 200 || !strings.Contains(js.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("asset did not load: %d %s", js.Code, script)
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest("GET", "/assets/missing.js", nil))
	if missing.Code != 404 {
		t.Fatal("missing asset became SPA HTML")
	}
}
