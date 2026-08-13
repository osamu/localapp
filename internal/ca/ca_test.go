package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testDomain = "localapp"

// newTestCA creates a root CA in a temporary directory and returns it.
func newTestCA(t *testing.T) (*CA, string, string) {
	t.Helper()
	root := t.TempDir()
	caDir := filepath.Join(root, "ca")
	certsDir := filepath.Join(root, "certs")
	c, err := LoadOrCreate(caDir, certsDir, testDomain)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	return c, caDir, certsDir
}

func TestLoadOrCreateGeneratesRoot(t *testing.T) {
	c, caDir, certsDir := newTestCA(t)

	for _, p := range []string{c.CertPath(), c.KeyPath()} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s does not exist: %v", p, err)
		}
	}
	if got, want := c.CertPath(), filepath.Join(caDir, "root.crt"); got != want {
		t.Errorf("CertPath = %s, want %s", got, want)
	}
	if _, err := os.Stat(certsDir); err != nil {
		t.Errorf("certificate directory was not created: %v", err)
	}

	// The private key is 0600.
	fi, err := os.Stat(c.KeyPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("root.key permissions = %o, want 600", perm)
	}

	root := c.cert
	if !root.IsCA {
		t.Error("IsCA = false, want true")
	}
	if !root.BasicConstraintsValid {
		t.Error("BasicConstraintsValid = false, want true")
	}
	if root.MaxPathLen != 0 || !root.MaxPathLenZero {
		t.Errorf("MaxPathLen = %d (zero=%v), want 0 (zero=true)", root.MaxPathLen, root.MaxPathLenZero)
	}
	if root.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("KeyUsageCertSign is missing")
	}
	pub, ok := root.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Errorf("public key = %T (%v), want ECDSA P-256", root.PublicKey, pub)
	}
	// A 10-year lifetime (within one day either way).
	years10 := root.NotBefore.Add(caValidity + backdate)
	if d := root.NotAfter.Sub(years10); d > 24*time.Hour || d < -24*time.Hour {
		t.Errorf("lifetime = %v, want about 10 years", root.NotAfter.Sub(root.NotBefore))
	}
	if root.Subject.CommonName == "" {
		t.Error("CommonName is empty")
	}
}

// TestRootNameConstraints checks that the Name Constraints are present and
// critical, in the form (no leading dot) that matches both the apex and
// subdomains.
func TestRootNameConstraints(t *testing.T) {
	c, _, _ := newTestCA(t)
	root := c.cert

	if got, want := root.PermittedDNSDomains, []string{testDomain}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("PermittedDNSDomains = %v, want %v", got, want)
	}
	if !root.PermittedDNSDomainsCritical {
		t.Error("PermittedDNSDomainsCritical = false, want true")
	}
	if len(root.ExcludedDNSDomains) != 0 {
		t.Errorf("ExcludedDNSDomains = %v, want empty", root.ExcludedDNSDomains)
	}
	if strings.HasPrefix(root.PermittedDNSDomains[0], ".") {
		t.Error("the leading-dot form does not match the apex and must not be used")
	}

	// The extension itself must be present and critical
	// (OID 2.5.29.30 = nameConstraints).
	found := false
	for _, ext := range root.Extensions {
		if ext.Id.String() == "2.5.29.30" {
			found = true
			if !ext.Critical {
				t.Error("the nameConstraints extension is not critical")
			}
		}
	}
	if !found {
		t.Error("the nameConstraints extension is missing")
	}
}

// TestLoadOrCreateReloadsExisting checks that a second call does not recreate
// an existing CA.
func TestLoadOrCreateReloadsExisting(t *testing.T) {
	c1, caDir, certsDir := newTestCA(t)
	before, err := os.ReadFile(c1.CertPath())
	if err != nil {
		t.Fatal(err)
	}

	c2, err := LoadOrCreate(caDir, certsDir, testDomain)
	if err != nil {
		t.Fatalf("LoadOrCreate (second call): %v", err)
	}
	if c1.cert.SerialNumber.Cmp(c2.cert.SerialNumber) != 0 {
		t.Error("the serial number changed, so the root CA was recreated")
	}
	after, err := os.ReadFile(c1.CertPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("root.crt was rewritten")
	}

	// A leaf issued with the reloaded CA key verifies against the original CA
	// certificate.
	leaf, err := c2.CertificateFor("app1." + testDomain)
	if err != nil {
		t.Fatalf("CertificateFor: %v", err)
	}
	if err := leaf.Leaf.CheckSignatureFrom(c1.cert); err != nil {
		t.Errorf("the reloaded key does not sign as the original CA: %v", err)
	}
}

// TestLoadOrCreateRejectsDomainMismatch checks that an existing CA for another
// domain is not replaced silently.
func TestLoadOrCreateRejectsDomainMismatch(t *testing.T) {
	_, caDir, certsDir := newTestCA(t)
	if _, err := LoadOrCreate(caDir, certsDir, "other"); err == nil {
		t.Fatal("a domain mismatch was accepted, want error")
	}
}

