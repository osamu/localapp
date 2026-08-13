//go:build darwin

package platform

import (
	"bytes"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Write targets on macOS. All of them are fixed paths and never concatenate
// user input (DESIGN.md "Security", install / uninstall rows). The only
// exception is the domain in the resolver file name, whose format is validated
// by validDomain.
const (
	defaultResolverDir = "/etc/resolver"
	defaultPlistPath   = "/Library/LaunchDaemons/dev.localapp.plist"
	defaultKeychain    = "/Library/Keychains/System.keychain"

	// serviceLabel is the LaunchDaemon label; it matches the plist file name.
	serviceLabel = "dev.localapp"
	// serviceTarget is the launchctl service target (system domain).
	serviceTarget = "system/" + serviceLabel
)

// validDomain is the accepted format for a domain used as a resolver file
// name. It rejects values containing path separators or `..`.
var validDomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// darwinPlatform is the macOS implementation.
//
// The fields are substitution points for tests; Current() uses the default
// fixed paths and really executes commands.
type darwinPlatform struct {
	resolverDir string
	plistPath   string
	keychain    string
	// run executes an external command. Tests replace it with a recorder.
	run func(name string, args ...string) (string, error)
}

func current() Platform {
	return darwinPlatform{
		resolverDir: defaultResolverDir,
		plistPath:   defaultPlistPath,
		keychain:    defaultKeychain,
		run:         runCommand,
	}
}

// StateDir is the macOS state directory (DESIGN.md "Platform abstraction").
func (darwinPlatform) StateDir() string { return "/usr/local/var/localapp" }

// InstallResolver creates /etc/resolver/<domain>. It overwrites an existing
// file and is idempotent, including when the contents are unchanged.
func (p darwinPlatform) InstallResolver(domain string, dnsPort int) error {
	path, err := p.resolverPath(domain)
	if err != nil {
		return err
	}
	if dnsPort < 1 || dnsPort > 65535 {
		return fmt.Errorf("installing resolver: invalid port number (%d)", dnsPort)
	}
	if err := os.MkdirAll(p.resolverDir, 0o755); err != nil {
		return fmt.Errorf("creating resolver directory (%s): %w", p.resolverDir, err)
	}
	if err := os.WriteFile(path, []byte(resolverContent(dnsPort)), 0o644); err != nil {
		return fmt.Errorf("writing resolver file (%s): %w", path, err)
	}
	return nil
}

// UninstallResolver removes /etc/resolver/<domain>. A missing file is success.
func (p darwinPlatform) UninstallResolver(domain string) error {
	path, err := p.resolverPath(domain)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing resolver file (%s): %w", path, err)
	}
	return nil
}

// InstallTrust adds the root CA to the System keychain as trusted.
// Re-running it for the same certificate overwrites, so it is idempotent.
func (p darwinPlatform) InstallTrust(caCertPath string) error {
	if _, err := os.Stat(caCertPath); err != nil {
		return fmt.Errorf("cannot read root CA certificate (%s): %w", caCertPath, err)
	}
	out, err := p.run("security", "add-trusted-cert", "-d", "-r", "trustRoot", "-k", p.keychain, caCertPath)
	if err != nil {
		return fmt.Errorf("trusting root CA (%s): %w%s", caCertPath, err, indentOutput(out))
	}
	return nil
}

