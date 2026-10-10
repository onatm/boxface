package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/onatm/boxface/internal/client"
	"github.com/onatm/boxface/internal/lifecycle"
	"github.com/onatm/boxface/internal/version"
	"golang.org/x/term"
)

const usage = `usage: boxfacectl [--json] <command> [arguments]

commands:
  login URL [--code CODE]
  image import SOURCE [--alias NAME]
  image list
  create NAME [--image ID|ALIAS] [--vcpus N] [--memory-mib N] [--disk-gib N] [--no-wait]
  list
  inspect NAME
  start NAME [--no-wait]
  stop NAME [--no-wait]
  delete NAME [--purge-data] [--yes] [--no-wait]
  operation ID [--follow]
  version`

const (
	exitOK          = 0
	exitError       = 1
	exitUsage       = 2
	exitInterrupted = 130
)

var errUsage = errors.New("usage")

type config struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

type cli struct {
	stdin       *bufio.Reader
	stdout      io.Writer
	stderr      io.Writer
	interactive bool
	configPath  string
	json        bool
	newKey      func() string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := &cli{
		stdin:       bufio.NewReader(os.Stdin),
		stdout:      os.Stdout,
		stderr:      os.Stderr,
		interactive: term.IsTerminal(int(os.Stdin.Fd())),
		configPath:  defaultConfigPath(),
		newKey:      randomKey,
	}
	os.Exit(c.run(ctx, os.Args[1:]))
}

func defaultConfigPath() string {
	if p := os.Getenv("BOXFACE_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "boxface", "config.json")
}

func randomKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *cli) run(ctx context.Context, args []string) int {
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool {
		if a == "--json" || a == "-json" {
			c.json = true
			return true
		}
		return false
	})
	if len(args) == 0 {
		fmt.Fprintln(c.stderr, usage)
		return exitUsage
	}
	err := c.dispatch(ctx, args[0], args[1:])
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, errUsage):
		fmt.Fprintln(c.stderr, usage)
		return exitUsage
	case errors.Is(err, context.Canceled):
		return exitInterrupted
	default:
		fmt.Fprintln(c.stderr, "error:", err)
		return exitError
	}
}

func (c *cli) dispatch(ctx context.Context, command string, args []string) error {
	switch command {
	case "version", "-version", "--version":
		fmt.Fprintln(c.stdout, "boxfacectl", version.Version)
		return nil
	case "login":
		return c.login(ctx, args)
	case "image":
		if len(args) == 1 && args[0] == "list" {
			return c.imageList(ctx)
		}
		if len(args) > 0 && args[0] == "import" {
			return c.imageImport(ctx, args[1:])
		}
		return errUsage
	case "create":
		return c.create(ctx, args)
	case "list":
		return c.list(ctx)
	case "inspect":
		if len(args) != 1 {
			return errUsage
		}
		return c.inspect(ctx, args[0])
	case "start", "stop":
		return c.startStop(ctx, command, args)
	case "delete":
		return c.delete(ctx, args)
	case "operation":
		return c.operation(ctx, args)
	}
	return errUsage
}

func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, errUsage
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func (c *cli) loadConfig() (config, error) {
	var cfg config
	b, err := os.ReadFile(c.configPath)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, errors.New("not logged in; run boxfacectl login --server URL")
	}
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(b, &cfg)
}

func (c *cli) client() (*client.Client, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return nil, err
	}
	return client.New(cfg.Server, cfg.Token), nil
}

func (c *cli) prompt(label, what string) (string, error) {
	if !c.interactive {
		return "", fmt.Errorf("%s required, but stdin is not a terminal", what)
	}
	fmt.Fprint(c.stderr, label)
	line, err := c.stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no input received")
	}
	return strings.TrimSpace(line), nil
}

func (c *cli) output(v any, human func(w io.Writer)) {
	if c.json {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		enc.Encode(v)
		return
	}
	human(c.stdout)
}

