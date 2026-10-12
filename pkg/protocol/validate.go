package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

func Digest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func TaskDigest(s TaskSpec) string { return Digest(s) }
func ValidateCatalog(entries []CatalogEntry) error {
	seen := map[string]bool{}
	for _, e := range entries {
		if e.ID == "" || e.Version == "" || e.Version == "latest" || seen[e.ID] {
			return errors.New("catalog requires unique IDs and pinned versions")
		}
		seen[e.ID] = true
		if e.Installer == "winget" {
			if e.WingetID == "" || e.Source != "winget" {
				return errors.New("winget requires a fixed ID and trusted source")
			}
			continue
		}
		if e.Installer != "msi" && !(e.Installer == "exe" && e.ID == "git") {
			return errors.New("unsupported installer template")
		}
		u, err := url.Parse(e.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return errors.New("artifact requires an HTTPS URL")
		}
		hash, err := hex.DecodeString(e.SHA256)
		if err != nil || len(hash) != 32 {
			return errors.New("artifact requires SHA-256")
		}
		if e.SizeBytes < 0 || e.SizeBytes > 4<<30 {
			return errors.New("invalid artifact size limit")
		}
	}
	return nil
}
func ValidateScope(s Scope, catalog []CatalogEntry) error {
	if len(s.AllowedTasks) == 0 {
		return errors.New("empty authorization scope")
	}
	for _, v := range s.AllowedTasks {
		if v != InspectSystem && v != InstallPackage {
			return errors.New("unknown task type")
		}
	}
	for _, id := range s.AllowedPackages {
		if _, ok := FindPackage(catalog, id, ""); !ok {
			return fmt.Errorf("unknown package %s", id)
		}
	}
	return nil
}
func FindPackage(c []CatalogEntry, id, version string) (CatalogEntry, bool) {
	for _, e := range c {
		if e.ID == id && (version == "" || e.Version == version) {
			return e, true
		}
	}
	return CatalogEntry{}, false
}
func ValidateSpec(s TaskSpec, scope Scope, catalog []CatalogEntry) error {
	if s.SessionID == "" || s.DeviceID == "" {
		return errors.New("session and device are required")
	}
	if s.TimeoutSeconds < 1 || s.TimeoutSeconds > 7200 {
		return errors.New("timeout must be between 1 and 7200 seconds")
	}
	if !slices.Contains(scope.AllowedTasks, s.Type) {
		return errors.New("task type outside authorized scope")
	}
	if s.Type == InspectSystem {
		if s.Package != nil || s.RequiresAdmin {
			return errors.New("inspection cannot install or elevate")
		}
		return nil
	}
	if s.Type != InstallPackage || s.Package == nil {
		return errors.New("unsupported task")
	}
	if s.Package.Version == "" || s.Package.Version == "latest" {
		return errors.New("installation requires an explicit pinned version")
	}
	if !slices.Contains(scope.AllowedPackages, s.Package.ID) {
		return errors.New("software outside authorized scope")
	}
	e, ok := FindPackage(catalog, s.Package.ID, s.Package.Version)
	if !ok {
		return errors.New("package version absent from catalog")
	}
	if s.RequiresAdmin != e.RequiresAdmin {
		return errors.New("admin requirement must match catalog")
	}
	if s.RequiresAdmin && !scope.AllowAdmin {
		return errors.New("administrator action outside authorized scope")
	}
	return nil
}
func ValidateConfig(c Config) error {
	if c.MaxConcurrency < 1 || c.MaxConcurrency > 32 || c.TaskTimeoutSeconds < 1 || c.TaskTimeoutSeconds > 7200 || c.PairingTTLSeconds < 30 || c.PairingTTLSeconds > 3600 || c.SessionTTLSeconds < 60 || c.SessionTTLSeconds > 86400 || c.HeartbeatTTLSeconds < 20 || c.HeartbeatTTLSeconds > 300 || c.LogMaxBytes < 1024 || c.LogMaxBytes > MaxBodyBytes || c.RetentionDays < 1 || c.RetentionDays > 365 {
		return errors.New("configuration outside permitted ranges")
	}
	return nil
}
func ValidIdempotencyKey(s string) bool {
	return len(s) >= 8 && len(s) <= 128 && !strings.ContainsAny(s, "\r\n\x00")
}
