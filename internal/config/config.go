// Package config resolves the runtime configuration from defaults and
// environment variables.
//
// There is no configuration file (DESIGN.md "Configuration"). Environment variable
// resolution is confined to this package; other layers receive resolved
// values.
package config

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/osamu/localapp/internal/platform"
)

// Version is the binary version. Release builds override it with
// -ldflags "-X github.com/osamu/localapp/internal/config.Version=v0.1.0".
var Version = "dev"

// Defaults (DESIGN.md "Configuration").
const (
	DefaultDomain    = "localapp"
	DefaultDNSPort   = 15353
	DefaultHTTPPort  = 80
	DefaultHTTPSPort = 443
)

// Config is the resolved runtime configuration.
type Config struct {
	Domain     string
	StateDir   string
	SocketPath string
	DNSPort    int
	HTTPPort   int
	HTTPSPort  int
}

// Load returns the platform defaults with environment variables applied.
func Load(p platform.Platform) Config {
	c := Config{
		Domain:    envString("LOCALAPP_DOMAIN", DefaultDomain),
		StateDir:  envString("LOCALAPP_STATE_DIR", p.StateDir()),
		DNSPort:   envInt("LOCALAPP_DNS_PORT", DefaultDNSPort),
		HTTPPort:  envInt("LOCALAPP_HTTP_PORT", DefaultHTTPPort),
		HTTPSPort: envInt("LOCALAPP_HTTPS_PORT", DefaultHTTPSPort),
	}
	c.SocketPath = envString("LOCALAPP_SOCKET", filepath.Join(c.StateDir, "control.sock"))
	return c
}

// NonDefaultEnv serializes runtime overrides for a service manager, which does
// not inherit the installing process's environment. A default socket follows
// StateDir, so only an independently overridden socket needs its own entry.
func (c Config) NonDefaultEnv(p platform.Platform) map[string]string {
	env := map[string]string{}
	if c.Domain != DefaultDomain {
		env["LOCALAPP_DOMAIN"] = c.Domain
	}
	if c.DNSPort != DefaultDNSPort {
		env["LOCALAPP_DNS_PORT"] = strconv.Itoa(c.DNSPort)
	}
	if c.HTTPPort != DefaultHTTPPort {
		env["LOCALAPP_HTTP_PORT"] = strconv.Itoa(c.HTTPPort)
	}
	if c.HTTPSPort != DefaultHTTPSPort {
		env["LOCALAPP_HTTPS_PORT"] = strconv.Itoa(c.HTTPSPort)
	}
	if c.StateDir != p.StateDir() {
		env["LOCALAPP_STATE_DIR"] = c.StateDir
	}
	if c.SocketPath != filepath.Join(c.StateDir, "control.sock") {
		env["LOCALAPP_SOCKET"] = c.SocketPath
	}
	return env
}

// RegistryPath returns the path of the registry file.
func (c Config) RegistryPath() string { return filepath.Join(c.StateDir, "registry.json") }

// LogPath returns the path of the daemon log.
func (c Config) LogPath() string { return filepath.Join(c.StateDir, "daemon.log") }

// CADir returns the directory holding the root CA.
func (c Config) CADir() string { return filepath.Join(c.StateDir, "ca") }

// CertsDir returns the directory of the leaf certificate cache.
func (c Config) CertsDir() string { return filepath.Join(c.StateDir, "certs") }

// Listeners builds the listener map returned by GET /v1/status.
func (c Config) Listeners() map[string]string {
	return map[string]string{
		"dns":   "127.0.0.1:" + strconv.Itoa(c.DNSPort),
		"http":  "127.0.0.1:" + strconv.Itoa(c.HTTPPort),
		"https": "127.0.0.1:" + strconv.Itoa(c.HTTPSPort),
	}
}

func envString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		return def
	}
	return n
}
