package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/osamu/localapp/internal/ca"
	"github.com/osamu/localapp/internal/config"
	"github.com/osamu/localapp/internal/platform"
)

// ownerFileName is the file recording the installing user (whoever ran
// `sudo localapp install`). The daemon runs as root, so it reads this to hand
// control.sock over to the installing user
// (DESIGN.md "State directory layout").
// The content is a single line, "<uid>:<gid>\n".
const ownerFileName = "owner"

// reservedDomain reports whether a name may not be used as the development
// domain. Everything under `local` is reserved for mDNS (RFC 6762) and
// everything under `localhost` is given special treatment in many places
// (RFC 6761), so not just the apex but the whole subtree is rejected. In
// particular, with `<name>.local` the two-label name is taken over by mDNS and
// the apex (the dashboard) becomes unreachable.
func reservedDomain(domain string) bool {
	for _, r := range []string{"local", "localhost"} {
		if domain == r || strings.HasSuffix(domain, "."+r) {
			return true
		}
	}
	return false
}

// cmdInstall configures the resolver, creates and trusts the CA, and installs
// the service. It requires root privileges and is idempotent.
func cmdInstall(args []string) int {
	fs := newFlagSet("install")
	domainFlag := fs.String("domain", "", "development domain (default: "+config.DefaultDomain+"; takes precedence over LOCALAPP_DOMAIN)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: sudo localapp install [--domain <name>]")
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
	if os.Geteuid() != 0 {
		errf("install requires root privileges; run `sudo localapp install`")
		return exitError
	}

	cfg := loadConfig()
	if *domainFlag != "" {
		cfg.Domain = strings.ToLower(*domainFlag)
	}
	if err := ca.ValidateDomain(cfg.Domain); err != nil {
		return reportError(fmt.Errorf("validating the domain: %w", err))
	}
	if reservedDomain(cfg.Domain) {
		errf("domain %q cannot be used (it collides with .local / .localhost, reserved by mDNS / RFC 6761)", cfg.Domain)
		return exitError
	}
	p := platform.Current()
	execPath, err := os.Executable()
	if err != nil {
		return reportError(fmt.Errorf("getting the executable path: %w", err))
	}
	daemonEnv := cfg.NonDefaultEnv(p)

	// 1. The state directory. Make it traversable so the CLI can reach
	// control.sock.
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return reportError(fmt.Errorf("creating the state directory (%s): %w", cfg.StateDir, err))
	}
	if err := os.Chmod(cfg.StateDir, 0o755); err != nil {
		return reportError(fmt.Errorf("setting the state directory permissions (%s): %w", cfg.StateDir, err))
	}
	errf("state directory: %s", cfg.StateDir)

	// 2. Record the installing user (only when running through sudo).
	if uid, gid, ok := sudoUser(os.Getenv); ok {
		if err := writeOwner(cfg.StateDir, uid, gid); err != nil {
			return reportError(err)
		}
		errf("installing user: uid=%d gid=%d (will own control.sock)", uid, gid)
	}

	// 3. Create the root CA (an existing one is reused). When the existing CA
	// does not cover the given domain, LoadOrCreate fails (the Name Constraints
	// are baked into the CA, so changing the domain requires
	// uninstall then install).
	rootCA, err := ca.LoadOrCreate(cfg.CADir(), cfg.CertsDir(), cfg.Domain)
	if err != nil {
		if errors.Is(err, ca.ErrDomainMismatch) {
			errf("the existing root CA does not cover %q; remove it with `sudo localapp uninstall` and try again", cfg.Domain)
			return exitError
		}
		return reportError(err)
	}
	// Record the domain used from step 4 on (so uninstall knows which resolver
	// to remove).
	if err := writeDomainRecord(cfg.StateDir, cfg.Domain); err != nil {
		return reportError(err)
	}
	errf("root CA: %s", rootCA.CertPath())

	// 4. Add it to the trust store.
	if err := p.InstallTrust(rootCA.CertPath()); err != nil {
		return reportError(err)
	}
	errf("added to the trust store")

	// 5. Configure the resolver.
	if err := p.InstallResolver(cfg.Domain, cfg.DNSPort); err != nil {
		return reportError(err)
	}
	errf("resolver: *.%s → 127.0.0.1:%d", cfg.Domain, cfg.DNSPort)

	// 6. Install the service. Non-default settings are carried over as the
	// plist EnvironmentVariables.
	if err := p.InstallService(execPath, daemonEnv); err != nil {
		return reportError(err)
	}
	errf("installed the service: %s daemon", execPath)
	if len(daemonEnv) > 0 {
		errf("settings handed to the daemon: %s", formatEnv(daemonEnv))
	}

	errf("done. Check it with `localapp status`")
	return exitOK
}

