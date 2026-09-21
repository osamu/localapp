// Package domain validates the configured DNS suffix shared by the CA, DNS
// server, and operating-system resolver.
package domain

import (
	"fmt"
	"strings"
)

// Validate accepts lowercase DNS names without a trailing dot. Each label must
// contain 1–63 ASCII letters, digits or hyphens, with no leading or trailing
// hyphen. The whole name may contain at most 253 characters.
func Validate(name string) error {
	if len(name) == 0 || len(name) > 253 {
		return fmt.Errorf("invalid domain %q: length must be between 1 and 253 characters", name)
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 {
			return fmt.Errorf("invalid domain %q: label length must be between 1 and 63 characters", name)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid domain %q: labels must not start or end with a hyphen", name)
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return fmt.Errorf("invalid domain %q: labels must contain only [a-z0-9-]", name)
			}
		}
	}
	return nil
}
