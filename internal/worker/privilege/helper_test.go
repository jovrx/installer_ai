package privilege

import (
	"remoteops/pkg/protocol"
	"testing"
)

func TestHelperRejectsUnboundRequest(t *testing.T) {
	r := Request{Spec: protocol.TaskSpec{SessionID: "s", DeviceID: "d", Type: protocol.InstallPackage, Package: &protocol.PackageRef{ID: "git", Version: "1"}, RequiresAdmin: true, TimeoutSeconds: 30}, Entry: protocol.CatalogEntry{ID: "git", Version: "1", Installer: "winget", WingetID: "Git.Git", Source: "winget", RequiresAdmin: true}}
	r.Digest = protocol.TaskDigest(r.Spec)
	if err := ValidateRequest(r); err != nil {
		t.Fatal(err)
	}
	r.Digest = "wrong"
	if ValidateRequest(r) == nil {
		t.Fatal("unbound elevation accepted")
	}
	r.Digest = protocol.TaskDigest(r.Spec)
	r.Entry.Version = "2"
	if ValidateRequest(r) == nil {
		t.Fatal("changed catalog entry accepted")
	}
}
