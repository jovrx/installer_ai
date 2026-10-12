package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	HeaderNonce     = "X-RemoteOps-Nonce"
	HeaderTime      = "X-RemoteOps-Time"
	HeaderSignature = "X-RemoteOps-Signature"
)

func NewID(prefix string) string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b)
}
func Digest(v any) string         { b, _ := json.Marshal(v); return bodyDigest(b) }
func TokenDigest(s string) string { return bodyDigest([]byte(s)) }
func bodyDigest(b []byte) string  { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func mac(secret, direction, canonical string) string {
	key := hmac.New(sha256.New, []byte(secret))
	key.Write([]byte("remoteops/v1/" + direction))
	h := hmac.New(sha256.New, key.Sum(nil))
	h.Write([]byte(canonical))
	return hex.EncodeToString(h.Sum(nil))
}
func canonical(req *http.Request, body []byte) string {
	return strings.Join([]string{"v1", req.Method, req.URL.EscapedPath(), req.URL.Query().Encode(), req.Header.Get(HeaderTime), req.Header.Get(HeaderNonce), bodyDigest(body)}, "\n")
}
func SignRequest(req *http.Request, body []byte, secret, direction string) error {
	if secret == "" {
		return errors.New("empty signing secret")
	}
	req.Header.Set(HeaderTime, strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set(HeaderNonce, NewID("nonce"))
	req.Header.Set(HeaderSignature, mac(secret, direction, canonical(req, body)))
	return nil
}

type Verifier struct {
	Secret, Direction string
	mu                sync.Mutex
	used              map[string]int64
	Now               func() time.Time
}

func NewVerifier(secret, direction string) *Verifier {
	return &Verifier{Secret: secret, Direction: direction, used: map[string]int64{}, Now: time.Now}
}
func (v *Verifier) Verify(req *http.Request, body []byte) error {
	ts, err := strconv.ParseInt(req.Header.Get(HeaderTime), 10, 64)
	now := v.Now().Unix()
	if err != nil || ts < now-60 || ts > now+60 {
		return errors.New("expired request timestamp")
	}
	nonce := req.Header.Get(HeaderNonce)
	if len(nonce) < 16 || len(nonce) > 128 {
		return errors.New("invalid nonce")
	}
	expected := mac(v.Secret, v.Direction, canonical(req, body))
	if v.Secret == "" || !hmac.Equal([]byte(expected), []byte(req.Header.Get(HeaderSignature))) {
		return errors.New("invalid request signature")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, t := range v.used {
		if t < now-60 {
			delete(v.used, k)
		}
	}
	if _, ok := v.used[nonce]; ok {
		return errors.New("replayed request")
	}
	if len(v.used) >= 10000 {
		return errors.New("replay cache capacity reached")
	}
	v.used[nonce] = ts
	return nil
}
func responseCanonical(status int, body []byte, direction, nonce, stamp string) string {
	return strings.Join([]string{"v1-response", direction, strconv.Itoa(status), nonce, stamp, bodyDigest(body)}, "\n")
}
func SignResponse(header http.Header, status int, body []byte, secret, direction, nonce string) {
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	header.Set(HeaderTime, stamp)
	header.Set(HeaderNonce, nonce)
	header.Set(HeaderSignature, mac(secret, direction, responseCanonical(status, body, direction, nonce, stamp)))
}
func VerifyResponse(resp *http.Response, body []byte, secret, direction, nonce string) error {
	stamp := resp.Header.Get(HeaderTime)
	ts, err := strconv.ParseInt(stamp, 10, 64)
	now := time.Now().Unix()
	if err != nil || ts < now-60 || ts > now+60 || resp.Header.Get(HeaderNonce) != nonce {
		return errors.New("invalid response freshness")
	}
	expected := mac(secret, direction, responseCanonical(resp.StatusCode, body, direction, nonce, stamp))
	if !hmac.Equal([]byte(expected), []byte(resp.Header.Get(HeaderSignature))) {
		return fmt.Errorf("invalid response identity")
	}
	return nil
}
