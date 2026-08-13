package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/osamu/localapp/internal/config"
	"github.com/osamu/localapp/internal/control"
	"github.com/osamu/localapp/internal/scan"
)

// scanResult is the output of `localapp scan --json`.
type scanResult struct {
	Candidates []scanCandidate `json:"candidates"`
}

// scanCandidate is one registration candidate. command is a command line that
// registers it.
type scanCandidate struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Process string `json:"process"`
	Address string `json:"address"`
	Command string `json:"command"`
}

// cmdScan finds listening ports that are not registered and offers them as
// registration candidates (DESIGN.md "CLI", `localapp scan`).
//
// Registered ports and the daemon's own listeners are excluded. Even with the
// daemon stopped, the default listener ports are excluded before the candidates
// are shown.
func cmdScan(args []string) int {
	fs := newFlagSet("scan")
	asJSON := fs.Bool("json", false, "print the candidates as JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: localapp scan [--json]")
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

	ctx := context.Background()
	listeners, err := scan.Listeners(ctx)
	if err != nil {
		if errors.Is(err, scan.ErrLsofMissing) {
			errf("%v. scan uses lsof to enumerate ports", err)
			return exitError
		}
		return reportError(err)
	}

	exclude, daemonUp := excludedPorts(ctx, loadConfig())
	candidates := scan.Filter(listeners, exclude)

	if *asJSON {
		return printScanJSON(candidates)
	}
	if !daemonUp {
		errf("warning: the daemon is unreachable, so registered ports could not be excluded")
	}
	if len(candidates) == 0 {
		fmt.Println("no unregistered listening ports")
		return exitOK
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PORT\tPID\tPROCESS\tADDRESS\tCOMMAND")
	for _, c := range candidates {
		fmt.Fprintf(tw, "%d\t%d\t%s\t%s\t%s\n", c.Port, c.PID, c.Command, c.Address, addCommand(c.Port, c.PID))
	}
	if err := tw.Flush(); err != nil {
		return reportError(err)
	}
	errf("run the command in the directory of the app (the app name comes from the cwd)")
	return exitOK
}

// excludedPorts returns the set of ports to exclude from the candidates.
// daemonUp reports whether the daemon could be reached (when false, registered
// ports have not been excluded).
func excludedPorts(ctx context.Context, cfg config.Config) (map[int]bool, bool) {
	exclude := map[int]bool{}
	// The daemon's own listeners. Even while it is stopped they can be excluded
	// from the configuration.
	for _, addr := range cfg.Listeners() {
		if p := scan.PortOf(addr); p > 0 {
			exclude[p] = true
		}
	}

	resp, _, err := control.NewClient(cfg.SocketPath).ListApps(ctx)
	if err != nil {
		return exclude, false
	}
	for _, app := range resp.Apps {
		for _, svc := range app.Services {
			exclude[svc.Port] = true
		}
	}
	return exclude, true
}

// addCommand builds the registration command for a candidate.
func addCommand(port, pid int) string {
	return fmt.Sprintf("localapp add %d --pid %d", port, pid)
}

func printScanJSON(candidates []scan.Listener) int {
	out := scanResult{Candidates: make([]scanCandidate, 0, len(candidates))}
	for _, c := range candidates {
		out.Candidates = append(out.Candidates, scanCandidate{
			Port:    c.Port,
			PID:     c.PID,
			Process: c.Command,
			Address: c.Address,
			Command: addCommand(c.Port, c.PID),
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return reportError(err)
	}
	return exitOK
}
