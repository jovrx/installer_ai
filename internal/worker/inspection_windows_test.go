//go:build windows

package worker

import (
	"context"
	"remoteops/pkg/protocol"
	"testing"
	"time"
)

func TestWindowsInspectionUsesUnprivilegedInventory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := (NativeExecutor{}).Execute(ctx, protocol.TaskSpec{Type: protocol.InspectSystem}, protocol.CatalogEntry{}, func(string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Info == nil || result.Info.Hostname == "" || result.Info.OS == "" || result.Info.CPU == "" || result.Info.MemoryBytes == 0 || len(result.Info.Disks) == 0 {
		t.Fatal("hardware inventory incomplete")
	}
	if len(result.Info.Software) != 0 {
		t.Fatal("empty software scope collected packages")
	}
}
