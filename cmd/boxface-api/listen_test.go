package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func selfSigned(t *testing.T, dir string) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "boxface.test"},
		DNSNames:     []string{"boxface.test"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	cert, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certFile, keyFile, pool
}

func TestListenServesPlainTLSAndTailscaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "bfx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	certFile, keyFile, pool := selfSigned(t, dir)
	socket := filepath.Join(dir, "ts.sock")

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.Proto) })
	eps, err := listen(listenConfig{addr: "127.0.0.1:0", tlsAddr: "127.0.0.1:0", tlsCert: certFile, tlsKey: keyFile, tailscaleSocket: socket}, h)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range eps {
		go ep.server.Serve(ep.listener)
		t.Cleanup(func() { ep.server.Close() })
	}
	if len(eps) != 3 || eps[0].server.ConnContext != nil || eps[1].server.ConnContext != nil || eps[2].server.ConnContext == nil {
		t.Fatalf("endpoints = %+v", eps)
	}
	if info, err := os.Stat(socket); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket = %v, %v", info, err)
	}

	get := func(c *http.Client, url string) string {
		t.Helper()
		resp, err := c.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if got := get(http.DefaultClient, "http://"+eps[0].listener.Addr().String()+"/"); got != "HTTP/1.1" {
		t.Fatalf("plain = %q", got)
	}
	tlsClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}}
	if got := get(tlsClient, "https://"+eps[1].listener.Addr().String()+"/"); got != "HTTP/2.0" {
		t.Fatalf("tls = %q", got)
	}
	unixClient := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	if got := get(unixClient, "http://localhost/"); got != "HTTP/1.1" {
		t.Fatalf("socket = %q", got)
	}
}

func TestListenRejectsIncompleteConfiguration(t *testing.T) {
	h := http.NotFoundHandler()
	for name, c := range map[string]listenConfig{
		"nothing":            {},
		"tls without a cert": {tlsAddr: "127.0.0.1:0"},
		"domain and cert":    {tlsAddr: ":443", tlsDomain: "boxface.example", tlsCert: "c", tlsKey: "k"},
		"domain off 443":     {tlsAddr: ":8443", tlsDomain: "boxface.example"},
		"missing cert file":  {tlsAddr: "127.0.0.1:0", tlsCert: "/nonexistent", tlsKey: "/nonexistent"},
	} {
		if _, err := listen(c, h); err == nil {
			t.Fatalf("%s: listen accepted %+v", name, c)
		} else if name == "domain off 443" && !strings.Contains(err.Error(), "port 443") {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	cfg, err := tlsConfig(listenConfig{tlsAddr: ":443", tlsDomain: "boxface.example", acmeDir: t.TempDir()})
	if err != nil || cfg.GetCertificate == nil {
		t.Fatalf("acme config = %+v, %v", cfg, err)
	}
}
