package installers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"remoteops/pkg/protocol"
	"slices"
	"strings"
	"time"
)

type Downloader struct {
	Client    *http.Client
	FreeSpace func(string) (uint64, error)
	Progress  func(stage string, completed, total int64)
}

func ValidateArtifact(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	sum, err := hex.DecodeString(expected)
	if err != nil || len(sum) != 32 || !slices.Equal(sum, h.Sum(nil)) {
		return errors.New("artifact SHA-256 mismatch")
	}
	return nil
}
func (d Downloader) Download(ctx context.Context, e protocol.CatalogEntry, dir string) (path string, err error) {
	origin, err := url.Parse(e.URL)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil {
		return "", errors.New("trusted HTTPS artifact URL required")
	}
	hosts := append([]string{strings.ToLower(origin.Hostname())}, e.AllowedHosts...)
	allowed := func(u *url.URL) bool {
		return u.Scheme == "https" && u.User == nil && slices.Contains(hosts, strings.ToLower(u.Hostname()))
	}
	max := e.SizeBytes
	if max <= 0 {
		max = 512 << 20
	}
	if max > 4<<30 {
		return "", errors.New("artifact too large")
	}
	space := d.FreeSpace
	if space == nil {
		space = FreeSpace
	}
	free, err := space(dir)
	if err != nil {
		return "", err
	}
	if free < uint64(max)+(64<<20) {
		return "", errors.New("insufficient disk space")
	}
	client := http.Client{Timeout: 15 * time.Minute}
	if d.Client != nil {
		client = *d.Client
	}
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowed(r.URL) {
			return errors.New("untrusted artifact redirect")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", e.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("artifact download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("artifact HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > max {
		return "", errors.New("artifact size limit exceeded")
	}
	pattern := "artifact-*.exe"
	if e.Installer == "msi" {
		pattern = "artifact-*.msi"
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path = f.Name()
	artifactPath := path
	defer func() {
		f.Close()
		if err != nil {
			os.Remove(artifactPath)
		}
	}()
	if err = f.Chmod(0600); err != nil {
		return "", err
	}
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	reader := &downloadProgressReader{reader: io.LimitReader(resp.Body, max+1), total: total, emit: d.Progress}
	if d.Progress != nil {
		d.Progress("downloading", 0, total)
	}
	n, err := io.Copy(f, reader)
	if d.Progress != nil {
		d.Progress("downloading", n, total)
	}
	if err != nil {
		return "", err
	}
	if n > max {
		return "", errors.New("artifact size limit exceeded")
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if d.Progress != nil {
		d.Progress("verifying_artifact", 0, 0)
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = ValidateArtifact(path, e.SHA256); err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	return path, nil
}

type downloadProgressReader struct {
	reader           io.Reader
	total, completed int64
	last             time.Time
	emit             func(string, int64, int64)
}

func (r *downloadProgressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.completed += int64(n)
	if n > 0 && r.emit != nil && (r.last.IsZero() || time.Since(r.last) >= 100*time.Millisecond) {
		r.emit("downloading", r.completed, r.total)
		r.last = time.Now()
	}
	return n, err
}