func (c *cli) login(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	server := fs.String("server", "", "Boxface API URL")
	code := fs.String("code", "", "one-time login code from boxface-api bootstrap")
	rest, err := parse(fs, args)
	if err != nil || len(rest) > 1 || (len(rest) == 1 && *server != "") {
		return errUsage
	}
	if len(rest) == 1 {
		*server = rest[0]
	}
	if *server == "" {
		cfg, err := c.loadConfig()
		if err != nil {
			return errors.New("--server is required for the first login")
		}
		*server = cfg.Server
	}
	if *code == "" {
		var err error
		if *code, err = c.prompt("Login code: ", "login code"); err != nil {
			return err
		}
	}
	token, err := client.New(*server, "").Login(ctx, *code, deviceName())
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(config{Server: *server, Token: token}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(c.configPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(c.configPath, b, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(c.stderr, "logged in to %s\n", *server)
	if err := c.registerDevice(ctx, client.New(*server, token)); err != nil {
		return fmt.Errorf("logged in, but could not register this device's SSH key: %w", err)
	}
	return nil
}

func deviceName() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "boxfacectl"
	}
	if len(host) > 64 {
		host = host[:64]
	}
	return host
}

func (c *cli) registerDevice(ctx context.Context, api *client.Client) error {
	path := filepath.Join(filepath.Dir(c.configPath), "id_ed25519")
	host := deviceName()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "boxfacectl@"+host, "-f", path).CombinedOutput(); err != nil {
			return fmt.Errorf("ssh-keygen: %w: %s", err, strings.TrimSpace(string(out)))
		}
	} else if err != nil {
		return err
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		return err
	}
	k, err := api.AddSSHKey(ctx, host, string(pub))
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stderr, "registered SSH key %s as %s\n", k.Fingerprint, k.Name)
	return nil
}

func (c *cli) imageImport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("image import", flag.ContinueOnError)
	alias := fs.String("alias", "", "also point this image alias at the image, such as main")
	rest, err := parse(fs, args)
	if err != nil || len(rest) != 1 {
		return errUsage
	}
	source := rest[0]
	api, err := c.client()
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stderr, "importing %s (hashing can take a minute)\n", source)
	img, err := api.ImportImage(ctx, source)
	if err != nil {
		return err
	}
	if *alias != "" {
		if err := api.SetImageAlias(ctx, *alias, img.ID); err != nil {
			return err
		}
		if !slices.Contains(img.Aliases, *alias) {
			img.Aliases = append(img.Aliases, *alias)
		}
	}
	c.output(img, func(w io.Writer) { fmt.Fprintf(w, "%s  %s\n", img.ID, img.Digest) })
	return nil
}

func (c *cli) imageList(ctx context.Context) error {
	api, err := c.client()
	if err != nil {
		return err
	}
	list, err := api.Images(ctx)
	if err != nil {
		return err
	}
	c.output(list, func(w io.Writer) {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tALIASES\tSTATUS\tDIGEST\tCREATED")
		for _, img := range list {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", img.ID, strings.Join(img.Aliases, ","), img.Status, img.Digest, img.CreatedAt)
		}
		tw.Flush()
	})
	return nil
}

func (c *cli) create(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	req := lifecycle.CreateRequest{}
	fs.StringVar(&req.ImageID, "image", "", "image ID or alias; the server default when omitted")
	fs.IntVar(&req.VCPUs, "vcpus", 0, "vCPUs; the server default when omitted")
	fs.IntVar(&req.MemoryMiB, "memory-mib", 0, "guest memory in MiB; the server default when omitted")
	fs.IntVar(&req.DiskGiB, "disk-gib", 0, "data disk size in GiB; the server default when omitted")
	noWait := fs.Bool("no-wait", false, "return once the operation is accepted")
	rest, err := parse(fs, args)
	if err != nil || len(rest) != 1 {
		return errUsage
	}
	req.Name = rest[0]
	api, err := c.client()
	if err != nil {
		return err
	}
	acc, err := api.Create(ctx, c.newKey(), req)
	if err != nil {
		return err
	}
	return c.finish(ctx, api, acc, *noWait)
}

func (c *cli) resolve(ctx context.Context, api *client.Client, nameOrID string) (lifecycle.Environment, error) {
	envs, err := api.Environments(ctx)
	if err != nil {
		return lifecycle.Environment{}, err
	}
	var deleted *lifecycle.Environment
	for i, env := range envs {
		if env.ID == nameOrID {
			return env, nil
		}
		if env.Name == nameOrID {
			if env.ObservedState != "deleted" {
				return env, nil
			}
			deleted = &envs[i]
		}
	}
	if deleted != nil {
		return *deleted, nil
	}
	return lifecycle.Environment{}, fmt.Errorf("no environment named %q", nameOrID)
}

func (c *cli) list(ctx context.Context) error {
	api, err := c.client()
	if err != nil {
		return err
	}
	envs, err := api.Environments(ctx)
	if err != nil {
		return err
	}
	c.output(envs, func(w io.Writer) {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSTATE\tDESIRED\tVCPUS\tMEMORY\tDISK\tID")
		for _, e := range envs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d MiB\t%s\t%s\n", e.Name, e.ObservedState, e.DesiredState, e.VCPUs, e.MemoryMiB, e.DiskRetention, e.ID)
		}
		tw.Flush()
	})
	return nil
}

