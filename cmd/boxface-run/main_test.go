package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func broker(t *testing.T, answer string) func() (brokerConn, error) {
	return func() (brokerConn, error) {
		client, server := net.Pipe()
		go func() {
			var req map[string]string
			json.NewDecoder(server).Decode(&req)
			if req["consumer"] != "claude-code" {
				t.Errorf("request = %v", req)
			}
			server.Write([]byte(answer + "\n"))
			server.Close()
		}()
		return client, nil
	}
}

func TestFetch(t *testing.T) {
	env, err := fetch(broker(t, `{"env":{"CLAUDE_CODE_OAUTH_TOKEN":"tok"}}`), "claude-code")
	if err != nil || env["CLAUDE_CODE_OAUTH_TOKEN"] != "tok" {
		t.Fatalf("env = %v, %v", env, err)
	}
	if env, err := fetch(broker(t, `{"env":{}}`), "claude-code"); err != nil || len(env) != 0 {
		t.Fatalf("unbound = %v, %v", env, err)
	}
	if _, err := fetch(broker(t, `{"error":"credentials for this environment are not loaded yet"}`), "claude-code"); err == nil || !strings.Contains(err.Error(), "not loaded") {
		t.Fatalf("broker error = %v", err)
	}
	if _, err := fetch(broker(t, `{"env":{"LD_PRELOAD":"/x.so"}}`), "claude-code"); err == nil {
		t.Fatal("accepted LD_PRELOAD from the broker")
	}
	if _, err := fetch(func() (brokerConn, error) { return nil, errors.New("refused") }, "claude-code"); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("unreachable = %v", err)
	}
}

func TestParseArgs(t *testing.T) {
	if c, cmd, err := parseArgs([]string{"--consumer", "pi", "--", "/opt/pi", "-p", "x"}); err != nil || c != "pi" || strings.Join(cmd, " ") != "/opt/pi -p x" {
		t.Fatalf("= %q %q %v", c, cmd, err)
	}
	for _, bad := range [][]string{{}, {"--consumer", "pi"}, {"--consumer", "", "--", "x"}, {"pi", "--", "x"}} {
		if _, _, err := parseArgs(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestMarkOnboarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := markOnboarded(path); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(`{"projects":{"/w":{}},"hasCompletedOnboarding":false}`), 0o600)
	if err := markOnboarded(path); err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &state)
	if state["hasCompletedOnboarding"] != true || state["projects"] == nil {
		t.Fatalf("state = %s", b)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	os.WriteFile(path, []byte(`not json`), 0o600)
	if err := markOnboarded(path); err == nil {
		t.Fatal("overwrote a file that is not JSON")
	}
}
