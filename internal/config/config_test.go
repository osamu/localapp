package config

import (
	"path/filepath"
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
