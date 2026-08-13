package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/osamu/localapp/internal/control"
	"github.com/osamu/localapp/internal/registry"
)

// portHintDelay is how long cmdRun waits before checking whether anything
// listens on the allocated port. Dev servers that honor PORT are up well
// within this; a silent miss means the command likely ignored the variable.
const portHintDelay = 5 * time.Second

// cmdRun allocates a free port, registers it, and runs the given command with
// the port injected as the PORT environment variable (the convention
// popularized by PaaS platforms and honored by most web frameworks).
//
// The registration is the same idempotent upsert as `add` and persists after
// the command exits (DESIGN.md "Registration lifecycle"). cmdRun exits with
// the command's exit code.
func cmdRun(args []string) int {
	fs := newFlagSet("run")
	app := fs.String("app", "", "app name (default: the normalized basename of the cwd)")
	service := fs.String("service", registry.DefaultService, "service name")
	path := fs.String("path", "", "path mount (for example /api)")
	stripPath := fs.Bool("strip-path", false, "strip the path prefix when forwarding")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: localapp run [--app name] [--service name] [--path /api] [--strip-path] [--] <command> [args...]")
		fs.PrintDefaults()
	}
	// Deliberately not parseArgs: flag parsing must stop at the first
	// positional argument so that the command's own flags are left intact
	// (localapp run --app x npm run dev -- --host).
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		fs.Usage()
		return exitUsage
	}

	appName := *app
	if appName == "" {
		var err error
		appName, err = defaultAppName()
		if err != nil {
			return reportError(err)
		}
	}

	port, err := allocatePort()
	if err != nil {
		return reportError(err)
	}

	// Register before spawning so that an unreachable daemon or an invalid
	// name fails before the command starts.
	client := newClient()
	view, _, err := client.PutService(context.Background(), appName, *service, control.ServiceRequest{
		Port:      &port,
		Path:      *path,
		StripPath: *stripPath,
	})
	if err != nil {
		return reportError(err)
	}

	child := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	child.Env = append(os.Environ(), "PORT="+strconv.Itoa(port))
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		return reportError(fmt.Errorf("starting %s: %w", cmdArgs[0], err))
	}

	// Record the child PID. Best effort: the registration itself already
	// succeeded above.
	if _, _, err := client.PutService(context.Background(), appName, *service, control.ServiceRequest{
		Port:      &port,
		Path:      *path,
		StripPath: *stripPath,
		PID:       child.Process.Pid,
	}); err != nil {
		errf("recording the pid: %v", err)
	}

	if len(view.URLs) > 0 {
		errf("%s → localhost:%d (PORT injected, pid %d)", view.URLs[0], port, child.Process.Pid)
	}

	// Forward termination signals to the command.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigc)
	go func() {
		for s := range sigc {
			_ = child.Process.Signal(s)
		}
	}()

	// Hint when the command does not honor PORT.
	go func() {
		time.Sleep(portHintDelay)
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
		if err != nil {
			errf("nothing is listening on port %d yet — the command may not honor the PORT environment variable; start it normally and use `localapp add <actual-port>` instead", port)
			return
		}
		conn.Close()
	}()

	err = child.Wait()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &exitErr):
		if code := exitErr.ExitCode(); code >= 0 {
			return code
		}
		return exitError // terminated by a signal
	default:
		return reportError(err)
	}
}

// allocatePort asks the kernel for a free TCP port on the loopback interface.
// The port is released before the command binds it; the race window is
// accepted for a development tool.
func allocatePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocating a port: %w", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
