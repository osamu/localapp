package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/osamu/localapp/internal/registry"
)

// cmdLogForward copies stdin to stdout unchanged and forwards the same bytes to the
// daemon's log stream for an already registered service. It is the way to get
// Web logs for processes that `run` cannot wrap (services registered with
// `add`, `docker compose logs`, a log file via `tail -f`).
//
// logforward never exits because of the daemon's state: exiting would break the pipe
// and kill the producer with SIGPIPE. Missing registration or an unreachable
// daemon is reported once on stderr while output keeps flowing to stdout.
func cmdLogForward(args []string) int {
	fs := newFlagSet("logforward")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: <command> 2>&1 | localapp logforward [<app>[/<service>]]")
		fmt.Fprintln(os.Stderr, "  app defaults to the normalized basename of the cwd, service to "+registry.DefaultService)
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(pos) > 1 {
		fs.Usage()
		return exitUsage
	}
	app, service := "", registry.DefaultService
	if len(pos) == 1 {
		if app, service, err = splitAppService(pos[0]); err != nil {
			errf("%v", err)
			return exitUsage
		}
	} else if app, err = defaultAppName(); err != nil {
		return reportError(err)
	}
	return forwardStdin(app, service, os.Stdin, os.Stdout)
}

// forwardStdin is cmdLogForward after argument handling; tests drive it directly.
func forwardStdin(app, service string, in io.Reader, out io.Writer) int {
	client := newClient()
	warn := func(msg string) { errf("%s", msg) }

	// Report the registration state up front so a forgotten `add` is visible
	// before the first batch of output.
	failing := false
	if view, _, err := client.GetApp(context.Background(), app); err != nil {
		warn(forwardFailureMessage(err, app, service))
		failing = true
	} else {
		found := false
		for _, s := range view.Services {
			found = found || s.Service == service
		}
		if !found {
			warn(app + "/" + service + " is not registered; output is shown but not captured until it is (localapp add <port> --app " + app + " --service " + service + ")")
			failing = true
		}
	}

	up := newLogForwarder(client, app, service, warn, failing)

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.MultiWriter(out, up), in)
		copied <- err
	}()
	select {
	case err := <-copied:
		up.Close()
		if err != nil {
			return reportError(fmt.Errorf("copying output: %w", err))
		}
		return exitOK
	case <-sigCtx.Done():
		// The producer received the same signal; flush what is queued and
		// leave the blocked read behind — the process is exiting.
		up.Close()
		return exitError
	}
}
