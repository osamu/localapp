package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/osamu/localapp/internal/ca"
	"github.com/osamu/localapp/internal/config"
)

type cleanupPlatform struct {
	calls                             []string
	serviceErr, resolverErr, trustErr error
	domain, certPath                  string
}

func (p *cleanupPlatform) UninstallService() error {
	p.calls = append(p.calls, "service")
	return p.serviceErr
}
func (p *cleanupPlatform) UninstallResolver(domain string) error {
	p.calls = append(p.calls, "resolver")
	p.domain = domain
	return p.resolverErr
}
func (p *cleanupPlatform) UninstallTrust(certPath string) error {
	p.calls = append(p.calls, "trust")
	p.certPath = certPath
	return p.trustErr
}

func TestUninstallPreservesRetryState(t *testing.T) {
	serviceErr := errors.New("service failed")
	resolverErr := errors.New("resolver failed")
	trustErr := errors.New("trust failed")
	tests := []struct {
		name                     string
		service, resolver, trust error
		calls                    []string
	}{
		{"success", nil, nil, nil, []string{"service", "resolver", "trust"}},
		{"service failure", serviceErr, nil, nil, []string{"service"}},
		{"resolver failure", nil, resolverErr, nil, []string{"service", "resolver", "trust"}},
		{"trust failure", nil, nil, trustErr, []string{"service", "resolver", "trust"}},
		{"both cleanup failures", nil, resolverErr, trustErr, []string{"service", "resolver", "trust"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{StateDir: filepath.Join(t.TempDir(), "state"), Domain: "localapp"}
			if err := os.MkdirAll(cfg.CADir(), 0700); err != nil {
				t.Fatal(err)
			}
			if err := writeDomainRecord(cfg.StateDir, "dev.test"); err != nil {
				t.Fatal(err)
			}
			certPath := ca.CertPath(cfg.CADir())
			const certificate = "certificate needed for retry"
			if err := os.WriteFile(certPath, []byte(certificate), 0600); err != nil {
				t.Fatal(err)
			}
			p := &cleanupPlatform{serviceErr: tt.service, resolverErr: tt.resolver, trustErr: tt.trust}
			report := func(string, ...any) {}
			err := uninstall(cfg, p, report)
			if !reflect.DeepEqual(p.calls, tt.calls) {
				t.Fatalf("calls = %v, want %v", p.calls, tt.calls)
			}
			if tt.service == nil && (p.domain != "dev.test" || p.certPath != certPath) {
				t.Fatalf("cleanup used domain=%q cert=%q", p.domain, p.certPath)
			}
			failed := tt.service != nil || tt.resolver != nil || tt.trust != nil
			if !failed {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				for _, cause := range []error{tt.service, tt.resolver, tt.trust} {
					if cause != nil && !errors.Is(err, cause) {
						t.Errorf("error %v does not contain %v", err, cause)
					}
				}
				if got, readErr := os.ReadFile(certPath); readErr != nil || string(got) != certificate {
					t.Fatalf("retry certificate lost: %q, %v", got, readErr)
				}
				if domain, ok := readDomainRecord(cfg.StateDir); !ok || domain != "dev.test" {
					t.Fatalf("retry domain lost: %q, %v", domain, ok)
				}
				// A later invocation can use the preserved metadata and finish cleanup.
				p.serviceErr, p.resolverErr, p.trustErr = nil, nil, nil
				p.calls = nil
				if err := uninstall(cfg, p, report); err != nil {
					t.Fatal(err)
				}
				if p.domain != "dev.test" || p.certPath != certPath {
					t.Fatal("retry used wrong metadata")
				}
			}
			if _, err := os.Stat(cfg.StateDir); !os.IsNotExist(err) {
				t.Fatalf("state directory remains: %v", err)
			}
			if err := uninstall(cfg, p, report); err != nil {
				t.Fatalf("repeated cleanup: %v", err)
			}
		})
	}
}
