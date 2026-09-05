package domain_test

import (
	"strings"
	"testing"

	"github.com/osamu/localapp/internal/ca"
	"github.com/osamu/localapp/internal/dnsd"
	"github.com/osamu/localapp/internal/domain"
)

// Check the public entry points as well as the shared validator so a consumer
// cannot silently drift from the configured-domain contract.
func TestDomainConsumers(t *testing.T) {
	maxName := strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 61)
	cases := []struct {
		name  string
		valid bool
	}{
		{"localapp", true}, {"example.test", true}, {"dev-local", true},
		{"a", true}, {"x1", true}, {strings.Repeat("a", 63), true}, {maxName, true},
		{"", false}, {".", false}, {".test", false}, {"test.", false},
		{"a..test", false}, {"-dev", false}, {"dev-", false},
		{"a.-dev", false}, {"dev-.test", false}, {"Test", false},
		{"local app", false}, {"../etc/passwd", false}, {"a/b", false},
		{"a\\b", false}, {"a_1", false}, {"アプリ", false}, {"a\x00", false},
		{strings.Repeat("a", 64), false}, {maxName + "a", false},
	}
	consumers := map[string]func(string) error{
		"domain": domain.Validate,
		"CA":     ca.ValidateDomain,
		"DNS":    func(name string) error { _, err := dnsd.New(name); return err },
	}
	for consumer, validate := range consumers {
		t.Run(consumer, func(t *testing.T) {
			for _, tc := range cases {
				if err := validate(tc.name); (err == nil) != tc.valid {
					t.Errorf("validate(%q) = %v, want valid=%v", tc.name, err, tc.valid)
				}
			}
		})
	}
}
