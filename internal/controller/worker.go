package controller

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"remoteops/internal/security"
	"remoteops/pkg/protocol"
	"strconv"
	"tailscale.com/client/local"
	"time"
)

// LocalPeerResolver obtains identity from the local Tailscale daemon, never JSON fields.
type LocalPeerResolver struct{ Client *local.Client }

func (p LocalPeerResolver) Resolve(ctx context.Context, r *http.Request) (Peer, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !tailnetIP(host) {
		return Peer{}, fmt.Errorf("request is not from an observed tailnet address")
	}
	c := p.Client
	if c == nil {
		c = &local.Client{}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	who, err := c.WhoIs(ctx, r.RemoteAddr)
	if err != nil {
		return Peer{}, err
	}
	if who.Node == nil || who.Node.ID == 0 {
		return Peer{}, fmt.Errorf("missing peer node identity")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return Peer{}, err
	}
	found := false
	for _, a := range who.Node.Addresses {
		if a.Addr() == ip {
			found = true
		}
	}
	if !found {
		return Peer{}, fmt.Errorf("observed peer address does not match node identity")
	}
	return Peer{Address: host, NodeID: strconv.FormatInt(int64(who.Node.ID), 10)}, nil
}
func (s *Server) WorkerHandler() http.Handler { return http.HandlerFunc(s.worker) }
func (s *Server) worker(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 404, "not_found", "Worker endpoint not found")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, protocol.MaxBodyBytes))
	r.Body.Close()
	if err != nil {
		fail(w, 400, "invalid_body", "Worker body too large")
		return
	}
	// Strict decode is used here because verification requires the untouched bytes.
	var in protocol.RegisterRequest
	var hb protocol.HeartbeatRequest
	register := r.URL.Path == "/api/v1/worker/register"
	if !register && r.URL.Path != "/api/v1/worker/heartbeat" && r.URL.Path != "/api/v1/worker/close" {
		fail(w, 404, "not_found", "Worker endpoint not found")
		return
	}
	if register {
		err = strict(body, &in)
	} else {
		err = strict(body, &hb)
	}
	if err != nil {
		fail(w, 400, "invalid_request", "Invalid Worker JSON")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		fail(w, 503, "unavailable", "Controller stopped")
		return
	}
	id := hb.SessionID
	if register {
		id = in.SessionID
	}
	var rec sessionRecord
	if err = s.get("sessions", id, &rec); err != nil {
		fail(w, 401, "unauthorized", "Invalid session")
		return
	}
	secret := rec.Token
	if register {
		secret = in.PairingToken
		if len(secret) < 48 || !equalToken(secret, rec.PairingDigest) {
			fail(w, 401, "unauthorized", "Invalid pairing token")
			return
		}
	}
	signedFail := func(status int, code, msg string) {
		security.WriteSignedJSON(w, r, status, protocol.ErrorEnvelope{Error: protocol.APIError{Code: code, Message: msg}}, secret, "worker-to-controller")
	}
	key := id
	if register {
		key = "pair:" + id
	}
	v := s.verifiers[key]
	if v == nil {
		v = security.NewVerifier(secret, "worker-to-controller")
		s.verifiers[key] = v
	}
	if err = v.Verify(r, body); err != nil {
		signedFail(401, "invalid_signature", "Invalid Worker signature or replay")
		return
	}
	// Persist nonce consumption across process restarts for the full freshness window.
	stamp, _ := strconv.ParseInt(r.Header.Get(security.HeaderTime), 10, 64)
	used, err := s.db.Exec("INSERT OR IGNORE INTO request_nonces(id,expires_at) VALUES(?,?)", security.TokenDigest(key+":"+r.Header.Get(security.HeaderNonce)), stamp+61)
	if err != nil {
		signedFail(500, "internal_error", "Nonce persistence failed")
		return
	}
	count, err := used.RowsAffected()
	if err != nil || count != 1 {
		signedFail(401, "invalid_signature", "Replayed Worker request")
		return
	}
	if register {
		if rec.Session.State != protocol.PendingPair {
			signedFail(409, "pairing_consumed", "Pairing is no longer available")
			return
		}
		if !s.opts.Now().Before(rec.Session.PairingExpiresAt) {
			rec.Session.State = protocol.Expired
			_ = s.put("sessions", id, rec)
			signedFail(409, "pairing_expired", "Pairing expired")
			return
		}
		if in.ProtocolVersion != protocol.Version || in.Port != protocol.WorkerPort || in.Hostname == "" || len(in.Hostname) > 255 || len(in.OS) > 128 || len(in.ClientVersion) > 128 {
			signedFail(400, "invalid_registration", "Protocol, fixed port and hostname required")
			return
		}
	} else {
		if _, err = s.active(s.db, id, hb.DeviceID); err != nil {
			signedFail(409, "session_inactive", "Session authority revoked or expired")
			return
		}
	}
	// LocalAPI identity resolution can block; management revocation must proceed.
	s.mu.Unlock()
	peer, err := s.opts.PeerResolver.Resolve(r.Context(), r)
	s.mu.Lock()
	if s.closed {
		signedFail(503, "unavailable", "Controller stopped")
		return
	}
	if err != nil || !tailnetIP(peer.Address) || peer.NodeID == "" {
		signedFail(403, "invalid_peer", "Cannot validate observed tailnet peer")
		return
	}
	if s.get("sessions", id, &rec) != nil {
		signedFail(401, "unauthorized", "Invalid session")
		return
	}
	if register {
		if rec.Session.State != protocol.PendingPair || !s.opts.Now().Before(rec.Session.PairingExpiresAt) {
			signedFail(409, "pairing_inactive", "Pairing consumed, revoked, or expired")
			return
		}
	} else if _, err = s.active(s.db, id, hb.DeviceID); err != nil {
		signedFail(409, "session_inactive", "Session authority revoked or expired")
		return
	}
	if register {
		tx, err := s.db.Begin()
		if err != nil {
			signedFail(500, "internal_error", "Database failure")
			return
		}
		defer tx.Rollback()
		var current sessionRecord
		if get(tx, "sessions", id, &current) != nil || current.Session.State != protocol.PendingPair || !equalToken(in.PairingToken, current.PairingDigest) {
			signedFail(409, "pairing_consumed", "Pairing consumed")
			return
		}
		now := s.opts.Now().UTC()
		rec.Token = security.NewID("session_token")
		rec.Session.DeviceID = security.NewID("device")
		rec.Session.NodeID = peer.NodeID
		rec.Session.State = protocol.Online
		rec.Session.LastHeartbeat = now
		if rec.AuthorizationSeconds < 60 || rec.AuthorizationSeconds > 86400 {
			signedFail(409, "pairing_incompatible", "Create a new pairing with explicit authorization duration")
			return
		}
		rec.Session.ExpiresAt = now.Add(time.Duration(rec.AuthorizationSeconds) * time.Second)
		d := protocol.Device{ID: rec.Session.DeviceID, SessionID: id, Hostname: in.Hostname, OS: in.OS, ClientVersion: in.ClientVersion, Address: peer.Address, Online: true, ConnectedAt: now, LastHeartbeat: now}
		if err = put(tx, "sessions", id, rec); err == nil {
			err = put(tx, "devices", d.ID, d)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			signedFail(500, "internal_error", "Registration commit failed")
			return
		}
		s.auditAs("worker", "worker_registered", id, d.ID, "", "success")
		security.WriteSignedJSON(w, r, 200, protocol.RegisterResponse{Session: rec.Session, Device: d, SessionToken: rec.Token, Catalog: rec.Catalog}, secret, "worker-to-controller")
		return
	}
	var d protocol.Device
	if s.get("devices", hb.DeviceID, &d) != nil || peer.NodeID != rec.Session.NodeID || peer.Address != d.Address {
		signedFail(403, "invalid_peer", "Peer does not match bound device")
		return
	}
	if r.URL.Path == "/api/v1/worker/close" {
		if err = s.revokeLocked(&rec, protocol.Closed, "worker"); err != nil {
			signedFail(500, "internal_error", "Revocation failed")
			return
		}

		security.WriteSignedJSON(w, r, 200, rec.Session, secret, "worker-to-controller")
		return
	}
	now := s.opts.Now().UTC()
	rec.Session.LastHeartbeat = now
	d.LastHeartbeat = now
	d.Online = true
	tx, err := s.db.Begin()
	if err == nil {
		defer tx.Rollback()
		err = put(tx, "sessions", id, rec)
		if err == nil {
			err = put(tx, "devices", d.ID, d)
		}
		if err == nil {
			err = tx.Commit()
		}
	}
	if err != nil {
		signedFail(500, "internal_error", "Heartbeat persistence failed")
		return
	}
	security.WriteSignedJSON(w, r, 200, rec.Session, secret, "worker-to-controller")
}
