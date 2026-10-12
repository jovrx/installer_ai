package worker

import (
	"remoteops/pkg/protocol"
	"strings"
	"testing"
)

func TestMaintainedInstallerMappings(t *testing.T) {
	for _, entry := range []protocol.CatalogEntry{{ID: "git", Installer: "exe", Version: "2.50.0"}, {ID: "tool", Installer: "msi", Version: "1"}, {ID: "git", Installer: "winget", WingetID: "Git.Git", Source: "winget", Version: "2.50.0"}} {
		name, args, err := installerCommand(entry, "C:/controlled/artifact")
		if err != nil {
			t.Fatal(err)
		}
		if name == "" || len(args) == 0 {
			t.Fatal("missing maintained command")
		}
		if entry.Installer == "winget" && !strings.Contains(strings.Join(args, " "), "--version 2.50.0") {
			t.Fatal("winget version unpinned")
		}
	}
	if _, _, err := installerCommand(protocol.CatalogEntry{ID: "unknown", Installer: "exe"}, "payload"); err == nil {
		t.Fatal("unmaintained EXE accepted")
	}
	if _, _, err := installerCommand(protocol.CatalogEntry{Installer: "winget", WingetID: "--override", Version: "1", Source: "winget"}, ""); err == nil {
		t.Fatal("catalog argument injection accepted")
	}
}
