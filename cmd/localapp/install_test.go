package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSudoUser(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantUID int
		wantGID int
		wantOK  bool
	}{
		{"through sudo", map[string]string{"SUDO_UID": "501", "SUDO_GID": "20"}, 501, 20, true},
		{"unset", map[string]string{}, 0, 0, false},
		{"UID is root", map[string]string{"SUDO_UID": "0", "SUDO_GID": "0"}, 0, 0, false},
		{"not a number", map[string]string{"SUDO_UID": "abc", "SUDO_GID": "20"}, 0, 0, false},
		{"missing GID", map[string]string{"SUDO_UID": "501"}, 0, 0, false},
		{"negative GID", map[string]string{"SUDO_UID": "501", "SUDO_GID": "-1"}, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid, gid, ok := sudoUser(func(k string) string { return tt.env[k] })
			if uid != tt.wantUID || gid != tt.wantGID || ok != tt.wantOK {
				t.Errorf("sudoUser = (%d, %d, %v), want (%d, %d, %v)", uid, gid, ok, tt.wantUID, tt.wantGID, tt.wantOK)
			}
		})
	}
}

// The installing user is recorded in the owner file of the state dir and read
// back at daemon startup to hand over ownership of control.sock.
func TestOwnerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := readOwner(dir); ok {
		t.Error("something was read even though nothing was recorded")
	}
	if err := writeOwner(dir, 501, 20); err != nil {
		t.Fatalf("writeOwner: %v", err)
	}
	uid, gid, ok := readOwner(dir)
	if !ok || uid != 501 || gid != 20 {
		t.Errorf("readOwner = (%d, %d, %v), want (501, 20, true)", uid, gid, ok)
	}
	// Re-running does not corrupt it (idempotent).
	if err := writeOwner(dir, 502, 21); err != nil {
		t.Fatalf("writeOwner on re-run: %v", err)
	}
	uid, gid, _ = readOwner(dir)
	if uid != 502 || gid != 21 {
		t.Errorf("after overwriting = (%d, %d), want (502, 21)", uid, gid)
	}
}

func TestReadOwnerRejectsBrokenContent(t *testing.T) {
	for _, content := range []string{"", "abc", "501", "501:", ":20", "0:0", "-1:20", "501:xyz"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ownerFileName), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, ok := readOwner(dir); ok {
			t.Errorf("invalid content %q was accepted", content)
		}
	}
}

// Without root, install / uninstall ask for sudo and exit 1.
func TestInstallRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("skipped because the tests run as root")
	}
	if code := cmdInstall(nil); code != exitError {
		t.Errorf("exit code of install = %d, want %d", code, exitError)
	}
	if code := cmdUninstall(nil); code != exitError {
		t.Errorf("exit code of uninstall = %d, want %d", code, exitError)
	}
}

func TestCAPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPP_STATE_DIR", dir)

	// Without a generated CA it exits 1.
	if code := cmdCA([]string{"path"}); code != exitError {
		t.Errorf("exit code without a generated CA = %d, want %d", code, exitError)
	}
	if err := os.MkdirAll(filepath.Join(dir, "ca"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca", "root.crt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := cmdCA([]string{"path"}); code != exitOK {
		t.Errorf("exit code = %d, want %d", code, exitOK)
	}
	// Usage errors.
	for _, args := range [][]string{nil, {"show"}, {"path", "extra"}} {
		if code := cmdCA(args); code != exitUsage {
			t.Errorf("cmdCA(%v) = %d, want %d", args, code, exitUsage)
		}
	}
}

// Round trip of the domain record.
func TestDomainRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok := readDomainRecord(dir); ok {
		t.Fatal("ok=true even though nothing was recorded")
	}
	if err := writeDomainRecord(dir, "myorg"); err != nil {
		t.Fatal(err)
	}
	got, ok := readDomainRecord(dir)
	if !ok || got != "myorg" {
		t.Errorf("readDomainRecord = %q, %v; want myorg, true", got, ok)
	}
}

// A record with invalid content is ignored.
func TestReadDomainRecordRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	if err := writeDomainRecord(dir, "../etc"); err != nil {
		t.Fatal(err)
	}
	if _, ok := readDomainRecord(dir); ok {
		t.Error("ok=true for an invalid domain")
	}
}

// Reserved domains (including everything under .local / .localhost) are
// rejected by install.
func TestReservedDomains(t *testing.T) {
	for _, d := range []string{"local", "localhost", "dev.local", "a.b.local", "app.localhost"} {
		if !reservedDomain(d) {
			t.Errorf("%q is not treated as a reserved domain", d)
		}
	}
	for _, d := range []string{"localapp", "test", "dev.test", "mylocal", "localhosting"} {
		if reservedDomain(d) {
			t.Errorf("the usable domain %q is treated as reserved", d)
		}
	}
}

// Only non-default settings appear in the environment variable map.
func TestNonDefaultEnv(t *testing.T) {
	cfg := loadConfig()
	if len(nonDefaultEnv(cfg)) != 0 && os.Getenv("LOCALAPP_STATE_DIR") == "" {
		t.Skip("the test environment already sets these environment variables")
	}
	cfg.Domain = "myorg"
	cfg.DNSPort = 5300
	env := nonDefaultEnv(cfg)
	if env["LOCALAPP_DOMAIN"] != "myorg" || env["LOCALAPP_DNS_PORT"] != "5300" {
		t.Errorf("nonDefaultEnv = %v", env)
	}
	if _, ok := env["LOCALAPP_HTTP_PORT"]; ok {
		t.Error("the default HTTP port is present")
	}
	if got := formatEnv(env); got != "LOCALAPP_DNS_PORT=5300 LOCALAPP_DOMAIN=myorg" {
		t.Errorf("formatEnv = %q", got)
	}
}
