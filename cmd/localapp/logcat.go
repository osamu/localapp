package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/osamu/localapp/internal/logstream"
	"github.com/osamu/localapp/internal/registry"
)

// cmdLogcat prints the captured output of a service (what `run` and
// `logforward` sent to the daemon) and, with -f, follows it over the control
// socket. It is the terminal counterpart of the dashboard's log page, so a
// coding agent can read a dev server's output without a browser.
func cmdLogcat(args []string) int {
	fs := newFlagSet("logcat")
	follow := fs.Bool("f", false, "follow new output")
	lines := fs.Int("n", defaultTailLines, "number of trailing lines to show first (0 for the whole retained window)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: localapp logcat [-f] [-n lines] <app>[/<service>]")
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
	if *lines < 0 {
		errf("-n takes a value of 0 or more")
		return exitUsage
	}
	app, service, err := splitAppService(pos[0])
	if err != nil {
		errf("%v", err)
		return exitUsage
	}
	return logcat(app, service, *follow, *lines, os.Stdout)
}

// splitAppService parses app or app/service; service defaults to web.
func splitAppService(arg string) (app, service string, err error) {
	app, service, hasService := strings.Cut(arg, "/")
	if app == "" || (hasService && service == "") || strings.Contains(service, "/") {
		return "", "", fmt.Errorf("invalid argument: %s (use the form app or app/service)", arg)
	}
	if !hasService {
		service = registry.DefaultService
	}
	return app, service, nil
}

// logcat is cmdLogcat after argument handling; tests drive it directly.
func logcat(app, service string, follow bool, lines int, out io.Writer) int {
	client := newClient()
	hint := func(ev logstream.Event) {
		if !ev.Captured {
			errf("no output captured for %s/%s yet; start it with `localapp run` or pipe it through `localapp logforward`", app, service)
		}
	}
	if !follow {
		ev, _, err := client.Logs(context.Background(), app, service)
		if err != nil {
			return reportError(err)
		}
		hint(ev)
		if _, err := io.WriteString(out, tailLines(ev.Text, lines)); err != nil {
			return reportError(err)
		}
		return exitOK
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	first := true
	err := client.FollowLogs(ctx, app, service, 0, func(event string, _ uint64, ev logstream.Event) error {
		switch event {
		case "reset", "append":
			text := ev.Text
			if first {
				hint(ev)
				text = tailLines(text, lines)
				first = false
			}
			_, err := io.WriteString(out, text)
			return err
		case "gone":
			return errServiceRemoved
		}
		return nil
	})
	switch {
	case err == nil || errors.Is(err, context.Canceled):
		return exitOK
	case errors.Is(err, errServiceRemoved):
		errf("%s/%s was removed", app, service)
		return exitError
	default:
		return reportError(err)
	}
}

var errServiceRemoved = errors.New("service removed")

// tailLines returns the last n lines of text (all of it when n is 0). A
// trailing partial line counts as a line.
func tailLines(text string, n int) string {
	if n == 0 || text == "" {
		return text
	}
	end := len(text)
	if text[end-1] == '\n' {
		end--
	}
	i := end
	for ; n > 0 && i > 0; n-- {
		j := strings.LastIndexByte(text[:i], '\n')
		if j < 0 {
			return text
		}
		i = j
	}
	return text[i+1:]
}
