package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/onatm/boxface/internal/api"
	"github.com/onatm/boxface/internal/auth"
	"github.com/onatm/boxface/internal/images"
	"github.com/onatm/boxface/internal/lifecycle"
	"github.com/onatm/boxface/internal/sshkeys"
	"github.com/onatm/boxface/internal/store"
	"github.com/onatm/boxface/internal/version"
	"github.com/onatm/boxface/internal/worker"
)

const usage = `usage:
  boxface-api serve [flags]
  boxface-api bootstrap -owner NAME [-tailscale-login LOGIN] [-db PATH]
  boxface-api revoke -owner NAME [-db PATH]
  boxface-api -version`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "-version", "--version":
		fmt.Println("boxface-api", version.Version)
		return
	case "serve":
		err = serve(os.Args[2:])
	case "bootstrap":
		err = bootstrap(os.Args[2:])
	case "revoke":
		err = revoke(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		slog.Error("boxface-api", "error", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var lc listenConfig
	fs.StringVar(&lc.addr, "listen", "127.0.0.1:7443", "plain HTTP address to serve the API on; empty disables it")
	fs.StringVar(&lc.tlsAddr, "tls-listen", "", "HTTPS address to serve the API on, such as :443")
	fs.StringVar(&lc.tlsDomain, "tls-domain", "", "domain to obtain a Let's Encrypt certificate for; needs -tls-listen on port 443")
	fs.StringVar(&lc.tlsCert, "tls-cert", "", "certificate file for -tls-listen, instead of -tls-domain")
	fs.StringVar(&lc.tlsKey, "tls-key", "", "private key file for -tls-cert")
	fs.StringVar(&lc.acmeDir, "acme-dir", "/var/lib/boxface/api/acme", "directory caching Let's Encrypt account and certificates")
	fs.StringVar(&lc.tailscaleSocket, "tailscale-socket", "", "Unix socket for tailscale serve, such as /var/lib/boxface/api/tailscale.sock")
	dbPath := fs.String("db", "/var/lib/boxface/api/boxface.db", "SQLite database")
	workerSocket := fs.String("worker-socket", "/run/boxface/worker.sock", "host worker socket")
	importDir := fs.String("import-dir", "/var/lib/boxface/import", "directory the owner stages images in")
	imagesDir := fs.String("images-dir", "/var/lib/boxface/images", "directory holding image artifact sets")
	reconcileInterval := fs.Duration("reconcile-interval", 30*time.Second, "how often to compare environment state with the worker")
	var capacity lifecycle.Capacity
	fs.IntVar(&capacity.Environments, "max-environments", 4, "maximum environments; 0 means unlimited")
	fs.IntVar(&capacity.VCPUs, "max-vcpus", 0, "maximum vCPUs across environments; 0 means unlimited")
	fs.IntVar(&capacity.MemoryMiB, "max-memory-mib", 0, "maximum guest memory across environments; 0 means unlimited")
	fs.IntVar(&capacity.DiskGiB, "max-disk-gib", 0, "maximum data disk size across environments, including retained disks; 0 means unlimited")
	fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	client := worker.NewClient(*workerSocket)
	runner := lifecycle.NewRunner(ctx, db, client)
	server := &api.Server{
		Auth:         auth.New(db),
		Lifecycle:    lifecycle.NewService(db, runner, capacity),
		Images:       images.NewRegistry(db, *importDir, *imagesDir),
		SSHKeys:      sshkeys.NewStore(db),
		Gateway:      client,
		Capacity:     capacity,
		PollInterval: 500 * time.Millisecond,
	}
	eps, err := listen(lc, server.Handler())
	if err != nil {
		runner.Wait()
		return err
	}

	reconcile(ctx, runner, client)
	runner.Kick()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(*reconcileInterval):
				reconcile(ctx, runner, client)
			}
		}
	}()
	failed := make(chan error, len(eps))
	for _, ep := range eps {
		slog.Info("boxface-api listening", "address", ep.name, "version", version.Version)
		go func() {
			if err := ep.server.Serve(ep.listener); !errors.Is(err, http.ErrServerClosed) {
				failed <- fmt.Errorf("%s: %w", ep.name, err)
			}
		}()
	}
	select {
	case <-ctx.Done():
	case err = <-failed:
		stop()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, ep := range eps {
		ep.server.Shutdown(shutdown)
	}
	runner.Wait()
	return err
}

func reconcile(ctx context.Context, runner *lifecycle.Runner, inv lifecycle.Inventory) {
	stopped, err := runner.Reconcile(ctx, inv)
	if err != nil {
		slog.Warn("reconcile with worker failed; will retry", "error", err)
	}
	for _, id := range stopped {
		slog.Info("environment marked stopped: VM not running", "environment", id)
	}
}

func ownerFlags(name string, args []string, extra ...func(fs *flag.FlagSet)) (string, string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	owner := fs.String("owner", "", "owner name")
	for _, f := range extra {
		f(fs)
	}
	dbPath := fs.String("db", "/var/lib/boxface/api/boxface.db", "SQLite database")
	fs.Parse(args)
	if *owner == "" {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	return *owner, *dbPath
}

func bootstrap(args []string) error {
	var tailscaleLogin string
	owner, dbPath := ownerFlags("bootstrap", args, func(fs *flag.FlagSet) {
		fs.StringVar(&tailscaleLogin, "tailscale-login", "", "also let this Tailscale user log in without a code through tailscale serve, such as alice@github")
	})
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	authStore := auth.New(db)
	code, err := authStore.Bootstrap(context.Background(), owner, 15*time.Minute)
	if err != nil {
		return err
	}
	if tailscaleLogin != "" {
		if _, err := authStore.BindOwnerIdentity(context.Background(), owner, auth.ProviderTailscale, tailscaleLogin); err != nil {
			return err
		}
		fmt.Printf("Tailscale login %s can now log in as %s without a code through tailscale serve\n", tailscaleLogin, owner)
	}
	fmt.Printf("one-time login code for %s, valid for 15 minutes:\n%s\n", owner, code)
	return nil
}

func revoke(args []string) error {
	owner, dbPath := ownerFlags("revoke", args)
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	n, err := auth.New(db).RevokeAll(context.Background(), owner)
	if err != nil {
		return err
	}
	fmt.Printf("revoked %d tokens for %s\n", n, owner)
	return nil
}
