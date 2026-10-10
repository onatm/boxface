package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/onatm/boxface/internal/lifecycle"
	"github.com/onatm/boxface/internal/sshkeys"
)

const (
	guestUser   = "dev"
	sshProtocol = "boxface-ssh"
)

var (
	errNotRunning       = errors.New("environment is not running")
	errGuestUnreachable = errors.New("guest SSH is unreachable")
)

type Gateway interface {
	Endpoint(ctx context.Context, environmentID string) (netip.Addr, error)
	HostKey(ctx context.Context, diskID string) (string, error)
}

func dialGuest(ctx context.Context, address string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
}

func (s *Server) liveEnvironment(r *http.Request) (lifecycle.Environment, error) {
	env, err := s.Lifecycle.Environment(r.Context(), owner(r), r.PathValue("id"))
	if err != nil {
		return env, err
	}
	if env.DesiredState == "deleted" || env.ObservedState == "deleted" {
		return env, lifecycle.ErrEnvironmentDeleted
	}
	return env, nil
}

func (s *Server) connection(w http.ResponseWriter, r *http.Request) {
	env, err := s.liveEnvironment(r)
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := s.Gateway.HostKey(r.Context(), env.DiskID)
	if err != nil {
		writeError(w, err)
		return
	}
	keys, err := s.Lifecycle.StartedKeys(r.Context(), owner(r), env.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	fingerprints := []string{}
	for _, k := range keys {
		if _, fp, err := sshkeys.Parse(k); err == nil {
			fingerprints = append(fingerprints, fp)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"environment_id": env.ID, "user": guestUser, "host_key": key, "key_fingerprints": fingerprints})
}

func (s *Server) sshTunnel(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), sshProtocol) || !headerContains(r.Header, "Connection", "upgrade") {
		writeError(w, fmt.Errorf("%w: expected Connection: Upgrade and Upgrade: %s", lifecycle.ErrInvalid, sshProtocol))
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, fmt.Errorf("%w: the SSH tunnel needs HTTP/1.1", lifecycle.ErrInvalid))
		return
	}
	env, err := s.liveEnvironment(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if env.ObservedState != "running" {
		writeError(w, fmt.Errorf("%w: %s is %s", errNotRunning, env.Name, env.ObservedState))
		return
	}
	addr, err := s.Gateway.Endpoint(r.Context(), env.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	guest, err := s.DialGuest(r.Context(), netip.AddrPortFrom(addr, 22).String())
	if err != nil {
		slog.Warn("guest SSH dial failed", "environment", env.ID, "error", err)
		writeError(w, errGuestUnreachable)
		return
	}
	if err := s.Auth.Record(r.Context(), owner(r), "environment.connect", r.Context().Value(tokenKey{}).(string), env.ID, clientAddr(r)); err != nil {
		guest.Close()
		writeError(w, err)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		guest.Close()
		return
	}
	conn.SetDeadline(time.Time{})
	brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + sshProtocol + "\r\n\r\n")
	if err := brw.Flush(); err != nil {
		conn.Close()
		guest.Close()
		return
	}
	done := make(chan struct{})
	go func() {
		io.Copy(guest, brw)
		guest.Close()
		close(done)
	}()
	io.Copy(conn, guest)
	conn.Close()
	<-done
}

func headerContains(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}
