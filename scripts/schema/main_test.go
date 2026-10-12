package main

import "testing"

func TestContractCoversApprovalAndWorkerAuthentication(t *testing.T) {
	d := document()
	paths := d["paths"].(map[string]any)
	op := paths["/api/v1/approvals/{id}/approve"].(map[string]any)["post"].(map[string]any)
	auth := op["security"].([]any)
	if len(auth) != 1 {
		t.Fatal("approval permits non-operator authentication")
	}
	if _, ok := auth[0].(map[string]any)["browserSession"]; !ok {
		t.Fatal("approval is not restricted to browser")
	}
	pairing := paths["/api/v1/sessions/{id}/pairing"].(map[string]any)["post"].(map[string]any)
	pairingAuth := pairing["security"].([]any)
	if len(pairingAuth) != 1 || pairingAuth[0].(map[string]any)["browserSession"] == nil {
		t.Fatal("pairing regeneration is not restricted to browser")
	}
	worker := paths["/v1/tasks"].(map[string]any)["post"].(map[string]any)
	if worker["requestBody"] == nil || worker["security"] == nil {
		t.Fatal("Worker execution contract missing")
	}
	schemas := d["components"].(map[string]any)["schemas"].(map[string]any)
	if _, ok := schemas[""]; ok {
		t.Fatal("anonymous structs produced invalid empty component name")
	}
	spec := schemas["TaskSpec"].(map[string]any)
	fields := spec["properties"].(map[string]any)
	for _, name := range []string{"session_id", "device_id", "package", "timeout_seconds", "requires_admin"} {
		if fields[name] == nil {
			t.Fatalf("task field absent: %s", name)
		}
	}
	if spec["additionalProperties"] != false {
		t.Fatal("arbitrary task fields allowed")
	}
}
