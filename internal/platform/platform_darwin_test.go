//go:build darwin

package platform

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner records external command invocations. It never runs the real
// security / launchctl binaries.
type fakeRunner struct {
	calls [][]string
	// fail maps the first two words of name+args to the response to return.
	fail map[string]string
}

func (f *fakeRunner) run(name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	key := name
	if len(args) > 0 {
		key = name + " " + args[0]
	}
	if out, ok := f.fail[key]; ok {
		return out, errors.New("exit status 1")
	}
	return "", nil
}

func (f *fakeRunner) call(i int) string { return strings.Join(f.calls[i], " ") }

func newTestPlatform(t *testing.T) (darwinPlatform, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()
	r := &fakeRunner{fail: map[string]string{}}
	return darwinPlatform{
		resolverDir: filepath.Join(dir, "resolver"),
		plistPath:   filepath.Join(dir, "LaunchDaemons", "dev.localapp.plist"),
		keychain:    filepath.Join(dir, "System.keychain"),
		run:         r.run,
	}, r
}

func TestInstallResolverWritesNameserverAndPort(t *testing.T) {
	p, _ := newTestPlatform(t)
	if err := p.InstallResolver("localapp", 15353); err != nil {
		t.Fatalf("InstallResolver: %v", err)
	}
	path := filepath.Join(p.resolverDir, "localapp")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("resolver file was not created: %v", err)
	}
	if !strings.Contains(string(got), "nameserver 127.0.0.1\n") {
		t.Errorf("missing nameserver line:\n%s", got)
	}
	if !strings.Contains(string(got), "port 15353\n") {
		t.Errorf("missing port line:\n%s", got)
	}

	// Idempotent: a re-run succeeds and leaves the contents unchanged.
	if err := p.InstallResolver("localapp", 15353); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	again, _ := os.ReadFile(path)
	if string(again) != string(got) {
		t.Errorf("contents changed on re-run")
	}

	if err := p.UninstallResolver("localapp"); err != nil {
		t.Fatalf("UninstallResolver: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("resolver file still present")
	}
	// Removing an absent file is success too.
	if err := p.UninstallResolver("localapp"); err != nil {
		t.Errorf("UninstallResolver when absent: %v", err)
	}
}

// The domain becomes a file name, so values containing path separators are
// rejected (DESIGN.md "セキュリティ", install / uninstall rows).
func TestResolverPathRejectsTraversal(t *testing.T) {
	p, _ := newTestPlatform(t)
	for _, domain := range []string{"../etc/passwd", "a/b", "..", "", "local app", "localapp/", "-bad", "l..ocal/../x"} {
		if _, err := p.resolverPath(domain); err == nil {
			t.Errorf("domain %q was accepted", domain)
		}
		if err := p.InstallResolver(domain, 15353); err == nil {
			t.Errorf("InstallResolver(%q) succeeded", domain)
		}
	}
	for _, domain := range []string{"localapp", "test.localapp"} {
		if _, err := p.resolverPath(domain); err != nil {
			t.Errorf("valid domain %q was rejected: %v", domain, err)
		}
	}
}

func TestInstallResolverRejectsBadPort(t *testing.T) {
	p, _ := newTestPlatform(t)
	for _, port := range []int{0, -1, 70000} {
		if err := p.InstallResolver("localapp", port); err == nil {
			t.Errorf("port %d was accepted", port)
		}
	}
}

func TestPlistContent(t *testing.T) {
	got := plistContent("/usr/local/bin/localapp", nil)
	s := string(got)
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		"<key>Label</key>\n\t<string>dev.localapp</string>",
		"<string>/usr/local/bin/localapp</string>",
		"<string>daemon</string>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<key>RunAtLoad</key>\n\t<true/>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("plist is missing %q:\n%s", want, s)
		}
	}
}

// The executable path is embedded into XML verbatim, so it must be escaped.
func TestPlistContentEscapesPath(t *testing.T) {
	got := plistContent("/tmp/a&b<c>/localapp", nil)
	s := string(got)
	if strings.Contains(s, "a&b<c>") {
		t.Errorf("not escaped:\n%s", s)
	}
	if !strings.Contains(s, "a&amp;b&lt;c&gt;") {
		t.Errorf("expected escaped form is missing:\n%s", s)
	}
}

