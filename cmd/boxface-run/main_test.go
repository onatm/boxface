package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fakeBroker(t *testing.T, answer func(request) string) func() (brokerConn, error) {
	return func() (brokerConn, error) {
		client, server := net.Pipe()
		go func() {
			var req request
			json.NewDecoder(server).Decode(&req)
			server.Write([]byte(answer(req) + "\n"))
			server.Close()
		}()
		return client, nil
	}
}

func constant(answer string) func(request) string { return func(request) string { return answer } }

func TestAsk(t *testing.T) {
	r, err := ask(fakeBroker(t, constant(`{"env":{"CLAUDE_CODE_OAUTH_TOKEN":"tok"}}`)), request{Consumer: "claude"})
	if err != nil || r.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "tok" {
		t.Fatalf("reply = %+v, %v", r, err)
	}
	if _, err := ask(fakeBroker(t, constant(`{"error":"credentials for this environment are not loaded yet"}`)), request{Consumer: "x"}); err == nil || !strings.Contains(err.Error(), "not loaded") {
		t.Fatalf("broker error = %v", err)
	}
	for _, bad := range []string{`{"env":{"LD_PRELOAD":"/x.so"}}`, `{"env":{"PATH":"/evil"}}`, `{"commands":["../sh"]}`, `{"commands":["-rf"]}`} {
		if _, err := ask(fakeBroker(t, constant(bad)), request{List: true}); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if _, err := ask(func() (brokerConn, error) { return nil, errors.New("refused") }, request{List: true}); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("unreachable = %v", err)
	}
}

func TestParseArgs(t *testing.T) {
	if c, cmd, err := parseArgs([]string{"--command", "gh", "--", "/usr/bin/gh", "pr", "list"}); err != nil || c != "gh" || strings.Join(cmd, " ") != "/usr/bin/gh pr list" {
		t.Fatalf("= %q %q %v", c, cmd, err)
	}
	for _, bad := range [][]string{{}, {"--command", "gh"}, {"--command", "", "--", "x"}, {"--consumer", "gh", "--", "x"}} {
		if _, _, err := parseArgs(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestShellInit(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	for _, name := range []string{"gh", "claude"} {
		os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755)
	}
	dir := filepath.Join(root, "run", "boxface", "bin")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "terraform"), []byte("stale"), 0o700)

	broker := fakeBroker(t, func(req request) string {
		if !req.NoWait {
			t.Errorf("shell init waited: %+v", req)
		}
		if req.List {
			return `{"commands":["claude","gh","missing-tool"]}`
		}
		return `{"env":{"GIT_TOKEN":"it's a secret"}}`
	})
	out, err := shellInit(broker, dir, bin+":/usr/bin")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := "export PATH='" + dir + "':\"$PATH\"\n"
	if !strings.HasPrefix(out, wantPath) || !strings.Contains(out, `export GIT_TOKEN='it'\''s a secret'`) {
		t.Fatalf("output = %q", out)
	}
	shim, err := os.ReadFile(filepath.Join(dir, "gh"))
	if err != nil || string(shim) != "#!/bin/sh\nexec /usr/local/bin/boxface-run --command gh -- '"+filepath.Join(bin, "gh")+"' \"$@\"\n" {
		t.Fatalf("gh shim = %q, %v", shim, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing-tool")); err == nil {
		t.Fatal("made a shim for a command that is not installed")
	}
	if _, err := os.Stat(filepath.Join(dir, "terraform")); err == nil {
		t.Fatal("left a stale shim for an unbound command")
	}
	if got, err := exec.Command("sh", "-c", out+`printf %s "$GIT_TOKEN"`).Output(); err != nil || string(got) != "it's a secret" {
		t.Fatalf("shell evaluated %q, %v", got, err)
	}

	again, err := shellInit(broker, dir, dir+":"+bin)
	if err != nil || strings.Contains(again, "export PATH") {
		t.Fatalf("second init in the same shell = %q, %v", again, err)
	}
	if shim, _ := os.ReadFile(filepath.Join(dir, "gh")); !strings.Contains(string(shim), filepath.Join(bin, "gh")) {
		t.Fatalf("shim pointed at itself when the shim directory was on PATH: %q", shim)
	}

	removed := fakeBroker(t, func(req request) string {
		if req.List {
			return `{"commands":["gh"]}`
		}
		return `{"env":{}}`
	})
	if out, err := shellInit(removed, dir, dir+":"+bin); err != nil || !strings.Contains(out, "unset GIT_TOKEN\n") || strings.Contains(out, "export GIT_TOKEN") {
		t.Fatalf("after the session binding was removed = %q, %v", out, err)
	}
	if out, _ := shellInit(removed, dir, dir+":"+bin); strings.Contains(out, "unset") {
		t.Fatalf("unset repeated once the variable was gone = %q", out)
	}

	if _, err := shellInit(fakeBroker(t, constant(`{"error":"credentials for this environment are not loaded yet"}`)), dir, bin); err == nil {
		t.Fatal("no error when the broker had nothing loaded")
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
	os.WriteFile(path, []byte(`not json`), 0o600)
	if err := markOnboarded(path); err == nil {
		t.Fatal("overwrote a file that is not JSON")
	}
}