func (c *cli) inspect(ctx context.Context, name string) error {
	api, err := c.client()
	if err != nil {
		return err
	}
	env, err := c.resolve(ctx, api, name)
	if err != nil {
		return err
	}
	c.output(env, func(w io.Writer) { printEnvironment(w, env) })
	return nil
}

func printEnvironment(w io.Writer, e lifecycle.Environment) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"name", e.Name}, {"id", e.ID}, {"state", e.ObservedState}, {"desired", e.DesiredState},
		{"image", e.ImageID}, {"vcpus", fmt.Sprint(e.VCPUs)}, {"memory", fmt.Sprintf("%d MiB", e.MemoryMiB)},
		{"disk", e.DiskID + " (" + e.DiskRetention + ")"}, {"error", e.Error},
	} {
		if row[1] != "" {
			fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
		}
	}
	tw.Flush()
}

func (c *cli) startStop(ctx context.Context, command string, args []string) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	noWait := fs.Bool("no-wait", false, "return once the operation is accepted")
	rest, err := parse(fs, args)
	if err != nil || len(rest) != 1 {
		return errUsage
	}
	api, err := c.client()
	if err != nil {
		return err
	}
	env, err := c.resolve(ctx, api, rest[0])
	if err != nil {
		return err
	}
	var acc lifecycle.Accepted
	if command == "start" {
		acc, err = api.Start(ctx, c.newKey(), env.ID)
	} else {
		acc, err = api.Stop(ctx, c.newKey(), env.ID)
	}
	if err != nil {
		return err
	}
	return c.finish(ctx, api, acc, *noWait)
}

func (c *cli) delete(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	purge := fs.Bool("purge-data", false, "also delete the data disk")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	noWait := fs.Bool("no-wait", false, "return once the operation is accepted")
	rest, err := parse(fs, args)
	if err != nil || len(rest) != 1 {
		return errUsage
	}
	api, err := c.client()
	if err != nil {
		return err
	}
	env, err := c.resolve(ctx, api, rest[0])
	if err != nil {
		return err
	}
	if !*yes {
		what := "its data disk is kept"
		if *purge {
			what = "its data disk is permanently deleted"
		}
		typed, err := c.prompt(fmt.Sprintf("Deleting %s stops it and %s. Type the name to confirm: ", env.Name, what), "confirmation")
		if err != nil {
			return fmt.Errorf("%w; pass --yes to delete without confirmation", err)
		}
		if typed != env.Name {
			return errors.New("name did not match; nothing deleted")
		}
	}
	acc, err := api.Delete(ctx, c.newKey(), env.ID, *purge)
	if err != nil {
		return err
	}
	return c.finish(ctx, api, acc, *noWait)
}

func (c *cli) operation(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("operation", flag.ContinueOnError)
	follow := fs.Bool("follow", false, "follow the operation until it finishes")
	rest, err := parse(fs, args)
	if err != nil || len(rest) != 1 {
		return errUsage
	}
	api, err := c.client()
	if err != nil {
		return err
	}
	op, err := api.Operation(ctx, rest[0])
	if err != nil {
		return err
	}
	if *follow {
		return c.finish(ctx, api, lifecycle.Accepted{OperationID: op.ID, EnvironmentID: op.TargetID}, false)
	}
	c.output(op, func(w io.Writer) {
		fmt.Fprintf(w, "%s %s: %s\n", op.Type, op.ID, op.State)
		for _, s := range op.CompletedSteps {
			fmt.Fprintf(w, "  done: %s\n", s)
		}
		if op.ErrorMessage != "" {
			fmt.Fprintf(w, "  error: %s\n", op.ErrorMessage)
		}
	})
	return nil
}

func (c *cli) finish(ctx context.Context, api *client.Client, acc lifecycle.Accepted, noWait bool) error {
	if noWait {
		c.output(acc, func(w io.Writer) { fmt.Fprintf(w, "operation %s accepted\n", acc.OperationID) })
		return nil
	}
	fmt.Fprintf(c.stderr, "operation %s\n", acc.OperationID)
	op, err := api.Follow(ctx, acc.OperationID, func(step string) { fmt.Fprintf(c.stderr, "  done: %s\n", step) })
	if errors.Is(err, context.Canceled) {
		fmt.Fprintf(c.stderr, "stopped following; the operation continues on the server.\nfollow it again with: boxfacectl operation %s --follow\n", acc.OperationID)
		return err
	}
	if err != nil {
		return err
	}
	if op.State != "succeeded" {
		return fmt.Errorf("operation %s %s: %s", op.ID, op.State, op.ErrorMessage)
	}
	env, err := api.Environment(context.WithoutCancel(ctx), acc.EnvironmentID)
	if err != nil {
		return err
	}
	c.output(env, func(w io.Writer) { printEnvironment(w, env) })
	return nil
}
