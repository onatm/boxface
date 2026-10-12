package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestNotifyReady(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "bfx")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	addr := filepath.Join(dir, "notify")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	t.Setenv("NOTIFY_SOCKET", addr)
	if err := notifyReady(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Fatalf("read %q, %v", buf[:n], err)
	}

	t.Setenv("NOTIFY_SOCKET", "")
	if err := notifyReady(); err != nil {
		t.Fatalf("without NOTIFY_SOCKET: %v", err)
	}
}
