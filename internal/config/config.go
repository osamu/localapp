// Package config resolves the runtime configuration from defaults and
// environment variables.
//
// There is no configuration file (DESIGN.md "設定"). Environment variable
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

// Defaults (DESIGN.md "設定").
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
