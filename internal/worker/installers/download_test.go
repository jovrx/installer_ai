package installers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"remoteops/pkg/protocol"
	"testing"
)

func TestArtifactIntegrity(t *testing.T) {
	data := []byte("installer bytes")
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	file := t.TempDir() + "/artifact"
	os.WriteFile(file, data, 0600)
	if err := ValidateArtifact(file, h); err != nil {
		t.Fatal(err)
	}
	if err := ValidateArtifact(file, string(make([]byte, 64))); err == nil {
		t.Fatal("invalid artifact accepted")
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://untrusted.invalid/payload", 302)
			return
		}
		w.Write(data)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	entry := protocol.CatalogEntry{URL: srv.URL + "/payload", SHA256: h, SizeBytes: int64(len(data)), AllowedHosts: []string{u.Hostname()}}
	dl := Downloader{Client: srv.Client(), FreeSpace: func(string) (uint64, error) { return 1 << 30, nil }}
	if _, err := dl.Download(context.Background(), entry, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	entry.SizeBytes = 2
	if _, err := dl.Download(context.Background(), entry, t.TempDir()); err == nil {
		t.Fatal("oversize download accepted")
	}
	entry.SizeBytes = 100
	entry.URL = srv.URL + "/redirect"
	if _, err := dl.Download(context.Background(), entry, t.TempDir()); err == nil {
		t.Fatal("untrusted redirect accepted")
	}
	entry.URL = "http://" + u.Host + "/payload"
	if _, err := dl.Download(context.Background(), entry, t.TempDir()); err == nil {
		t.Fatal("HTTP source accepted")
	}
}