func TestInstallServiceWritesPlistAndBootstraps(t *testing.T) {
	p, r := newTestPlatform(t)
	if err := p.InstallService("/usr/local/bin/localapp", nil); err != nil {
		t.Fatalf("InstallService: %v", err)
	}
	info, err := os.Stat(p.plistPath)
	if err != nil {
		t.Fatalf("plist was not created: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("plist permissions = %v, want 0644", info.Mode().Perm())
	}
	if len(r.calls) != 2 {
		t.Fatalf("command calls = %v", r.calls)
	}
	if got := r.call(0); got != "launchctl bootout system/dev.localapp" {
		t.Errorf("call 1 = %q", got)
	}
	if got := r.call(1); got != "launchctl bootstrap system "+p.plistPath {
		t.Errorf("call 2 = %q", got)
	}
}

func TestInstallServiceRejectsRelativePath(t *testing.T) {
	p, _ := newTestPlatform(t)
	if err := p.InstallService("localapp", nil); err == nil {
		t.Error("a relative path was accepted")
	}
}

func TestUninstallServiceRemovesPlist(t *testing.T) {
	p, r := newTestPlatform(t)
	if err := p.InstallService("/usr/local/bin/localapp", nil); err != nil {
		t.Fatalf("InstallService: %v", err)
	}
	r.calls = nil
	if err := p.UninstallService(); err != nil {
		t.Fatalf("UninstallService: %v", err)
	}
	if _, err := os.Stat(p.plistPath); !os.IsNotExist(err) {
		t.Error("plist still present")
	}
	if got := r.call(0); got != "launchctl bootout system/dev.localapp" {
		t.Errorf("bootout was not called: %v", r.calls)
	}
	// Succeeds even when unregistered and the plist is absent.
	r.fail["launchctl bootout"] = "Could not find service in domain"
	if err := p.UninstallService(); err != nil {
		t.Errorf("UninstallService when unregistered: %v", err)
	}
}

func TestUninstallServicePropagatesRealFailure(t *testing.T) {
	p, r := newTestPlatform(t)
	r.fail["launchctl bootout"] = "Operation not permitted"
	if err := p.UninstallService(); err == nil {
		t.Error("bootout failure was ignored")
	}
}

func TestInstallTrustInvokesSecurity(t *testing.T) {
	p, r := newTestPlatform(t)
	cert := writeTestCert(t)
	if err := p.InstallTrust(cert); err != nil {
		t.Fatalf("InstallTrust: %v", err)
	}
	want := "security add-trusted-cert -d -r trustRoot -k " + p.keychain + " " + cert
	if got := r.call(0); got != want {
		t.Errorf("call = %q, want %q", got, want)
	}
}

func TestInstallTrustFailsWhenCertMissing(t *testing.T) {
	p, r := newTestPlatform(t)
	if err := p.InstallTrust(filepath.Join(t.TempDir(), "none.crt")); err == nil {
		t.Error("a missing certificate was accepted")
	}
	if len(r.calls) != 0 {
		t.Errorf("a command was executed: %v", r.calls)
	}
}

// UninstallTrust both removes the trust settings and deletes the certificate
// from the keychain. The deletion target is identified by its SHA-1
// fingerprint.
func TestUninstallTrustRemovesTrustAndCertificate(t *testing.T) {
	p, r := newTestPlatform(t)
	cert := writeTestCert(t)
	if err := p.UninstallTrust(cert); err != nil {
		t.Fatalf("UninstallTrust: %v", err)
	}
	if got, want := r.call(0), "security remove-trusted-cert -d "+cert; got != want {
		t.Errorf("call 1 = %q, want %q", got, want)
	}
	want := "security delete-certificate -Z " + testCertFingerprint(t, cert) + " " + p.keychain
	if got := r.call(1); got != want {
		t.Errorf("call 2 = %q, want %q", got, want)
	}
}

func TestUninstallTrustIsIdempotent(t *testing.T) {
	p, r := newTestPlatform(t)
	// With no certificate file it does nothing and succeeds.
	if err := p.UninstallTrust(filepath.Join(t.TempDir(), "none.crt")); err != nil {
		t.Errorf("certificate absent: %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("a command was executed: %v", r.calls)
	}
	// Output meaning "not registered" is treated as success.
	cert := writeTestCert(t)
	r.fail["security remove-trusted-cert"] = "SecTrustSettingsRemoveTrustSettings: The specified item could not be found in the keychain."
	r.fail["security delete-certificate"] = "security: nothing found to delete"
	if err := p.UninstallTrust(cert); err != nil {
		t.Errorf("UninstallTrust when unregistered: %v", err)
	}
	// Any other failure propagates.
	r.fail["security delete-certificate"] = "security: Operation not permitted"
	if err := p.UninstallTrust(cert); err == nil {
		t.Error("deletion failure was ignored")
	}
}

// writeTestCert writes a self-signed certificate as PEM and returns its path.
func writeTestCert(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localapp test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "root.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func testCertFingerprint(t *testing.T, path string) string {
	t.Helper()
	der, err := readCertDER(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum(der)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// EnvironmentVariables is embedded in the plist with a deterministic key order.
func TestPlistContentWithEnv(t *testing.T) {
	got := plistContent("/usr/local/bin/localapp", map[string]string{
		"LOCALAPP_DOMAIN":   "myorg",
		"LOCALAPP_DNS_PORT": "5300",
	})
	s := string(got)
	for _, want := range []string{
		"<key>EnvironmentVariables</key>",
		"<key>LOCALAPP_DNS_PORT</key>\n\t\t<string>5300</string>",
		"<key>LOCALAPP_DOMAIN</key>\n\t\t<string>myorg</string>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("plist does not contain %q:\n%s", want, s)
		}
	}
	// Keys ascending (DNS_PORT before DOMAIN).
	if strings.Index(s, "LOCALAPP_DNS_PORT") > strings.Index(s, "LOCALAPP_DOMAIN") {
		t.Error("EnvironmentVariables keys are not sorted")
	}
}

// Without env, the EnvironmentVariables key itself must not appear.
func TestPlistContentWithoutEnv(t *testing.T) {
	got := plistContent("/usr/local/bin/localapp", nil)
	if strings.Contains(string(got), "EnvironmentVariables") {
		t.Error("EnvironmentVariables present even though env was nil")
	}
}