// UninstallTrust removes the trust settings and deletes the certificate itself
// from the System keychain. A missing certificate file or an unregistered
// certificate is also success.
func (p darwinPlatform) UninstallTrust(caCertPath string) error {
	der, err := readCertDER(caCertPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// Remove the trust settings. security fails when they are not registered,
	// which we treat as already removed.
	if out, rerr := p.run("security", "remove-trusted-cert", "-d", caCertPath); rerr != nil {
		if !isAlreadyAbsent(out) {
			return fmt.Errorf("removing root CA trust (%s): %w%s", caCertPath, rerr, indentOutput(out))
		}
	}
	// Deletion from the keychain identifies the target by its SHA-1 fingerprint.
	sum := sha1.Sum(der)
	fp := strings.ToUpper(hex.EncodeToString(sum[:]))
	if out, derr := p.run("security", "delete-certificate", "-Z", fp, p.keychain); derr != nil {
		if !isAlreadyAbsent(out) {
			return fmt.Errorf("deleting root CA from keychain (%s): %w%s", fp, derr, indentOutput(out))
		}
	}
	return nil
}

// InstallService generates the LaunchDaemon plist and registers it with
// launchd. When already registered it boots out first and then bootstraps, so
// it is idempotent. env is handed to the daemon as the plist
// EnvironmentVariables.
func (p darwinPlatform) InstallService(execPath string, env map[string]string) error {
	if !filepath.IsAbs(execPath) {
		return fmt.Errorf("installing service: the executable path must be absolute (%s)", execPath)
	}
	plist := plistContent(execPath, env)
	if err := os.MkdirAll(filepath.Dir(p.plistPath), 0o755); err != nil {
		return fmt.Errorf("creating LaunchDaemons directory: %w", err)
	}
	// launchd rejects a plist writable by group or other, so use 0644.
	if err := os.WriteFile(p.plistPath, plist, 0o644); err != nil {
		return fmt.Errorf("writing plist (%s): %w", p.plistPath, err)
	}
	// Unregister first so a re-run does not fail with "already bootstrapped".
	_, _ = p.run("launchctl", "bootout", serviceTarget)
	if out, err := p.run("launchctl", "bootstrap", "system", p.plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap (%s): %w%s", p.plistPath, err, indentOutput(out))
	}
	return nil
}

// UninstallService unregisters the LaunchDaemon and deletes the plist.
// A missing registration or plist is also success.
func (p darwinPlatform) UninstallService() error {
	if out, err := p.run("launchctl", "bootout", serviceTarget); err != nil {
		if !isAlreadyAbsent(out) {
			return fmt.Errorf("launchctl bootout (%s): %w%s", serviceTarget, err, indentOutput(out))
		}
	}
	if err := os.Remove(p.plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing plist (%s): %w", p.plistPath, err)
	}
	return nil
}

// OpenURL opens a URL in the default browser (`localapp open`).
// It is not part of the Platform interface, but lives here to keep every
// OS-dependent implementation in this package (DESIGN.md "Platform abstraction").
func OpenURL(url string) error {
	// Pass `--` so the URL is not interpreted as an option.
	if out, err := runCommand("open", "--", url); err != nil {
		return fmt.Errorf("running open(1): %w%s", err, indentOutput(out))
	}
	return nil
}

// resolverPath builds the path of the resolver file. It accepts only domains
// that pass format validation.
func (p darwinPlatform) resolverPath(domain string) (string, error) {
	if !validDomain.MatchString(domain) {
		return "", fmt.Errorf("invalid resolver domain: %q", domain)
	}
	return filepath.Join(p.resolverDir, domain), nil
}

// resolverContent returns the configuration body in resolver(5) format.
func resolverContent(dnsPort int) string {
	return "# Generated by localapp. Removed by localapp uninstall.\n" +
		"nameserver 127.0.0.1\n" +
		"port " + strconv.Itoa(dnsPort) + "\n"
}

// plistContent generates the LaunchDaemon plist. It keeps `<binary> daemon`
// running via KeepAlive. env is embedded as EnvironmentVariables (launchd does
// not inherit the caller's environment, so non-default settings are persisted
// here; DESIGN.md "Configuration").
func plistContent(execPath string, env map[string]string) []byte {
	prog := xmlString(execPath)
	label := xmlString(serviceLabel)
	envXML := envDict(env)
	var b bytes.Buffer
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
	</array>%s
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`, label, prog, envXML)
	return b.Bytes()
}

// envDict generates the EnvironmentVariables plist fragment. Keys are sorted
// to keep the output deterministic. Empty or nil input yields an empty string.
func envDict(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteString("\n\t<key>EnvironmentVariables</key>\n\t<dict>")
	for _, k := range keys {
		ek := xmlString(k)
		ev := xmlString(env[k])
		fmt.Fprintf(&b, "\n\t\t<key>%s</key>\n\t\t<string>%s</string>", ek, ev)
	}
	b.WriteString("\n\t</dict>")
	return b.String()
}

// xmlString escapes a value so it can be embedded in a plist <string>.
func xmlString(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s)) // bytes.Buffer.Write never fails.
	return b.String()
}

// readCertDER reads the PEM-encoded root CA certificate and returns its DER.
func readCertDER(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("cannot parse root CA certificate (%s)", path)
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return nil, fmt.Errorf("cannot parse root CA certificate (%s): %w", path, err)
	}
	return block.Bytes, nil
}

// isAlreadyAbsent reports whether the output means "the target does not exist".
// Such failures are treated as success to keep uninstall idempotent.
func isAlreadyAbsent(out string) bool {
	s := strings.ToLower(out)
	for _, marker := range []string{
		"could not find",
		"no such file",
		"not find specified service",
		"unable to find",
		"nothing found to delete",
		"no matching certificate",
		"the specified item could not be found",
		"input/output error", // launchctl bootout may return this when unregistered
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// runCommand runs a command and returns its combined output.
func runCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// indentOutput formats command output for inclusion in an error message.
func indentOutput(out string) string {
	if out == "" {
		return ""
	}
	return "\n  " + strings.ReplaceAll(out, "\n", "\n  ")
}
