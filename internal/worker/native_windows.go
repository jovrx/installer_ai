//go:build windows

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/sys/windows/registry"
	"os"
	"path/filepath"
	"remoteops/internal/worker/installers"
	"remoteops/internal/worker/privilege"
	"remoteops/pkg/protocol"
	"strings"
	"time"
)

func inspect(ctx context.Context) (*protocol.SystemInfo, error) {
	// The script is compiled in, accepts no remote values, and reads machine inventory only.
	const script = `[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new();$ErrorActionPreference='Stop';$os=Get-CimInstance Win32_OperatingSystem;$cpu=Get-CimInstance Win32_Processor|Select-Object -First 1;$mem=Get-CimInstance Win32_ComputerSystem;@{os=$os.Caption+' '+$os.Version;hostname=[System.Environment]::MachineName;cpu=$cpu.Name;memory_bytes=[uint64]$mem.TotalPhysicalMemory;disks=@(Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=3'|ForEach-Object {@{name=$_.DeviceID;total_bytes=[uint64]$_.Size;free_bytes=[uint64]$_.FreeSpace}});gpus=@(Get-CimInstance Win32_VideoController|ForEach-Object {@{name=$_.Name;driver=$_.DriverVersion}});software=@{}}|ConvertTo-Json -Depth 5 -Compress`
	exe, err := installers.SystemExecutable("powershell.exe")
	if err != nil {
		return nil, err
	}
	var output strings.Builder
	_, err = installers.Run(ctx, exe, []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script}, os.TempDir(), func(stream, s string) {
		if stream == "stdout" {
			output.WriteString(s)
		}
	})
	if err != nil {
		return nil, errors.New("Windows system inspection failed")
	}
	var info protocol.SystemInfo
	if err = json.Unmarshal([]byte(output.String()), &info); err != nil {
		return nil, errors.New("invalid system inventory result")
	}
	info.Elevated = privilege.Elevated()
	return &info, nil
}
func wingetPath() (string, error) {
	if privilege.Elevated() {
		// Never elevate a user-writable execution alias. Resolve the signed App
		// Installer package below the protected Program Files WindowsApps tree.
		exe, err := installers.SystemExecutable("powershell.exe")
		if err != nil {
			return "", err
		}
		var output strings.Builder
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		const script = `[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new();Get-AppxPackage -Name Microsoft.DesktopAppInstaller|Select-Object -First 1 -ExpandProperty InstallLocation`
		_, err = installers.Run(ctx, exe, []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script}, os.TempDir(), func(stream, s string) {
			if stream == "stdout" {
				output.WriteString(s)
			}
		})
		if err != nil {
			return "", errors.New("protected App Installer package unavailable")
		}
		base := filepath.Clean(strings.TrimSpace(output.String()))
		trusted := filepath.Join(os.Getenv("ProgramFiles"), "WindowsApps") + string(filepath.Separator)
		if !filepath.IsAbs(base) || !strings.HasPrefix(strings.ToLower(base), strings.ToLower(trusted)) || !strings.HasPrefix(filepath.Base(base), "Microsoft.DesktopAppInstaller_") {
			return "", errors.New("untrusted elevated winget location")
		}
		path := filepath.Join(base, "winget.exe")
		if _, err = os.Stat(path); err != nil {
			return "", err
		}
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(home, "AppData", "Local", "Microsoft", "WindowsApps", "winget.exe")
	if _, err = os.Stat(p); err != nil {
		return "", errors.New("winget is unavailable; install App Installer locally")
	}
	return p, nil
}
func installedVersion(ctx context.Context, e protocol.CatalogEntry) (string, error) {
	if e.ID == "git" {
		p := filepath.Join(os.Getenv("ProgramFiles"), "Git", "cmd", "git.exe")
		if !filepath.IsAbs(p) {
			return "", errors.New("invalid Program Files location")
		}
		var output strings.Builder
		_, err := installers.Run(ctx, p, []string{"--version"}, os.TempDir(), func(stream, s string) {
			if stream == "stdout" {
				output.WriteString(s)
			}
		})
		if err != nil {
			return "", err
		}
		v := strings.TrimSpace(strings.TrimPrefix(output.String(), "git version "))
		v = strings.Split(v, ".windows.")[0]
		return v, nil
	}
	// MSI/winget maintained verification reads DisplayName/DisplayVersion, never a catalog command.
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, view := range []uint32{registry.WOW64_64KEY, registry.WOW64_32KEY} {
			k, err := registry.OpenKey(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`, registry.READ|view)
			if err != nil {
				continue
			}
			names, _ := k.ReadSubKeyNames(-1)
			for _, name := range names {
				child, err := registry.OpenKey(k, name, registry.READ|view)
				if err != nil {
					continue
				}
				display, _, _ := child.GetStringValue("DisplayName")
				version, _, _ := child.GetStringValue("DisplayVersion")
				child.Close()
				if display == e.Name && version != "" {
					k.Close()
					return version, nil
				}
			}
			k.Close()
		}
	}
	return "", errors.New("approved package has no matching installed version record")
}
