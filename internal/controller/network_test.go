package controller

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestNetworkStatusIsAuthenticatedAndBounded(t *testing.T) {
	s := newTestServer(t, ":memory:")
	r := httptest.NewRequest("GET", "/api/v1/network-status", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("network status exposed without authentication")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	status := s.networkStatus(ctx)
	if status.State != "unavailable" || status.Error == "" {
		t.Fatal("local daemon failure was not represented")
	}
}
