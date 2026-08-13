package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/osamu/localapp/internal/control"
	"github.com/osamu/localapp/internal/platform"
	"github.com/osamu/localapp/internal/registry"
)

// openURL launches the browser. It is a substitution point for tests.
var openURL = platform.OpenURL

// cmdOpen opens the URL of a registered app in the browser.
// An unregistered app exits 1 (DESIGN.md "CLI").
func cmdOpen(args []string) int {
	fs := newFlagSet("open")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: localapp open <app>")
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
	app := pos[0]

	view, _, err := newClient().GetApp(context.Background(), app)
	if err != nil {
		if control.IsNotFound(err) {
			errf("app `%s` is not registered (check with `localapp ls`)", app)
			return exitError
		}
		return reportError(err)
	}

	url, ok := primaryURL(view)
	if !ok {
		errf("app `%s` has no service with a URL", app)
		return exitError
	}
	// The URL is data, so it goes to stdout: it can be opened by hand even if
	// launching the browser fails.
	fmt.Println(url)
	if err := openURL(url); err != nil {
		return reportError(fmt.Errorf("launching the browser (%s): %w", url, err))
	}
	return exitOK
}

// primaryURL returns the representative URL of an app: that of the default
// service (web) when present, otherwise the primary URL of the first service.
func primaryURL(view control.AppView) (string, bool) {
	for _, svc := range view.Services {
		if svc.Service == registry.DefaultService && len(svc.URLs) > 0 {
			return svc.URLs[0], true
		}
	}
	for _, svc := range view.Services {
		if len(svc.URLs) > 0 {
			return svc.URLs[0], true
		}
	}
	return "", false
}
