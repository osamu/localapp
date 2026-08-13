package ca

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateDomain(t *testing.T) {
	ok := []string{"localapp", "test", "dev-local", "a", "example.test", "x1"}
	for _, d := range ok {
		if err := ValidateDomain(d); err != nil {
			t.Errorf("ValidateDomain(%q) = %v, want nil", d, err)
		}
	}
	ng := []string{"", ".", ".localapp", "localapp.", "Localapp", "local app", "loc/app", "..", strings.Repeat("a", 64)}
	for _, d := range ng {
		err := ValidateDomain(d)
		if err == nil {
			t.Errorf("ValidateDomain(%q) = nil, want error", d)
			continue
		}
		if !errors.Is(err, ErrInvalidHost) {
			t.Errorf("ValidateDomain(%q): errors.Is(ErrInvalidHost) = false (%v)", d, err)
		}
	}
}

func TestValidateHostAccepts(t *testing.T) {
	hosts := []string{
		"localapp",                            // apex (dashboard)
		"app1.localapp",                       // app
		"api.app1.localapp",                   // service subdomain
		"a.localapp",                          // single-character label
		"my-app-2.localapp",                   // hyphens and digits
		"a.b.c.d.e.f.localapp",                // deep nesting
		"xn--pckua2a7gp15o.localapp",          // punycode ([a-z0-9-] only)
		strings.Repeat("a", 63) + ".localapp", // label length limit
	}
	for _, h := range hosts {
		if err := validateHost(h, "localapp"); err != nil {
			t.Errorf("validateHost(%q) = %v, want nil", h, err)
		}
	}
}

// TestValidateHostRejects checks that SNI validation rejects names outside the
// domain, path traversal and invalid characters (DESIGN.md "Security": SNI).
func TestValidateHostRejects(t *testing.T) {
	hosts := []string{
		"",                       // empty SNI
		"evil.com",               // outside the domain
		"localapp.evil.com",      // suffix spoofing
		"app1.notlocalapp",       // partial match
		"app1.localappx",         // partial suffix match
		"xlocalapp",              // partial apex match
		"App1.localapp",          // uppercase
		"APP1.LOCALAPP",          // uppercase
		"app1.localapp.",         // trailing dot
		".localapp",              // empty label
		"..localapp",             // empty label (..)
		"a..localapp",            // empty label (..)
		"../evil",                // path traversal
		"../../etc/passwd",       // path traversal
		"../evil.localapp",       // path traversal with a valid suffix
		"a/../b.localapp",        // path traversal with a valid suffix
		"..%2fevil.localapp",     // encoded path traversal
		"foo/bar.localapp",       // slash
		"foo\\bar.localapp",      // backslash
		"/etc/passwd.localapp",   // absolute path
		"~/evil.localapp",        // tilde
		"app1.localapp/../root",  // path at the end
		"app 1.localapp",         // whitespace
		"app1\x00.localapp",      // NUL
		"app1.localapp\x00.evil", // NUL plus suffix spoofing
		"app_1.localapp",         // underscore
		"アプリ.localapp",           // non-ASCII
	}
	for _, h := range hosts {
		err := validateHost(h, "localapp")
		if err == nil {
			t.Errorf("validateHost(%q) = nil, want error", h)
			continue
		}
		if !errors.Is(err, ErrInvalidHost) {
			t.Errorf("validateHost(%q): errors.Is(ErrInvalidHost) = false (%v)", h, err)
		}
	}
}

func TestValidateHostLengthLimits(t *testing.T) {
	if err := validateHost(strings.Repeat("a", 64)+".localapp", "localapp"); err == nil {
		t.Error("a 64-character label was accepted, want error")
	}
	long := strings.Repeat("ab.", 90) + "localapp"
	if len(long) <= maxHostLen {
		t.Fatalf("test data is too short: %d", len(long))
	}
	if err := validateHost(long, "localapp"); err == nil {
		t.Error("a host name longer than 253 characters was accepted, want error")
	}
}
