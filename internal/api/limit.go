package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/onatm/boxface/internal/auth"
)

var (
	errRateLimited = errors.New("too many failed logins from this address; try again later")
	errNoIdentity  = fmt.Errorf("%w: a login code is required; this connection carries no Tailscale login", auth.ErrUnauthorized)
)

type tailscaleKey struct{}

func ViaTailscale(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, tailscaleKey{}, true)
}

func requestIdentity(r *http.Request) (provider, subject string, ok bool) {
	if via, _ := r.Context().Value(tailscaleKey{}).(bool); !via {
		return "", "", false
	}
	login := r.Header.Get("Tailscale-User-Login")
	return auth.ProviderTailscale, login, login != ""
}

func clientAddr(r *http.Request) string {
	if via, _ := r.Context().Value(tailscaleKey{}).(bool); via {
		if forwarded, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ","); strings.TrimSpace(forwarded) != "" {
			return strings.TrimSpace(forwarded)
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

type window struct {
	start    time.Time
	failures int
}

type loginLimiter struct {
	mu      sync.Mutex
	max     int
	period  time.Duration
	now     func() time.Time
	windows map[string]*window
}

func newLoginLimiter(max int, period time.Duration) *loginLimiter {
	return &loginLimiter{max: max, period: period, now: time.Now, windows: map[string]*window{}}
}

func (l *loginLimiter) allow(addr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.windows[addr]
	return w == nil || l.now().Sub(w.start) >= l.period || w.failures < l.max
}

func (l *loginLimiter) fail(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.windows) > 10000 {
		for a, w := range l.windows {
			if now.Sub(w.start) >= l.period {
				delete(l.windows, a)
			}
		}
	}
	w := l.windows[addr]
	if w == nil || now.Sub(w.start) >= l.period {
		l.windows[addr] = &window{start: now, failures: 1}
		return
	}
	w.failures++
}
