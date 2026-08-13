// Package ca creates and loads the local root CA and issues leaf certificates
// on demand per SNI (DESIGN.md "TLS / CA").
//
// The root CA carries critical X.509 Name Constraints so that, even if its
// private key leaks, certificates outside the configured domain do not pass
// browser validation. Issuance additionally validates the domain in code, for
// defense in depth.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Certificate lifetimes (DESIGN.md "TLS / CA").
const (
	// caValidity is the lifetime of the root CA.
	caValidity = 10 * 365 * 24 * time.Hour
	// leafValidity is the lifetime of a leaf certificate. It leaves ample
	// margin against Apple's 825-day limit for certificates from a local root.
	leafValidity = 90 * 24 * time.Hour
	// renewBefore is the threshold below which a leaf's remaining lifetime
	// triggers reissuance.
	renewBefore = 30 * 24 * time.Hour
	// backdate is how far NotBefore is moved into the past to absorb clock skew.
	backdate = 1 * time.Hour
)

// File names (DESIGN.md "状態ディレクトリのレイアウト").
const (
	rootCertFile = "root.crt"
	rootKeyFile  = "root.key"
)

// CA holds the root CA and the cache of leaf certificates issued under it.
// The zero value is unusable; create one with LoadOrCreate.
type CA struct {
	domain   string
	caDir    string
	certsDir string

	cert *x509.Certificate
	key  *ecdsa.PrivateKey

	// now returns the current time; tests replace it.
	now func() time.Time

	mu    sync.Mutex
	cache map[string]*tls.Certificate
}

// LoadOrCreate loads the root CA from caDir, creating and saving one when it
// does not exist. Leaf certificates are cached in certsDir. domain is the
// configured domain (for example "localapp").
//
// It fails when an existing root CA does not cover the configured domain, so
// that a root CA already added to the trust store is never replaced silently.
func LoadOrCreate(caDir, certsDir, domain string) (*CA, error) {
	if err := ValidateDomain(domain); err != nil {
		return nil, err
	}
	if caDir == "" || certsDir == "" {
		return nil, errors.New("ca: no directory given")
	}
	// caDir is 0755: root.crt must be readable by unprivileged users
	// (`localapp ca path` → NODE_EXTRA_CA_CERTS / SSL_CERT_FILE).
	// The private key root.key is protected by its own 0600 file mode.
	if err := os.MkdirAll(caDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating CA directory (%s): %w", caDir, err)
	}
	if err := os.MkdirAll(certsDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating certificate directory (%s): %w", certsDir, err)
	}

	c := &CA{
		domain:   domain,
		caDir:    caDir,
		certsDir: certsDir,
		now:      time.Now,
		cache:    map[string]*tls.Certificate{},
	}

	cert, key, err := loadRoot(c.CertPath(), c.KeyPath())
	switch {
	case err == nil:
		c.cert, c.key = cert, key
		if err := c.checkRootCoversDomain(); err != nil {
			return nil, err
		}
	case errors.Is(err, os.ErrNotExist):
		if err := c.createRoot(); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return c, nil
}

// CertPath returns the path of the root CA certificate under caDir. It is a
// package-level function for callers that need only the path, without loading
// or creating the CA (`localapp ca path`).
func CertPath(caDir string) string { return filepath.Join(caDir, rootCertFile) }

// CertPath returns the path of the root CA certificate (for `localapp ca path`
// and for trust-store registration).
func (c *CA) CertPath() string { return CertPath(c.caDir) }

// KeyPath returns the path of the root CA private key.
func (c *CA) KeyPath() string { return filepath.Join(c.caDir, rootKeyFile) }

// TLSConfig returns a TLS configuration that issues certificates on demand per
// SNI.
func (c *CA) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: c.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}
}

// ErrDomainMismatch reports that the existing root CA does not cover the
// configured domain. Callers detect it and tell the user to run
// uninstall then install.
var ErrDomainMismatch = errors.New("the existing root CA does not cover the configured domain")

// checkRootCoversDomain verifies that the loaded root CA permits the configured
// domain.
func (c *CA) checkRootCoversDomain() error {
	for _, d := range c.cert.PermittedDNSDomains {
		if d == c.domain {
			return nil
		}
	}
	return fmt.Errorf("%w: %s is not for domain %q (permitted: %v)",
		ErrDomainMismatch, c.CertPath(), c.domain, c.cert.PermittedDNSDomains)
}

// createRoot creates a new root CA and saves the certificate and private key.
func (c *CA) createRoot() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating CA key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return err
	}
	now := c.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("localapp local CA (%s)", c.domain),
			Organization: []string{"localapp"},
		},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		// dNSName constraints per RFC 5280. Without a leading dot the form
		// matches both the apex (localapp) and subdomains (app1.localapp).
		// ExcludedDNSDomains is not used.
		PermittedDNSDomains:         []string{c.domain},
		PermittedDNSDomainsCritical: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("generating CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("parsing CA certificate: %w", err)
	}

	keyPEM, err := encodeKey(key)
	if err != nil {
		return err
	}
	// Write the private key first to avoid a half-written state with only the
	// certificate present.
	if err := writeFile(c.KeyPath(), keyPEM, 0o600); err != nil {
		return fmt.Errorf("saving CA private key: %w", err)
	}
	if err := writeFile(c.CertPath(), encodeCert(der), 0o644); err != nil {
		return fmt.Errorf("saving CA certificate: %w", err)
	}

	c.cert, c.key = cert, key
	return nil
}

// loadRoot reads the certificate and private key. It returns os.ErrNotExist
// when either of them is missing.
func loadRoot(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("reading CA certificate (%s): %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("reading CA private key (%s): %w", keyPath, err)
	}

	cert, err := decodeCert(certPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CA certificate (%s): %w", certPath, err)
	}
	key, err := decodeKey(keyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CA private key (%s): %w", keyPath, err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, nil, fmt.Errorf("CA certificate (%s) and private key (%s) do not match", certPath, keyPath)
	}
	if !cert.IsCA {
		return nil, nil, fmt.Errorf("CA certificate (%s) is not a CA", certPath)
	}
	return cert, key, nil
}

// newSerial returns a random 128-bit serial number.
func newSerial() (*big.Int, error) {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}
	return n.Add(n, big.NewInt(1)), nil
}

func encodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func decodeCert(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no CERTIFICATE block")
	}
	return x509.ParseCertificate(block.Bytes)
}

func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encoding private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func decodeKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("unknown PEM type %q", block.Type)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an ECDSA key (%T)", k)
	}
	return ec, nil
}

// writeFile writes a file atomically (temp file + rename).
func writeFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeded

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("setting temporary file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming to %s: %w", path, err)
	}
	return nil
}
