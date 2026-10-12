package protocol

import "testing"

func TestScopeAndDigest(t *testing.T) {
	spec := TaskSpec{SessionID: "s", DeviceID: "d", Type: InstallPackage, Package: &PackageRef{ID: "git", Version: "2.51.0"}, TimeoutSeconds: 60, RequiresAdmin: true}
	scope := Scope{AllowedTasks: []TaskType{InstallPackage}, AllowedPackages: []string{"git"}, AllowAdmin: false}
	catalog := []CatalogEntry{{ID: "git", Version: "2.51.0", Installer: "exe", URL: "https://example.org/git.exe", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RequiresAdmin: true}}
	if err := ValidateSpec(spec, scope, catalog); err == nil {
		t.Fatal("admin scope ignored")
	}
	scope.AllowAdmin = true
	if err := ValidateSpec(spec, scope, catalog); err != nil {
		t.Fatal(err)
	}
	first := TaskDigest(spec)
	spec.Package.Version = "other"
	if first == TaskDigest(spec) {
		t.Fatal("approval digest did not bind version")
	}
	if err := ValidateSpec(spec, scope, catalog); err == nil {
		t.Fatal("unapproved version accepted")
	}
}

func TestInstallRequiresExplicitPinnedVersion(t *testing.T) {
	spec := TaskSpec{SessionID: "s", DeviceID: "d", Type: InstallPackage, Package: &PackageRef{ID: "git"}, TimeoutSeconds: 60}
	scope := Scope{AllowedTasks: []TaskType{InstallPackage}, AllowedPackages: []string{"git"}}
	catalog := []CatalogEntry{{ID: "git", Version: "2.51.0", Installer: "exe"}}
	if err := ValidateSpec(spec, scope, catalog); err == nil {
		t.Fatal("missing pinned version accepted")
	}
}