func TestLoadOrCreateRejectsInvalidDomain(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"", "Localapp", ".localapp", "loc/app"} {
		if _, err := LoadOrCreate(filepath.Join(root, "ca"), filepath.Join(root, "certs"), d); err == nil {
			t.Errorf("LoadOrCreate(domain=%q) = nil, want error", d)
		}
	}
}

// TestLeafVerifiesAgainstCA checks that a leaf passes crypto/x509 chain
// verification against the CA, Name Constraints included.
func TestLeafVerifiesAgainstCA(t *testing.T) {
	c, _, _ := newTestCA(t)
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)

	for _, host := range []string{testDomain, "app1." + testDomain, "api.app1." + testDomain} {
		cert, err := c.CertificateFor(host)
		if err != nil {
			t.Fatalf("CertificateFor(%q): %v", host, err)
		}
		leaf := cert.Leaf
		if _, err := leaf.Verify(x509.VerifyOptions{
			DNSName:   host,
			Roots:     pool,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			t.Errorf("Verify(%q): %v", host, err)
		}
		if got := leaf.DNSNames; len(got) != 1 || got[0] != host {
			t.Errorf("DNSNames = %v, want [%s]", got, host)
		}
		if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
			t.Errorf("ExtKeyUsage = %v, want [ServerAuth]", leaf.ExtKeyUsage)
		}
		if leaf.IsCA {
			t.Error("the leaf has IsCA = true")
		}
		pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
		if !ok || pub.Curve != elliptic.P256() {
			t.Errorf("public key = %T, want ECDSA P-256", leaf.PublicKey)
		}
		// A 90-day lifetime (within one hour either way).
		want := leaf.NotBefore.Add(leafValidity + backdate)
		if d := leaf.NotAfter.Sub(want); d > time.Hour || d < -time.Hour {
			t.Errorf("lifetime = %v, want 90 days", leaf.NotAfter.Sub(leaf.NotBefore))
		}
		// The CA certificate is sent as part of the chain.
		if len(cert.Certificate) != 2 {
			t.Errorf("chain length = %d, want 2 (leaf + CA)", len(cert.Certificate))
		}
	}
}

// TestNameConstraintsRejectOutOfDomainLeaf checks that even when the in-code
// issuing restriction is bypassed, the Name Constraints make verification fail
// (the lower half of the defense in depth).
func TestNameConstraintsRejectOutOfDomainLeaf(t *testing.T) {
	c, _, _ := newTestCA(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := newSerial()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "evil.com"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(leafValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"evil.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatalf("generating the test leaf: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(c.cert)
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "evil.com", Roots: pool}); err == nil {
		t.Fatal("verification of an out-of-domain leaf succeeded, want error")
	}
}

// TestCertificateForRejectsOutOfDomain checks that issuance outside the
// configured domain is rejected in code (the upper half of the defense in
// depth) and that no cache file is created.
func TestCertificateForRejectsOutOfDomain(t *testing.T) {
	c, _, certsDir := newTestCA(t)

	hosts := []string{
		"evil.com",
		"localapp.evil.com",
		"app1.notlocalapp",
		"App1.localapp",
		"",
		"../evil",
		"../../etc/passwd",
		"../evil.localapp",
		"foo/bar.localapp",
		"a/../b.localapp",
		"app1.localapp/../root",
		"..localapp",
		"app1..localapp",
	}
	for _, h := range hosts {
		cert, err := c.CertificateFor(h)
		if err == nil {
			t.Errorf("CertificateFor(%q) = %v, want error", h, cert.Leaf.DNSNames)
			continue
		}
		if !errors.Is(err, ErrInvalidHost) {
			t.Errorf("CertificateFor(%q): errors.Is(ErrInvalidHost) = false (%v)", h, err)
		}
	}

	// Nothing was written into the certs directory (checking that path
	// traversal did no real damage).
	entries, err := os.ReadDir(certsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("files were created in the certs directory: %v", entries)
	}
	// Nothing leaked into the parent directory either.
	parent := filepath.Dir(certsDir)
	got, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.Name() != "ca" && e.Name() != "certs" {
			t.Errorf("unexpected entry in the state directory: %s", e.Name())
		}
	}
}

// TestGetCertificateEmptySNI checks that a connection without SNI is rejected.
func TestGetCertificateEmptySNI(t *testing.T) {
	c, _, _ := newTestCA(t)
	if _, err := c.GetCertificate(&tls.ClientHelloInfo{ServerName: ""}); err == nil {
		t.Error("an empty SNI was accepted, want error")
	}
}

// TestCacheHit checks that the memory cache is used when the same host is
// requested again.
func TestCacheHit(t *testing.T) {
	c, _, certsDir := newTestCA(t)
	host := "app1." + testDomain

	first, err := c.CertificateFor(host)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("the cache did not return the same instance")
	}
	if first.Leaf.SerialNumber.Cmp(second.Leaf.SerialNumber) != 0 {
		t.Error("the serial numbers differ, so the certificate was reissued")
	}

	// The disk cache (<certs>/<host>.crt|.key) exists and the key is 0600.
	crt := filepath.Join(certsDir, host+".crt")
	key := filepath.Join(certsDir, host+".key")
	if _, err := os.Stat(crt); err != nil {
		t.Errorf("%s is missing: %v", crt, err)
	}
	fi, err := os.Stat(key)
	if err != nil {
		t.Fatalf("%s is missing: %v", key, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s permissions = %o, want 600", key, perm)
	}
}

