package security

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"remoteops/pkg/protocol"
)

func DecodeJSON(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, protocol.MaxBodyBytes)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return errors.New("exactly one JSON object is required")
	}
	return nil
}
func SignedJSON(ctx context.Context, client *http.Client, method, target, secret, direction string, in, out any) error {
	var body []byte
	var err error
	if in != nil {
		body, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err = SignRequest(req, body, secret, direction); err != nil {
		return err
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxBodyBytes+1))
	if err != nil {
		return err
	}
	if len(data) > protocol.MaxBodyBytes {
		return errors.New("response too large")
	}
	if err = VerifyResponse(resp, data, secret, direction, req.Header.Get(HeaderNonce)); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e protocol.ErrorEnvelope
		if json.Unmarshal(data, &e) == nil && e.Error.Code != "" {
			return e.Error
		}
		return errors.New("signed endpoint returned an error")
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
func WriteSignedJSON(w http.ResponseWriter, r *http.Request, status int, v any, secret, direction string) {
	body, err := json.Marshal(v)
	if err != nil {
		status = 500
		body = []byte(`{"error":{"code":"internal_error","message":"JSON encoding failed"}}`)
	}
	SignResponse(w.Header(), status, body, secret, direction, r.Header.Get(HeaderNonce))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
