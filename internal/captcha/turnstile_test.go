package captcha

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fake(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.PostForm.Get("secret") != "s3cret" {
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-secret"]}`))
			return
		}
		if r.PostForm.Get("remoteip") != "203.0.113.7" {
			t.Errorf("remoteip = %q", r.PostForm.Get("remoteip"))
		}
		switch r.PostForm.Get("response") {
		case "good":
			_, _ = w.Write([]byte(`{"success":true,"hostname":"WWW.Acme.test"}`))
		case "boom":
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTurnstile(t *testing.T) {
	srv := fake(t)
	ctx := context.Background()
	v := NewTurnstile(srv.URL)

	res, err := v.Verify(ctx, "s3cret", " good ", "203.0.113.7")
	if err != nil || res.Hostname != "www.acme.test" {
		t.Fatalf("good token: %+v %v", res, err)
	}
	if _, err := v.Verify(ctx, "s3cret", "", "203.0.113.7"); !errors.Is(err, ErrMissing) {
		t.Fatalf("empty token: %v", err)
	}
	if _, err := v.Verify(ctx, "s3cret", "used-or-fake", "203.0.113.7"); !errors.Is(err, ErrRejected) {
		t.Fatalf("bad token: %v", err)
	}
	if _, err := v.Verify(ctx, "s3cret", string(make([]byte, 3000)), "203.0.113.7"); !errors.Is(err, ErrRejected) {
		t.Fatalf("oversized token: %v", err)
	}
	if _, err := v.Verify(ctx, "s3cret", "boom", "203.0.113.7"); err == nil || errors.Is(err, ErrRejected) {
		t.Fatalf("upstream failure must be an error, not a rejection: %v", err)
	}
	if _, err := v.Verify(ctx, "wrong", "good", "203.0.113.7"); err == nil || errors.Is(err, ErrRejected) {
		t.Fatalf("bad secret must be a server error: %v", err)
	}
}
