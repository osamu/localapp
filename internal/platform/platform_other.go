//go:build !darwin

package platform

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// unsupportedPlatform is a placeholder for unsupported operating systems.
// Linux support is Phase 6 (DESIGN.md "Platform abstraction").
type unsupportedPlatform struct{}

func current() Platform { return unsupportedPlatform{} }

// StateDir returns an XDG-style default. Real support comes in Phase 6.
func (unsupportedPlatform) StateDir() string { return "/var/lib/localapp" }

func (unsupportedPlatform) InstallResolver(domain string, dnsPort int) error { return unsupported() }
func (unsupportedPlatform) UninstallResolver(domain string) error            { return unsupported() }
func (unsupportedPlatform) InstallTrust(caCertPath string) error             { return unsupported() }
func (unsupportedPlatform) UninstallTrust(caCertPath string) error           { return unsupported() }
func (unsupportedPlatform) InstallService(execPath string, env map[string]string) error {
	return unsupported()
}
func (unsupportedPlatform) UninstallService() error { return unsupported() }

// OpenURL opens a URL in the default browser (`localapp open`).
// Even where running the daemon as a service is unsupported, `open` against an
// already running localapp works as long as xdg-open is available.
func OpenURL(url string) error {
	out, err := exec.Command("xdg-open", url).CombinedOutput()
	if err != nil {
		return fmt.Errorf("running xdg-open: %w\n  %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func unsupported() error {
	return fmt.Errorf("%s is not supported: %w", runtime.GOOS, ErrNotImplemented)
}
