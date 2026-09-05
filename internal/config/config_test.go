package config

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/osamu/localapp/internal/platform"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("LOCALAPP_DOMAIN", "")
	t.Setenv("LOCALAPP_STATE_DIR", "")
	t.Setenv("LOCALAPP_SOCKET", "")
	t.Setenv("LOCALAPP_DNS_PORT", "")
	t.Setenv("LOCALAPP_HTTP_PORT", "")
	t.Setenv("LOCALAPP_HTTPS_PORT", "")

	p := platform.Current()
	c := Load(p)
	if c.Domain != DefaultDomain {
		t.Errorf("Domain = %q, want %q", c.Domain, DefaultDomain)
	}
	if c.StateDir != p.StateDir() {
		t.Errorf("StateDir = %q, want platform default %q", c.StateDir, p.StateDir())
	}
	if want := filepath.Join(p.StateDir(), "control.sock"); c.SocketPath != want {
		t.Errorf("SocketPath = %q, want %q", c.SocketPath, want)
	}
	if c.DNSPort != DefaultDNSPort || c.HTTPPort != DefaultHTTPPort || c.HTTPSPort != DefaultHTTPSPort {
		t.Errorf("default ports = %d/%d/%d", c.DNSPort, c.HTTPPort, c.HTTPSPort)
	}
}

// LOCALAPP_STATE_DIR overrides the platform default, and the socket follows it.
func TestLoadStateDirOverride(t *testing.T) {
	t.Setenv("LOCALAPP_STATE_DIR", "/tmp/localapp-test")
	t.Setenv("LOCALAPP_SOCKET", "")

	c := Load(platform.Current())
	if c.StateDir != "/tmp/localapp-test" {
		t.Errorf("StateDir = %q", c.StateDir)
	}
	if c.SocketPath != "/tmp/localapp-test/control.sock" {
		t.Errorf("SocketPath = %q, want a path under the state dir", c.SocketPath)
	}
	if c.RegistryPath() != "/tmp/localapp-test/registry.json" {
		t.Errorf("RegistryPath = %q", c.RegistryPath())
	}
}

// LOCALAPP_SOCKET can be set independently of the state dir.
func TestLoadSocketOverride(t *testing.T) {
	t.Setenv("LOCALAPP_STATE_DIR", "/tmp/localapp-test")
	t.Setenv("LOCALAPP_SOCKET", "/tmp/other.sock")

	c := Load(platform.Current())
	if c.SocketPath != "/tmp/other.sock" {
		t.Errorf("SocketPath = %q", c.SocketPath)
	}
}

func TestLoadDomainAndPorts(t *testing.T) {
	t.Setenv("LOCALAPP_DOMAIN", "test")
	t.Setenv("LOCALAPP_DNS_PORT", "5300")
	t.Setenv("LOCALAPP_HTTP_PORT", "14380")
	t.Setenv("LOCALAPP_HTTPS_PORT", "14443")

	c := Load(platform.Current())
	if c.Domain != "test" {
		t.Errorf("Domain = %q", c.Domain)
	}
	if c.DNSPort != 5300 || c.HTTPPort != 14380 || c.HTTPSPort != 14443 {
		t.Errorf("ports = %d/%d/%d", c.DNSPort, c.HTTPPort, c.HTTPSPort)
	}
	l := c.Listeners()
	if l["https"] != "127.0.0.1:14443" {
		t.Errorf("Listeners = %+v", l)
	}
}

// An invalid port setting falls back to the default.
func TestLoadInvalidPortFallsBack(t *testing.T) {
	t.Setenv("LOCALAPP_DNS_PORT", "not-a-number")
	t.Setenv("LOCALAPP_HTTP_PORT", "0")
	t.Setenv("LOCALAPP_HTTPS_PORT", "99999")

	c := Load(platform.Current())
	if c.DNSPort != DefaultDNSPort || c.HTTPPort != DefaultHTTPPort || c.HTTPSPort != DefaultHTTPSPort {
		t.Errorf("ports = %d/%d/%d, want defaults", c.DNSPort, c.HTTPPort, c.HTTPSPort)
	}
}

// Every listener binds to 127.0.0.1 (DESIGN.md "Security").
func TestListenersBindLoopbackOnly(t *testing.T) {
	c := Load(platform.Current())
	for name, addr := range c.Listeners() {
		if len(addr) < 10 || addr[:10] != "127.0.0.1:" {
			t.Errorf("listener %s = %q, want a 127.0.0.1 bind", name, addr)
		}
	}
}

// Service-manager serialization must recreate the complete configuration in a
// fresh environment, including custom sockets outside the state directory.
func TestNonDefaultEnvRoundTrip(t *testing.T) {
	p := platform.Current()
	keys := []string{"LOCALAPP_DOMAIN", "LOCALAPP_DNS_PORT", "LOCALAPP_HTTP_PORT", "LOCALAPP_HTTPS_PORT", "LOCALAPP_STATE_DIR", "LOCALAPP_SOCKET"}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	defaults := Load(p)
	tests := []struct {
		name   string
		change func(*Config)
		want   map[string]string
	}{
		{"defaults", func(c *Config) {}, map[string]string{}},
		{"domain", func(c *Config) { c.Domain = "dev.test" }, map[string]string{"LOCALAPP_DOMAIN": "dev.test"}},
		{"dns", func(c *Config) { c.DNSPort = 25353 }, map[string]string{"LOCALAPP_DNS_PORT": "25353"}},
		{"http", func(c *Config) { c.HTTPPort = 18080 }, map[string]string{"LOCALAPP_HTTP_PORT": "18080"}},
		{"https", func(c *Config) { c.HTTPSPort = 18443 }, map[string]string{"LOCALAPP_HTTPS_PORT": "18443"}},
		{"state with derived socket", func(c *Config) {
			c.StateDir = "/tmp/custom-state"
			c.SocketPath = filepath.Join(c.StateDir, "control.sock")
		}, map[string]string{"LOCALAPP_STATE_DIR": "/tmp/custom-state"}},
		{"independent socket", func(c *Config) { c.SocketPath = "/tmp/custom.sock" }, map[string]string{"LOCALAPP_SOCKET": "/tmp/custom.sock"}},
		{"all overrides", func(c *Config) {
			c.Domain = "dev.test"
			c.DNSPort, c.HTTPPort, c.HTTPSPort = 25353, 18080, 18443
			c.StateDir, c.SocketPath = "/tmp/custom-state", "/tmp/custom.sock"
		}, map[string]string{
			"LOCALAPP_DOMAIN": "dev.test", "LOCALAPP_DNS_PORT": "25353",
			"LOCALAPP_HTTP_PORT": "18080", "LOCALAPP_HTTPS_PORT": "18443",
			"LOCALAPP_STATE_DIR": "/tmp/custom-state", "LOCALAPP_SOCKET": "/tmp/custom.sock",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaults
			tt.change(&cfg)
			env := cfg.NonDefaultEnv(p)
			if !reflect.DeepEqual(env, tt.want) {
				t.Fatalf("environment = %v, want %v", env, tt.want)
			}
			for _, key := range keys {
				t.Setenv(key, "")
			}
			for key, value := range env {
				t.Setenv(key, value)
			}
			if got := Load(p); got != cfg {
				t.Errorf("round trip = %+v, want %+v", got, cfg)
			}
		})
	}
}
