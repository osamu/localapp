// Package platform isolates OS-dependent operations.
//
// The core packages (registry / control / proxy / dnsd / ca) do not import it.
// OS-specific implementations live only in platform_<GOOS>.go.
package platform

import "errors"

// ErrNotImplemented reports an operation unimplemented on this platform.
var ErrNotImplemented = errors.New("this operation is not implemented on the current platform")

// Platform is the interface for OS-dependent operations
// (DESIGN.md "Platform abstraction").
type Platform interface {
	// StateDir returns the default path of the state directory.
	// Environment variable overrides are handled by the configuration layer
	// (internal/config).
	StateDir() string

	// InstallResolver configures the OS to delegate DNS for the domain.
	InstallResolver(domain string, dnsPort int) error
	// UninstallResolver removes what InstallResolver configured.
	UninstallResolver(domain string) error

	// InstallTrust adds the root CA to the OS trust store.
	InstallTrust(caCertPath string) error
	// UninstallTrust removes what InstallTrust added.
	UninstallTrust(caCertPath string) error

	// InstallService registers the daemon to run as a service. env holds the
	// environment variables handed to the daemon (LOCALAPP_DOMAIN and so on);
	// nil or empty means it runs with the defaults.
	InstallService(execPath string, env map[string]string) error
	// UninstallService removes the service registration.
	UninstallService() error
}

// Current returns the Platform for the running OS.
func Current() Platform { return current() }
