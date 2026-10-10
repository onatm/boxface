package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/onatm/boxface/internal/api"
	"golang.org/x/crypto/acme/autocert"
)

type listenConfig struct {
	addr            string
	tlsAddr         string
	tlsDomain       string
	tlsCert         string
	tlsKey          string
	acmeDir         string
	tailscaleSocket string
}

type endpoint struct {
	name     string
	listener net.Listener
	server   *http.Server
}

func newServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
}

func tlsConfig(c listenConfig) (*tls.Config, error) {
	switch {
	case c.tlsDomain != "" && (c.tlsCert != "" || c.tlsKey != ""):
		return nil, errors.New("use either -tls-domain or -tls-cert and -tls-key, not both")
	case c.tlsDomain != "":
		if _, port, err := net.SplitHostPort(c.tlsAddr); err != nil || port != "443" {
			return nil, fmt.Errorf("-tls-domain needs -tls-listen on port 443 for the ACME TLS-ALPN-01 challenge, got %q", c.tlsAddr)
		}
		m := &autocert.Manager{Prompt: autocert.AcceptTOS, Cache: autocert.DirCache(c.acmeDir), HostPolicy: autocert.HostWhitelist(c.tlsDomain)}
		cfg := m.TLSConfig()
		cfg.MinVersion = tls.VersionTLS12
		return cfg, nil
	case c.tlsCert != "" && c.tlsKey != "":
		cert, err := tls.LoadX509KeyPair(c.tlsCert, c.tlsKey)
		if err != nil {
			return nil, err
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}, nil
	}
	return nil, errors.New("-tls-listen needs -tls-domain, or both -tls-cert and -tls-key")
}

func listen(c listenConfig, h http.Handler) (eps []endpoint, err error) {
	defer func() {
		if err != nil {
			for _, ep := range eps {
				ep.listener.Close()
			}
		}
	}()
	if c.addr != "" {
		l, err := net.Listen("tcp", c.addr)
		if err != nil {
			return eps, err
		}
		eps = append(eps, endpoint{"http://" + c.addr, l, newServer(h)})
	}
	if c.tlsAddr != "" {
		cfg, err := tlsConfig(c)
		if err != nil {
			return eps, err
		}
		l, err := net.Listen("tcp", c.tlsAddr)
		if err != nil {
			return eps, err
		}
		eps = append(eps, endpoint{"https://" + c.tlsAddr, tls.NewListener(l, cfg), newServer(h)})
	}
	if c.tailscaleSocket != "" {
		if err := os.Remove(c.tailscaleSocket); err != nil && !errors.Is(err, os.ErrNotExist) {
			return eps, err
		}
		l, err := net.Listen("unix", c.tailscaleSocket)
		if err != nil {
			return eps, err
		}
		eps = append(eps, endpoint{"unix:" + c.tailscaleSocket, l, newServer(h)})
		if err := os.Chmod(c.tailscaleSocket, 0o600); err != nil {
			return eps, err
		}
		eps[len(eps)-1].server.ConnContext = api.ViaTailscale
	}
	if len(eps) == 0 {
		return nil, errors.New("no listener configured; set -listen, -tls-listen, or -tailscale-socket")
	}
	return eps, nil
}
