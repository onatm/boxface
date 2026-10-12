package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const usage = `usage:
  spike server -socket PATH [-target HOST:PORT]
  spike tunnel URL           (SSH ProxyCommand: Upgrade to URL, then pipe stdio)
  spike events URL           (print the gap between server-sent events)`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "server":
		err = server(os.Args[2:])
	case "tunnel":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		err = tunnel(os.Args[2])
	case "events":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		err = events(os.Args[2])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spike:", err)
		os.Exit(1)
	}
}

func server(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	socket := fs.String("socket", "/run/boxface-spike/api.sock", "Unix socket for tailscale serve")
	target := fs.String("target", "127.0.0.1:22", "SSH server the tunnel dials")
	fs.Parse(args)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"login":           r.Header.Get("Tailscale-User-Login"),
			"name":            r.Header.Get("Tailscale-User-Name"),
			"x_forwarded_for": r.Header.Get("X-Forwarded-For"),
			"proto":           r.Proto,
		})
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 {
			n = 10
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		for i := 1; i <= n; i++ {
			fmt.Fprintf(w, "id: %d\nevent: tick\ndata: {}\n\n", i)
			w.(http.Flusher).Flush()
			if i < n {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(time.Second):
				}
			}
		}
	})
	mux.HandleFunc("GET /ssh", func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "boxface-ssh") {
			http.Error(w, "upgrade to boxface-ssh required", http.StatusUpgradeRequired)
			return
		}
		backend, err := net.DialTimeout("tcp", *target, 5*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			backend.Close()
			return
		}
		opened := time.Now()
		login := r.Header.Get("Tailscale-User-Login")
		slog.Info("tunnel opened", "login", login, "proto", r.Proto)
		brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: boxface-ssh\r\n\r\n")
		brw.Flush()

		closed := make(chan string, 2)
		go func() {
			n, err := io.Copy(backend, brw)
			closed <- fmt.Sprintf("client side ended after %d bytes: %v", n, err)
			backend.Close()
		}()
		go func() {
			n, err := io.Copy(conn, backend)
			closed <- fmt.Sprintf("ssh side ended after %d bytes: %v", n, err)
			conn.Close()
		}()
		first := <-closed
		<-closed
		slog.Info("tunnel closed", "login", login, "duration", time.Since(opened).Round(time.Second).String(), "first", first)
	})

	os.Remove(*socket)
	l, err := net.Listen("unix", *socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(*socket, 0o600); err != nil {
		return err
	}
	slog.Info("spike listening", "socket", *socket, "target", *target)
	return (&http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}).Serve(l)
}

func dial(u *url.URL) (net.Conn, error) {
	host := u.Hostname()
	port := u.Port()
	if u.Scheme == "https" {
		if port == "" {
			port = "443"
		}
		return tls.Dial("tcp", net.JoinHostPort(host, port), &tls.Config{ServerName: host, NextProtos: []string{"http/1.1"}})
	}
	if port == "" {
		port = "80"
	}
	return net.Dial("tcp", net.JoinHostPort(host, port))
}

func tunnel(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	conn, err := dial(u)
	if err != nil {
		return err
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: boxface-ssh\r\n\r\n", u.RequestURI(), u.Host)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("upgrade refused: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, os.Stdin)
		done <- err
	}()
	go func() {
		_, err := io.Copy(os.Stdout, br)
		done <- err
	}()
	return <-done
}

func events(raw string) error {
	resp, err := http.Get(raw)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("events: %s", resp.Status)
	}
	scanner := bufio.NewScanner(resp.Body)
	last := time.Now()
	for scanner.Scan() {
		if id, ok := strings.CutPrefix(scanner.Text(), "id: "); ok {
			now := time.Now()
			fmt.Printf("event %s after %.2fs (%s)\n", id, now.Sub(last).Seconds(), resp.Proto)
			last = now
		}
	}
	return scanner.Err()
}