// cmdUninstall removes everything install created, preserving state needed to
// retry cleanup if stopping the service or removing OS configuration fails.
func cmdUninstall(args []string) int {
	fs := newFlagSet("uninstall")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: sudo localapp uninstall")
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
	if os.Geteuid() != 0 {
		errf("uninstall requires root privileges; run `sudo localapp uninstall`")
		return exitError
	}

	if err := uninstall(loadConfig(), platform.Current(), errf); err != nil {
		return reportError(err)
	}
	errf("done")
	return exitOK
}

// uninstallPlatform is the subset of OS operations needed for cleanup.
type uninstallPlatform interface {
	UninstallService() error
	UninstallResolver(string) error
	UninstallTrust(string) error
}

// uninstall keeps the state directory until all dependent OS cleanup succeeds.
// The platform and progress output are supplied by the caller so partial
// failures can be tested without root or changes to the host configuration.
func uninstall(cfg config.Config, p uninstallPlatform, report func(string, ...any)) error {
	// With install --domain the domain never appears in the environment, so the
	// recorded value wins.
	if domain, ok := readDomainRecord(cfg.StateDir); ok {
		cfg.Domain = domain
	}
	var failed []error

	step := func(label string, fn func() error) bool {
		if err := fn(); err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", label, err))
			return false
		}
		report("%s: removed", label)
		return true
	}

	// Service, then resolver, then trust, then the state directory. The daemon
	// is stopped first.
	if !step("service", p.UninstallService) {
		return fmt.Errorf("cleanup skipped; state preserved for retry: %w", errors.Join(failed...))
	}
	step("resolver", func() error { return p.UninstallResolver(cfg.Domain) })
	step("trust store", func() error { return p.UninstallTrust(ca.CertPath(cfg.CADir())) })
	if len(failed) > 0 {
		return fmt.Errorf("state preserved for retry: %w", errors.Join(failed...))
	}
	step("state directory ("+cfg.StateDir+")", func() error { return os.RemoveAll(cfg.StateDir) })
	return errors.Join(failed...)
}

// cmdCA handles `localapp ca path`.
func cmdCA(args []string) int {
	if len(args) != 1 || args[0] != "path" {
		fmt.Fprintln(os.Stderr, "usage: localapp ca path")
		return exitUsage
	}
	path := ca.CertPath(loadConfig().CADir())
	if _, err := os.Stat(path); err != nil {
		errf("the root CA has not been created yet (%s); run `sudo localapp install`", path)
		return exitError
	}
	fmt.Println(path)
	return exitOK
}

// formatEnv formats an environment variable map as "K=V K=V", keys ascending.
func formatEnv(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+env[k])
	}
	return strings.Join(parts, " ")
}

// domainFileName is the file recording the installed domain. uninstall reads it
// to know which resolver to remove (independently of the environment).
const domainFileName = "domain"

// writeDomainRecord records the installed domain in the state directory.
func writeDomainRecord(stateDir, domain string) error {
	path := filepath.Join(stateDir, domainFileName)
	if err := os.WriteFile(path, []byte(domain+"\n"), 0o644); err != nil {
		return fmt.Errorf("recording the domain (%s): %w", path, err)
	}
	return nil
}

// readDomainRecord returns the recorded installed domain. When nothing was
// recorded, or the content is invalid, ok is false.
func readDomainRecord(stateDir string) (domain string, ok bool) {
	data, err := os.ReadFile(filepath.Join(stateDir, domainFileName))
	if err != nil {
		return "", false
	}
	domain = strings.TrimSpace(string(data))
	if ca.ValidateDomain(domain) != nil {
		return "", false
	}
	return domain, true
}

// sudoUser returns the uid / gid of the user running through sudo. getenv is a
// substitution point for tests.
func sudoUser(getenv func(string) string) (uid, gid int, ok bool) {
	uid, err := strconv.Atoi(getenv("SUDO_UID"))
	if err != nil || uid <= 0 {
		return 0, 0, false
	}
	gid, err = strconv.Atoi(getenv("SUDO_GID"))
	if err != nil || gid < 0 {
		return 0, 0, false
	}
	return uid, gid, true
}

// writeOwner records the installing user in the state directory.
func writeOwner(stateDir string, uid, gid int) error {
	path := filepath.Join(stateDir, ownerFileName)
	content := strconv.Itoa(uid) + ":" + strconv.Itoa(gid) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("recording the installing user (%s): %w", path, err)
	}
	return nil
}

// readOwner returns the recorded installing user. When nothing was recorded, or
// the content is invalid, ok is false.
func readOwner(stateDir string) (uid, gid int, ok bool) {
	data, err := os.ReadFile(filepath.Join(stateDir, ownerFileName))
	if err != nil {
		return 0, 0, false
	}
	rawUID, rawGID, found := strings.Cut(strings.TrimSpace(string(data)), ":")
	if !found {
		return 0, 0, false
	}
	uid, err = strconv.Atoi(rawUID)
	if err != nil || uid <= 0 {
		return 0, 0, false
	}
	gid, err = strconv.Atoi(rawGID)
	if err != nil || gid < 0 {
		return 0, 0, false
	}
	return uid, gid, true
}
