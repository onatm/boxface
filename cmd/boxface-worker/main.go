package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/onatm/boxface/internal/version"
	"github.com/onatm/boxface/internal/worker"
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	socket := flag.String("socket", "/run/boxface/worker.sock", "Unix socket to serve the worker API on")
	apiUser := flag.String("api-user", "boxface", "user allowed to call the worker, in addition to root")
	stateDir := flag.String("state-dir", "/var/lib/boxface", "directory for environment state and data disks")
	imagesDir := flag.String("images-dir", "/var/lib/boxface/images", "directory holding image artifact sets")
	firecracker := flag.String("firecracker", "/usr/local/bin/firecracker", "Firecracker binary")
	vmUser := flag.String("vm-user", "boxface-vm", "unprivileged user that runs Firecracker")
	pool := flag.String("pool", "172.20.0.0/16", "IPv4 pool for per-environment /30 networks")
	authorizedKeys := flag.String("authorized-keys", "/etc/boxface/authorized_keys", "SSH public keys seeded into new data disks")
	readyTimeout := flag.Duration("ready-timeout", 2*time.Minute, "how long to wait for guest readiness")
	stopTimeout := flag.Duration("stop-timeout", 30*time.Second, "how long to wait for a clean guest shutdown")
	flag.Parse()

	if *showVersion {
		fmt.Println("boxface-worker", version.Version)
		return
	}
	if err := run(*socket, *apiUser, worker.Config{
		StateDir:           *stateDir,
		ImagesDir:          *imagesDir,
		Firecracker:        *firecracker,
		VMUser:             *vmUser,
		Pool:               netip.MustParsePrefix(*pool),
		AuthorizedKeysFile: *authorizedKeys,
		ReadyTimeout:       *readyTimeout,
		StopTimeout:        *stopTimeout,
	}); err != nil {
		slog.Error("boxface-worker", "error", err)
		os.Exit(1)
	}
}

func run(socket, apiUser string, cfg worker.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	api, err := user.Lookup(apiUser)
	if err != nil {
		return fmt.Errorf("api user: %w", err)
	}
	apiUID, _ := strconv.Atoi(api.Uid)
	apiGID, _ := strconv.Atoi(api.Gid)

	host, err := worker.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer host.Close()

	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		return err
	}
	os.Remove(socket)
	l, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if err := os.Chown(socket, 0, apiGID); err != nil {
		return err
	}
	if err := os.Chmod(socket, 0o660); err != nil {
		return err
	}

	srv := &http.Server{Handler: worker.NewHandler(host)}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("boxface-worker listening", "socket", socket, "api_uid", apiUID)
	if err := notifyReady(); err != nil {
		slog.Warn("notify systemd of readiness", "error", err)
	}
	err = srv.Serve(worker.PeerListener{Listener: l, Allow: func(uid uint32) bool { return uid == 0 || uid == uint32(apiUID) }})
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
