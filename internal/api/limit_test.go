package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginLimiterBlocksUntilWindowEnds(t *testing.T) {
	now := time.Now()
	l := newLoginLimiter(2, time.Minute)
	l.now = func() time.Time { return now }
	l.fail("a")
	l.fail("a")
	if l.allow("a") || !l.allow("b") {
		t.Fatalf("after two failures: a %v, b %v", l.allow("a"), l.allow("b"))
	}
	now = now.Add(time.Minute)
	if !l.allow("a") {
		t.Fatal("still blocked after the window")
	}
	l.fail("a")
	if !l.allow("a") {
		t.Fatal("one failure in a new window blocked")
	}
}

func TestClientAddrTrustsForwardedOnlyViaTailscale(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/login", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "100.64.0.7, 10.0.0.1")
	if got := clientAddr(r); got != "127.0.0.1" {
		t.Fatalf("plain listener = %q", got)
	}
	r = r.WithContext(ViaTailscale(context.Background(), nil))
	if got := clientAddr(r); got != "100.64.0.7" {
		t.Fatalf("tailscale listener = %q", got)
	}
}
