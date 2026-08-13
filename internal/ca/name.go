package ca

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidHost reports that an SNI / host name is not a valid name under the
// configured domain. When GetCertificate returns this error the TLS handshake
// fails (DESIGN.md "Security": SNI validation).
var ErrInvalidHost = errors.New("invalid host name")

const (
	// maxHostLen is the limit for a whole DNS name (RFC 1035).
	maxHostLen = 253
	// maxLabelLen is the limit for a single label (RFC 1035).
	maxLabelLen = 63
)

// ValidateDomain verifies that the configured domain is a dot-joined sequence
// of `[a-z0-9-]` labels.
func ValidateDomain(domain string) error {
	if domain == "" {
		return fmt.Errorf("%w: empty domain", ErrInvalidHost)
	}
	if len(domain) > maxHostLen {
		return fmt.Errorf("%w: domain too long (%d characters)", ErrInvalidHost, len(domain))
	}
	for _, label := range strings.Split(domain, ".") {
		if err := validateLabel(label); err != nil {
			return fmt.Errorf("%w: domain %q: %s", ErrInvalidHost, domain, err)
		}
	}
	return nil
}

// validateHost verifies that the host name is either the configured domain
// itself (apex) or a sequence of `[a-z0-9-]` labels followed by `.<domain>`.
//
// Issuing a certificate, caching it, and building the cache file name all
// happen only for names that passed this validation. Since a label can never
// contain a dot, a slash or `..`, this validation doubles as path traversal
// protection. Uppercase letters, a trailing dot and the empty string are
// rejected.
func validateHost(host, domain string) error {
	if host == "" {
		return fmt.Errorf("%w: empty SNI", ErrInvalidHost)
	}
	if len(host) > maxHostLen {
		return fmt.Errorf("%w: host name too long (%d characters)", ErrInvalidHost, len(host))
	}
	if host == domain {
		return nil
	}
	suffix := "." + domain
	if !strings.HasSuffix(host, suffix) {
		return fmt.Errorf("%w: %q is not under .%s", ErrInvalidHost, host, domain)
	}
	prefix := strings.TrimSuffix(host, suffix)
	for _, label := range strings.Split(prefix, ".") {
		if err := validateLabel(label); err != nil {
			return fmt.Errorf("%w: %q: %s", ErrInvalidHost, host, err)
		}
	}
	return nil
}

// validateLabel verifies that a single label is non-empty, at most 63
// characters, and made up only of `[a-z0-9-]`.
func validateLabel(label string) error {
	if label == "" {
		return errors.New("empty label")
	}
	if len(label) > maxLabelLen {
		return fmt.Errorf("label too long (%d characters)", len(label))
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return fmt.Errorf("label %q contains the unusable character %q", label, r)
		}
	}
	return nil
}
