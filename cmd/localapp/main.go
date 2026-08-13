// Command localapp is a single binary that is both the DNS + reverse proxy
// daemon for local development and its CLI.
//
// Conventions (DESIGN.md "CLI"):
//   - exit codes: 0 success / 1 error / 2 usage error
//   - data goes to stdout, diagnostics and logs go to stderr
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/osamu/localapp/internal/config"
	"github.com/osamu/localapp/internal/control"
	"github.com/osamu/localapp/internal/platform"
	"github.com/osamu/localapp/internal/registry"
)

// The exit code convention.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}
	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "add":
		return cmdAdd(rest)
	case "run":
		return cmdRun(rest)
	case "rm":
		return cmdRm(rest)
	case "ls":
		return cmdLs(rest)
	case "status":
		return cmdStatus(rest)
	case "open":
		return cmdOpen(rest)
	case "scan":
		return cmdScan(rest)
	case "skill":
		return cmdSkill(rest)
	case "daemon":
		return cmdDaemon(rest)
	case "logs":
		return cmdLogs(rest)
	case "install":
		return cmdInstall(rest)
	case "uninstall":
		return cmdUninstall(rest)
	case "ca":
		return cmdCA(rest)
	case "help", "-h", "--help":
		usage()
		return exitOK
	case "version", "--version":
		fmt.Println(config.Version)
		return exitOK
	default:
		errf("unknown command: %s", cmd)
		usage()
		return exitUsage
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `localapp - a daemon that gives local development fixed URLs

Usage:
  localapp <command> [arguments...]

Commands:
  add <port>            register a service (idempotent)
                        --app --service --path --strip-path --pid --json
  run [--] <cmd> [args...]  allocate a free port, inject it as PORT, register,
                        and run the command; exits with the command's status
                        --app --service --path --strip-path
  rm <app>[/<service>]  remove a registration
  ls [--json]           list the registrations
  status [--json]       show daemon liveness, listeners and counts
  open <app>            open a registered app in the browser
  scan [--json]         find listening ports that are not registered
  logs [-f] [-n lines]  show the daemon log
  daemon                run the daemon in the foreground
  install [--domain <name>]  set up the resolver, CA trust and service (needs sudo)
  uninstall             remove everything install created (needs sudo)
  ca path               print the path of the root CA certificate
  skill show            print SKILL.md for coding agents
  skill install <claude|codex>    install SKILL.md (--project --dir)
  skill uninstall <claude|codex>  remove the installed SKILL.md
  version               print the version

Environment variables:
  LOCALAPP_DOMAIN       domain suffix (default: localapp)
  LOCALAPP_DNS_PORT     DNS listener port (default: 15353)
  LOCALAPP_HTTP_PORT    HTTP listener port (default: 80)
  LOCALAPP_HTTPS_PORT   HTTPS listener port (default: 443)
  LOCALAPP_STATE_DIR    state directory
  LOCALAPP_SOCKET       path of the control socket
`)
}

func errf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "localapp: "+format+"\n", a...)
}

func loadConfig() config.Config { return config.Load(platform.Current()) }

func newClient() *control.Client { return control.NewClient(loadConfig().SocketPath) }

// reportError prints the error to stderr and returns the exit code.
func reportError(err error) int {
	if errors.Is(err, control.ErrUnavailable) {
		errf("%v", err)
		errf("the daemon may not be running (localapp daemon)")
		return exitError
	}
	errf("%v", err)
	return exitError
}

// parseArgs allows flags and positional arguments to be mixed (for example
// add 5173 --path /api) by repeatedly calling flag.Parse while peeling off
// positional arguments.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// cmdAdd calls PUT /v1/apps/{app}/services/{service}.
func cmdAdd(args []string) int {
	fs := newFlagSet("add")
	app := fs.String("app", "", "app name (default: the normalized basename of the cwd)")
	service := fs.String("service", registry.DefaultService, "service name")
	path := fs.String("path", "", "path mount (for example /api)")
	stripPath := fs.Bool("strip-path", false, "strip the path prefix when forwarding")
	pid := fs.Int("pid", 0, "PID of the process to watch")
	asJSON := fs.Bool("json", false, "print the API response verbatim")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: localapp add <port> [--app name] [--service name] [--path /api] [--strip-path] [--pid PID] [--json]")
		fs.PrintDefaults()
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(pos) != 1 {
		fs.Usage()
		return exitUsage
	}
	port, err := strconv.Atoi(pos[0])
	if err != nil {
		errf("the port is not a number: %s", pos[0])
		return exitUsage
	}

	appName := *app
	if appName == "" {
		appName, err = defaultAppName()
		if err != nil {
			return reportError(err)
		}
	}

	view, raw, err := newClient().PutService(context.Background(), appName, *service, control.ServiceRequest{
		Port:      &port,
		Path:      *path,
		StripPath: *stripPath,
		PID:       *pid,
	})
	if err != nil {
		return reportError(err)
	}
	if *asJSON {
		os.Stdout.Write(raw)
		return exitOK
	}
	fmt.Printf("registered: %s/%s → localhost:%d (%s)\n", view.App, view.Service, view.Port, view.Status)
	for _, u := range view.URLs {
		fmt.Printf("  %s\n", u)
	}
	return exitOK
}

