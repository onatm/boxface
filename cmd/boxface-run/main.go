package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	hostCID    = 2
	brokerPort = 1025
	self       = "/usr/local/bin/boxface-run"
	usage      = `usage:
  boxface-run --command NAME -- COMMAND [ARGS...]
  boxface-run --shell-init`
)

type brokerConn interface {
	io.ReadWriteCloser
	SetDeadline(t time.Time) error
}

type request struct {
	Consumer string `json:"consumer,omitempty"`
	List     bool   `json:"list,omitempty"`
	NoWait   bool   `json:"nowait,omitempty"`
}

type reply struct {
	Env      map[string]string `json:"env"`
	Commands []string          `json:"commands"`
	Error    string            `json:"error"`
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--shell-init" {
		out, err := shellInit(dialBroker, shimDir(), os.Getenv("PATH"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "boxface: stored credentials are unavailable in this shell: %v\n", err)
		}
		fmt.Print(out)
		return
	}
	name, command, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	r, err := ask(dialBroker, request{Consumer: name})
	if err != nil {
		fmt.Fprintf(os.Stderr, "boxface: credentials for %s are unavailable: %v\nrun %s directly to start it without stored credentials\n", name, err, command[0])
		os.Exit(1)
	}
	if r.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
		if home, err := os.UserHomeDir(); err == nil {
			if err := markOnboarded(filepath.Join(home, ".claude.json")); err != nil {
				fmt.Fprintf(os.Stderr, "boxface: could not skip Claude Code onboarding: %v\n", err)
			}
		}
	}
	environ := os.Environ()
	for k, v := range r.Env {
		environ = append(environ, k+"="+v)
	}
	err = syscall.Exec(command[0], command, environ)
	fmt.Fprintf(os.Stderr, "boxface: run %s: %v\n", command[0], err)
	os.Exit(127)
}

func parseArgs(args []string) (string, []string, error) {
	if len(args) < 4 || args[0] != "--command" || args[1] == "" || args[2] != "--" {
		return "", nil, errors.New("usage")
	}
	return args[1], args[3:], nil
}

func shimDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "boxface", "bin")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("boxface-%d", os.Getuid()), "bin")
}

func ask(dial func() (brokerConn, error), req request) (reply, error) {
	conn, err := dial()
	if err != nil {
		return reply{}, fmt.Errorf("credential broker unreachable: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(45 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return reply{}, err
	}
	var r reply
	if err := json.NewDecoder(io.LimitReader(conn, 1<<20)).Decode(&r); err != nil {
		return reply{}, fmt.Errorf("no reply from the credential broker: %w", err)
	}
	if r.Error != "" {
		return reply{}, errors.New(r.Error)
	}
	for k := range r.Env {
		if !validVariable(k) {
			return reply{}, fmt.Errorf("broker sent an invalid variable name %q", k)
		}
	}
	for _, c := range r.Commands {
		if !validCommand(c) {
			return reply{}, fmt.Errorf("broker sent an invalid command name %q", c)
		}
	}
	return r, nil
}

func validVariable(k string) bool {
	if k == "" || strings.HasPrefix(k, "LD_") || slices.Contains([]string{"PATH", "HOME"}, k) {
		return false
	}
	for i, c := range k {
		if !(c == '_' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func validCommand(c string) bool {
	if c == "" || len(c) > 64 || c[0] == '.' || c[0] == '-' {
		return false
	}
	for _, r := range c {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("._+-", r)) {
			return false
		}
	}
	return true
}

func shellInit(dial func() (brokerConn, error), dir, path string) (string, error) {
	list, err := ask(dial, request{List: true, NoWait: true})
	if err != nil {
		return "", err
	}
	session, err := ask(dial, request{Consumer: "session", NoWait: true})
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	searchPath := slices.DeleteFunc(filepath.SplitList(path), func(p string) bool { return p == dir || p == "" })
	wanted := map[string]bool{}
	for _, name := range list.Commands {
		real := lookPath(name, searchPath)
		if real == "" {
			continue
		}
		wanted[name] = true
		shim := fmt.Sprintf("#!/bin/sh\nexec %s --command %s -- %s \"$@\"\n", self, name, shellQuote(real))
		tmp := filepath.Join(dir, "."+name+".tmp")
		if err := os.WriteFile(tmp, []byte(shim), 0o700); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			return "", err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !wanted[e.Name()] {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	var b strings.Builder
	if !slices.Contains(filepath.SplitList(path), dir) {
		fmt.Fprintf(&b, "export PATH=%s:\"$PATH\"\n", shellQuote(dir))
	}
	keys := make([]string, 0, len(session.Env))
	for k := range session.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	namesFile := filepath.Join(filepath.Dir(dir), "session-variables")
	if previous, err := os.ReadFile(namesFile); err == nil {
		for _, k := range strings.Fields(string(previous)) {
			if validVariable(k) && session.Env[k] == "" {
				fmt.Fprintf(&b, "unset %s\n", k)
			}
		}
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(session.Env[k]))
	}
	if err := os.WriteFile(namesFile, []byte(strings.Join(keys, "\n")+"\n"), 0o600); err != nil {
		return "", err
	}
	return b.String(), nil
}

func lookPath(name string, dirs []string) string {
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func markOnboarded(path string) error {
	state := map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &state); err != nil {
			return fmt.Errorf("%s is not JSON: %w", path, err)
		}
	}
	if state["hasCompletedOnboarding"] == true {
		return nil
	}
	state["hasCompletedOnboarding"] = true
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".boxface-tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
