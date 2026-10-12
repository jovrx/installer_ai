package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"remoteops/internal/worker/events"
	"remoteops/internal/worker/installers"
	"remoteops/internal/worker/privilege"
	"remoteops/pkg/protocol"
	"runtime"
	"strings"
)

type NativeExecutor struct{ Catalog []protocol.CatalogEntry }

func (native NativeExecutor) Execute(ctx context.Context, sp protocol.TaskSpec, e protocol.CatalogEntry, emit func(string, string)) (protocol.TaskResult, error) {
	if runtime.GOOS != "windows" {
		return protocol.TaskResult{}, protocol.APIError{Code: "unsupported_platform", Message: "native execution requires Windows"}
	}
	if sp.Type == protocol.InspectSystem {
		if sp.RequiresAdmin || sp.Package != nil {
			return protocol.TaskResult{}, errors.New("inspection cannot install or elevate")
		}
		events.Emit(emit, events.Progress{Stage: "inspecting"})
		info, err := inspect(ctx)
		if err == nil {
			for _, entry := range native.Catalog {
				if version, e := installedVersion(ctx, entry); e == nil {
					info.Software[entry.ID] = version
				}
			}
		}
		return protocol.TaskResult{Info: info, Verified: err == nil}, err
	}
	if sp.Type != protocol.InstallPackage {
		return protocol.TaskResult{}, errors.New("unsupported task")
	}
	if sp.Package == nil || sp.Package.ID != e.ID || sp.Package.Version != e.Version || sp.RequiresAdmin != e.RequiresAdmin {
		return protocol.TaskResult{}, errors.New("installer does not match approved task")
	}
	if err := protocol.ValidateCatalog([]protocol.CatalogEntry{e}); err != nil {
		return protocol.TaskResult{}, err
	}
	dir, err := os.MkdirTemp("", "remoteops-install-")
	if err != nil {
		return protocol.TaskResult{}, err
	}
	defer os.RemoveAll(dir)
	if err = privilege.SecureDirectory(dir); err != nil {
		return protocol.TaskResult{}, err
	}
	artifact := ""
	if e.Installer != "winget" {
		emit("stdout", "Downloading pinned artifact and checking SHA-256")
		artifact, err = (installers.Downloader{Progress: func(stage string, completed, total int64) {
			events.Emit(emit, events.Progress{Stage: stage, CompletedBytes: completed, TotalBytes: total})
		}}).Download(ctx, e, dir)
		if err != nil {
			return protocol.TaskResult{}, err
		}
	}
	req := privilege.Request{Spec: sp, Entry: e, Artifact: artifact, Digest: protocol.TaskDigest(sp)}
	if sp.RequiresAdmin && !privilege.Elevated() {
		events.Emit(emit, events.Progress{Stage: "awaiting_uac"})
		return privilege.Elevate(ctx, req, emit)
	}
	return installPrepared(ctx, req, emit)
}
func installerCommand(e protocol.CatalogEntry, artifact string) (string, []string, error) {
	switch e.Installer {
	case "msi":
		return "msiexec.exe", []string{"/i", artifact, "/qn", "/norestart", "REBOOT=ReallySuppress"}, nil
	case "exe":
		if e.ID != "git" {
			return "", nil, errors.New("unmaintained EXE installer")
		}
		return artifact, []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/NOCANCEL", "/SP-"}, nil
	case "winget":
		if e.Source != "winget" || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`).MatchString(e.WingetID) || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,63}$`).MatchString(e.Version) || strings.EqualFold(e.Version, "latest") {
			return "", nil, errors.New("winget requires pinned trusted catalog entry")
		}
		return "winget.exe", []string{"install", "--id", e.WingetID, "--exact", "--version", e.Version, "--source", "winget", "--silent", "--disable-interactivity", "--accept-package-agreements", "--accept-source-agreements"}, nil
	}
	return "", nil, errors.New("unsupported installer template")
}
func installPrepared(ctx context.Context, r privilege.Request, emit func(string, string)) (protocol.TaskResult, error) {
	if r.Entry.Installer != "winget" {
		events.Emit(emit, events.Progress{Stage: "verifying_artifact"})
		if err := installers.ValidateArtifact(r.Artifact, r.Entry.SHA256); err != nil {
			return protocol.TaskResult{}, err
		}
	}
	exe, args, err := installerCommand(r.Entry, r.Artifact)
	if err != nil {
		return protocol.TaskResult{}, err
	}
	dir := filepath.Dir(r.Artifact)
	if r.Entry.Installer == "msi" {
		exe, err = installers.SystemExecutable(exe)
	}
	if r.Entry.Installer == "winget" {
		exe, err = wingetPath()
		dir = os.TempDir()
	}
	if err != nil {
		return protocol.TaskResult{}, err
	}
	events.Emit(emit, events.Progress{Stage: "installing"})
	emit("stdout", "Executing maintained "+r.Entry.Installer+" template for "+r.Entry.Name+" "+r.Entry.Version)
	code, err := installers.Run(ctx, exe, args, dir, emit)
	result := protocol.TaskResult{ExitCode: &code, RebootRequired: code == 3010}
	if err != nil {
		return result, err
	}
	events.Emit(emit, events.Progress{Stage: "verifying_version"})
	emit("stdout", "Checking installed version independently")
	version, err := installedVersion(ctx, r.Entry)
	result.Version = version
	result.Verified = err == nil && version == r.Entry.Version
	if !result.Verified {
		if err == nil {
			err = fmt.Errorf("installed version %q does not match approved %q", version, r.Entry.Version)
		}
		return result, protocol.APIError{Code: "verification_failed", Message: err.Error()}
	}
	return result, nil
}
func RunElevatedHelper(ctx context.Context, path string) error {
	return privilege.RunHelper(ctx, path, installPrepared)
}