// TestDiskCacheReused checks that another instance reuses the disk cache.
func TestDiskCacheReused(t *testing.T) {
	c1, caDir, certsDir := newTestCA(t)
	host := "app1." + testDomain
	first, err := c1.CertificateFor(host)
	if err != nil {
		t.Fatal(err)
	}

	c2, err := LoadOrCreate(caDir, certsDir, testDomain)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c2.CertificateFor(host)
	if err != nil {
		t.Fatal(err)
	}
	if first.Leaf.SerialNumber.Cmp(second.Leaf.SerialNumber) != 0 {
		t.Error("the disk cache was not reused and the certificate was reissued")
	}
	if len(second.Certificate) != 2 {
		t.Errorf("chain length = %d, want 2 (leaf + CA)", len(second.Certificate))
	}
}

// TestRenewNearExpiry checks that a leaf with less than 30 days left is
// reissued.
func TestRenewNearExpiry(t *testing.T) {
	c, _, _ := newTestCA(t)
	host := "app1." + testDomain
	now := time.Now()

	// Put a leaf with 10 days left into both the memory and disk caches.
	old, err := c.issueLeaf(host, now, 10*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c.cache[host] = old

	fresh, err := c.CertificateFor(host)
	if err != nil {
		t.Fatal(err)
	}
	if old.Leaf.SerialNumber.Cmp(fresh.Leaf.SerialNumber) == 0 {
		t.Fatal("a leaf close to expiry was not reissued")
	}
	if remaining := fresh.Leaf.NotAfter.Sub(now); remaining < 89*24*time.Hour {
		t.Errorf("remaining lifetime after reissue = %v, want about 90 days", remaining)
	}

	// With 31 days left it is used as is (the other side of the threshold).
	keep, err := c.issueLeaf(host, now, 31*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c.cache[host] = keep
	got, err := c.CertificateFor(host)
	if err != nil {
		t.Fatal(err)
	}
	if got.Leaf.SerialNumber.Cmp(keep.Leaf.SerialNumber) != 0 {
		t.Error("a leaf with 31 days left was reissued")
	}
}

// TestStaleCacheFromOtherCA checks that a stale cache signed by another CA is
// not reused.
func TestStaleCacheFromOtherCA(t *testing.T) {
	c1, _, certsDir1 := newTestCA(t)
	host := "app1." + testDomain
	if _, err := c1.CertificateFor(host); err != nil {
		t.Fatal(err)
	}

	// Start another root CA combined with the existing certs directory.
	otherCADir := filepath.Join(t.TempDir(), "ca")
	c2, err := LoadOrCreate(otherCADir, certsDir1, testDomain)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := c2.CertificateFor(host)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.Leaf.CheckSignatureFrom(c2.cert); err != nil {
		t.Errorf("the certificate was not reissued by the new CA: %v", err)
	}
}

// TestHandshake checks that a real TLS handshake accepts or rejects the
// certificate as expected.
func TestHandshake(t *testing.T) {
	c, _, _ := newTestCA(t)
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)

	handshake := func(sni string) error {
		serverConn, clientConn := net.Pipe()
		defer serverConn.Close()
		defer clientConn.Close()
		deadline := time.Now().Add(10 * time.Second)
		_ = serverConn.SetDeadline(deadline)
		_ = clientConn.SetDeadline(deadline)

		// net.Pipe is synchronous, so writing close_notify from both sides
		// deadlocks. The TLS layer is not closed; the deferred closes take care
		// of the raw connections.
		server := tls.Server(serverConn, c.TLSConfig())
		done := make(chan error, 1)
		go func() { done <- server.Handshake() }()

		client := tls.Client(clientConn, &tls.Config{RootCAs: pool, ServerName: sni})
		clientErr := client.Handshake()
		<-done
		return clientErr
	}

	if err := handshake("app1." + testDomain); err != nil {
		t.Errorf("the handshake with a valid SNI failed: %v", err)
	}
	if err := handshake(testDomain); err != nil {
		t.Errorf("the handshake for the apex failed: %v", err)
	}
	if err := handshake("evil.com"); err == nil {
		t.Error("the handshake with an out-of-domain SNI succeeded, want error")
	}
}

// TestConcurrentIssue checks that concurrent access does not corrupt the cache
// (for -race).
func TestConcurrentIssue(t *testing.T) {
	c, _, _ := newTestCA(t)
	hosts := []string{"a." + testDomain, "b." + testDomain, testDomain}
	errs := make(chan error, 30)
	for i := 0; i < 30; i++ {
		host := hosts[i%len(hosts)]
		go func() {
			_, err := c.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
			errs <- err
		}()
	}
	for i := 0; i < 30; i++ {
		if err := <-errs; err != nil {
			t.Errorf("error during concurrent issuance: %v", err)
		}
	}
}
