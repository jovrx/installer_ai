package installers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"remoteops/pkg/protocol"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDownloadReportsReceivedBytesBeforeCompletion(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(strconv.FormatBool(known), func(t *testing.T) {
			data := strings.Repeat("a", 64<<10)
			sum := sha256.Sum256([]byte(data))
			release := make(chan struct{})
			defer close(release)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if known {
					w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				}
				w.Write([]byte(data[:32<<10]))
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Write([]byte(data[32<<10:]))
			}))
			defer srv.Close()
			type event struct {
				stage       string
				done, total int64
			}
			received := make(chan event, 100)
			dl := Downloader{Client: srv.Client(), FreeSpace: func(string) (uint64, error) { return 1 << 30, nil }, Progress: func(stage string, done, total int64) { received <- event{stage, done, total} }}
			ended := make(chan error, 1)
			dir := t.TempDir()
			go func() {
				_, err := dl.Download(context.Background(), protocol.CatalogEntry{URL: srv.URL, SHA256: hex.EncodeToString(sum[:]), SizeBytes: 128 << 10}, dir)
				ended <- err
			}()
			deadline := time.After(3 * time.Second)
			for {
				select {
				case p := <-received:
					if p.stage != "downloading" || p.done == 0 {
						continue
					}
					want := int64(0)
					if known {
						want = int64(len(data))
					}
					if p.total != want || p.done >= int64(len(data)) {
						t.Fatalf("early progress %+v", p)
					}
					goto streamed
				case err := <-ended:
					t.Fatalf("completed before streaming: %v", err)
				case <-deadline:
					t.Fatal("no live bytes")
				}
			}
		streamed:
			release <- struct{}{}
			if err := <-ended; err != nil {
				t.Fatal(err)
			}
			var final event
			verification := false
			for len(received) > 0 {
				p := <-received
				if p.stage == "downloading" {
					final = p
				}
				if p.stage == "verifying_artifact" {
					verification = true
				}
			}
			if final.done != int64(len(data)) || !verification {
				t.Fatalf("final bytes %+v verification %v", final, verification)
			}
		})
	}
}

func TestDownloadProgressPreservesCancellationAndSizeLimits(t *testing.T) {
	for _, mode := range []string{"cancel", "limit", "hash", "verify_cancel"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.(http.Flusher).Flush()
				w.Write([]byte("first"))
				w.(http.Flusher).Flush()
				if mode == "cancel" {
					<-r.Context().Done()
					return
				}
				w.Write([]byte("extra"))
			}))
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sum := sha256.Sum256([]byte("firstextra"))
			hash := hex.EncodeToString(sum[:])
			max := int64(100)
			if mode == "limit" {
				max = 5
			}
			if mode == "hash" {
				hash = strings.Repeat("0", 64)
			}
			dl := Downloader{Client: srv.Client(), FreeSpace: func(string) (uint64, error) { return 1 << 30, nil }, Progress: func(stage string, n, total int64) {
				if mode == "cancel" && n > 0 {
					cancel()
				}
				if mode == "verify_cancel" && stage == "verifying_artifact" {
					cancel()
				}
			}}
			dir := t.TempDir()
			_, err := dl.Download(ctx, protocol.CatalogEntry{URL: srv.URL, SHA256: hash, SizeBytes: max}, dir)
			if err == nil {
				t.Fatal("unsafe download accepted")
			}
			files, _ := os.ReadDir(dir)
			if len(files) != 0 {
				t.Fatal("failed artifact retained")
			}
		})
	}
}
