package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// GetCertificate is the callback for tls.Config.GetCertificate.
// It issues a leaf certificate on demand per SNI. When the SNI is not a valid
// name under the configured domain it returns an error, failing the handshake.
func (c *CA) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	return c.CertificateFor(hello.ServerName)
}

// CertificateFor returns the leaf certificate for host. It consults the caches
// (memory, then disk) and issues a new certificate when there is none or when
// less than 30 days of validity remain.
func (c *CA) CertificateFor(host string) (*tls.Certificate, error) {
	if err := validateHost(host, c.domain); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if cert, ok := c.cache[host]; ok && c.usable(cert.Leaf, host, now) {
		return cert, nil
	}
	if cert, err := c.loadLeaf(host); err == nil && c.usable(cert.Leaf, host, now) {
		c.cache[host] = cert
		return cert, nil
	}

	cert, err := c.issueLeaf(host, now, leafValidity)
	if err != nil {
		return nil, err
	}
	c.cache[host] = cert
	return cert, nil
}

// usable reports whether an existing leaf certificate can be used as is.
// It must have been signed by the current root CA, carry host in its SAN, and
// have more than renewBefore of validity left.
func (c *CA) usable(leaf *x509.Certificate, host string, now time.Time) bool {
	if leaf == nil {
		return false
	}
	if now.Before(leaf.NotBefore) || now.Add(renewBefore).After(leaf.NotAfter) {
		return false
	}
	if leaf.VerifyHostname(host) != nil {
		return false
	}
	return leaf.CheckSignatureFrom(c.cert) == nil
}

// issueLeaf issues the leaf certificate for host and writes it to the disk
// cache.
func (c *CA) issueLeaf(host string, now time.Time, validity time.Duration) (*tls.Certificate, error) {
	// The caller already validated this, but issuance is the last line of
	// defense, so check again (an issuing restriction in code, separate from
	// the Name Constraints).
	if err := validateHost(host, c.domain); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating leaf key (%s): %w", host, err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName(host)},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("generating leaf certificate (%s): %w", host, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parsing leaf certificate (%s): %w", host, err)
	}

	keyPEM, err := encodeKey(key)
	if err != nil {
		return nil, err
	}
	certPath, keyPath, err := c.leafPaths(host)
	if err != nil {
		return nil, err
	}
	if err := writeFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("saving leaf key (%s): %w", host, err)
	}
	if err := writeFile(certPath, encodeCert(der), 0o644); err != nil {
		return nil, fmt.Errorf("saving leaf certificate (%s): %w", host, err)
	}

	return &tls.Certificate{
		Certificate: [][]byte{der, c.cert.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// loadLeaf loads a leaf certificate from the disk cache.
func (c *CA) loadLeaf(host string) (*tls.Certificate, error) {
	certPath, keyPath, err := c.leafPaths(host)
	if err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing leaf certificate (%s): %w", certPath, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parsing leaf certificate (%s): %w", certPath, err)
	}
	pair.Leaf = leaf
	// Include the CA certificate in the chain (the cache file holds the leaf
	// only).
	pair.Certificate = [][]byte{pair.Certificate[0], c.cert.Raw}
	return &pair, nil
}

// leafPaths returns the cache file paths. The SNI is validated right before the
// paths are built.
func (c *CA) leafPaths(host string) (certPath, keyPath string, err error) {
	if err := validateHost(host, c.domain); err != nil {
		return "", "", err
	}
	return filepath.Join(c.certsDir, host+".crt"), filepath.Join(c.certsDir, host+".key"), nil
}

// commonName returns a value that fits in the CN (64 characters max). When the
// host is longer the CN is left empty and the name is carried by the SAN alone
// (modern clients do not look at the CN).
func commonName(host string) string {
	if len(host) > 64 {
		return ""
	}
	return host
}