// cmdRm calls DELETE /v1/apps/{app}[/services/{service}].
func cmdRm(args []string) int {
	fs := newFlagSet("rm")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: localapp rm <app>[/<service>]")
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(pos) != 1 {
		fs.Usage()
		return exitUsage
	}
	app, service, hasService := strings.Cut(pos[0], "/")
	if app == "" || (hasService && service == "") || strings.Contains(service, "/") {
		errf("invalid argument: %s (use the form app or app/service)", pos[0])
		return exitUsage
	}

	c := newClient()
	ctx := context.Background()
	if hasService {
		if err := c.DeleteService(ctx, app, service); err != nil {
			return reportError(err)
		}
		fmt.Printf("removed: %s/%s\n", app, service)
		return exitOK
	}
	if err := c.DeleteApp(ctx, app); err != nil {
		return reportError(err)
	}
	fmt.Printf("removed: %s\n", app)
	return exitOK
}

// cmdLs calls GET /v1/apps.
func cmdLs(args []string) int {
	fs := newFlagSet("ls")
	asJSON := fs.Bool("json", false, "print the API response verbatim")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: localapp ls [--json]")
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(pos) != 0 {
		fs.Usage()
		return exitUsage
	}

	resp, raw, err := newClient().ListApps(context.Background())
	if err != nil {
		return reportError(err)
	}
	if *asJSON {
		os.Stdout.Write(raw)
		return exitOK
	}
	if len(resp.Apps) == 0 {
		fmt.Println("no registrations")
		return exitOK
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "APP/SERVICE\tPORT\tSTATUS\tURL")
	for _, a := range resp.Apps {
		for _, s := range a.Services {
			url := ""
			if len(s.URLs) > 0 {
				url = s.URLs[0]
			}
			fmt.Fprintf(tw, "%s/%s\t%d\t%s\t%s\n", a.Name, s.Service, s.Port, s.Status, url)
		}
	}
	return flushOrFail(tw)
}

// cmdStatus calls GET /v1/status. With the daemon stopped it exits 1.
func cmdStatus(args []string) int {
	fs := newFlagSet("status")
	asJSON := fs.Bool("json", false, "print the API response verbatim")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: localapp status [--json]")
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(pos) != 0 {
		fs.Usage()
		return exitUsage
	}

	cfg := loadConfig()
	st, raw, err := control.NewClient(cfg.SocketPath).Status(context.Background())
	if err != nil {
		return reportError(err)
	}
	if *asJSON {
		os.Stdout.Write(raw)
		return exitOK
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "version\t%s\n", st.Version)
	fmt.Fprintf(tw, "uptime\t%s\n", time.Duration(st.UptimeSec)*time.Second)
	fmt.Fprintf(tw, "domain\t%s\n", st.Domain)
	fmt.Fprintf(tw, "socket\t%s\n", cfg.SocketPath)
	for _, k := range sortedKeys(st.Listeners) {
		fmt.Fprintf(tw, "listener.%s\t%s\n", k, st.Listeners[k])
	}
	fmt.Fprintf(tw, "apps\t%d\n", st.Apps)
	fmt.Fprintf(tw, "services\t%d\n", st.Services)
	return flushOrFail(tw)
}

// defaultAppName derives the default app name by normalizing the basename of
// the cwd.
func defaultAppName() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting the current directory: %w", err)
	}
	name := registry.NormalizeName(filepath.Base(cwd))
	if name == "" {
		return "", fmt.Errorf("cannot derive an app name from the cwd (%s); pass --app", cwd)
	}
	return name, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func flushOrFail(tw *tabwriter.Writer) int {
	if err := tw.Flush(); err != nil {
		return reportError(err)
	}
	return exitOK
}
