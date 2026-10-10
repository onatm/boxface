package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

const (
	hostCID    = 2
	brokerPort = 1025
	usage      = "usage: boxface-run --consumer NAME -- COMMAND [ARGS...]"
)

type brokerConn interface {
	io.ReadWriteCloser
	SetDeadline(t time.Time) error
}

type reply struct {
	Env   map[string]string `json:"env"`
	Error string            `json:"error"`
}

func main() {
	consumer, command, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	env, err := fetch(dialBroker, consumer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "boxface: credentials for %s are unavailable: %v\nrun %s directly to start it without stored credentials\n", consumer, err, command[0])
		os.Exit(1)
	}
	if consumer == "claude-code" && env["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
		if home, err := os.UserHomeDir(); err == nil {
			if err := markOnboarded(filepath.Join(home, ".claude.json")); err != nil {
				fmt.Fprintf(os.Stderr, "boxface: could not skip Claude Code onboarding: %v\n", err)
			}
		}
	}
	environ := os.Environ()
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	err = syscall.Exec(command[0], command, environ)
	fmt.Fprintf(os.Stderr, "boxface: run %s: %v\n", command[0], err)
	os.Exit(127)
}

func parseArgs(args []string) (string, []string, error) {
	if len(args) < 4 || args[0] != "--consumer" || args[1] == "" || args[2] != "--" {
		return "", nil, errors.New("usage")
	}
	return args[1], args[3:], nil
}

func fetch(dial func() (brokerConn, error), consumer string) (map[string]string, error) {
	conn, err := dial()
	if err != nil {
		return nil, fmt.Errorf("credential broker unreachable: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(45 * time.Second))
	if err := json.NewEncoder(conn).Encode(map[string]string{"consumer": consumer}); err != nil {
		return nil, err
	}
	var r reply
	if err := json.NewDecoder(io.LimitReader(conn, 1<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("no reply from the credential broker: %w", err)
	}
	if r.Error != "" {
		return nil, errors.New(r.Error)
	}
	for k := range r.Env {
		if !validName(k) {
			return nil, fmt.Errorf("broker sent an invalid variable name %q", k)
		}
	}
	return r.Env, nil
}

func validName(k string) bool {
	if k == "" {
		return false
	}
	for i, c := range k {
		if !(c == '_' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return !slices.Contains([]string{"PATH", "HOME", "LD_PRELOAD", "LD_LIBRARY_PATH"}, k)
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
