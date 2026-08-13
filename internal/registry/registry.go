// Package registry provides the app / service registry and its persistence.
//
// The data model has two levels, App > Service (DESIGN.md "Control Plane API").
// status / urls are not stored; they are derived when read.
package registry

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultService is the default service name (DESIGN.md "CLI" conventions).
const DefaultService = "web"

// Liveness states of a service.
const (
	StatusUp   = "up"
	StatusDown = "down"
)

// DefaultProbeTimeout is the default TCP connect timeout for liveness probes.
const DefaultProbeTimeout = 200 * time.Millisecond

// nameRe is the allowed pattern for app / service names
// (DESIGN.md "Control Plane API").
var nameRe = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)

// Service is a single forwarding target.
type Service struct {
	Name      string `json:"name"`
	Port      int    `json:"port"`
	Path      string `json:"path,omitempty"`
	StripPath bool   `json:"strip_path,omitempty"`
	PID       int    `json:"pid,omitempty"`
}

// App is the set of services belonging to the same namespace.
type App struct {
	Name     string    `json:"name"`
	Services []Service `json:"services"`
}

// Service returns the service with the given name.
func (a App) Service(name string) (Service, bool) {
	for _, s := range a.Services {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
}

// ValidationError is a validation failure. Code maps directly onto the Control
// Plane API error codes (DESIGN.md "Control Plane API").
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }

// Validation error codes.
const (
	CodeInvalidName = "invalid_name"
	CodeInvalidPort = "invalid_port"
	CodeInvalidPath = "invalid_path"
	CodeInvalidPID  = "invalid_pid"
)

// ValidateName validates an app / service name. kind is "app" or "service".
func ValidateName(kind, name string) error {
	if !nameRe.MatchString(name) {
		return &ValidationError{
			Code:    CodeInvalidName,
			Message: kind + " name must match [a-z0-9-]{1,63}",
		}
	}
	return nil
}

// ValidatePort validates a port number.
func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return &ValidationError{Code: CodeInvalidPort, Message: "port must be 1-65535"}
	}
	return nil
}

// ValidatePath validates a path mount. An empty string (no path mount) is
// allowed.
func ValidatePath(p string) error {
	if p == "" {
		return nil
	}
	if !strings.HasPrefix(p, "/") {
		return &ValidationError{Code: CodeInvalidPath, Message: `path must start with "/"`}
	}
	if strings.Contains(p, "?") || strings.Contains(p, "#") || strings.ContainsAny(p, " \t\r\n") {
		return &ValidationError{Code: CodeInvalidPath, Message: "path must not contain query, fragment or whitespace"}
	}
	return nil
}

// ValidatePID validates the watched PID. 0 means "not specified".
func ValidatePID(pid int) error {
	if pid < 0 {
		return &ValidationError{Code: CodeInvalidPID, Message: "pid must not be negative"}
	}
	return nil
}

// Validate validates the whole Service.
func (s Service) Validate() error {
	if err := ValidateName("service", s.Name); err != nil {
		return err
	}
	if err := ValidatePort(s.Port); err != nil {
		return err
	}
	if err := ValidatePath(s.Path); err != nil {
		return err
	}
	return ValidatePID(s.PID)
}

// NormalizeName deterministically normalizes an arbitrary string into a usable
// app name. It lowercases the input, collapses runs of characters outside
// [a-z0-9-] into a single "-", trims leading and trailing "-", and truncates to
// 63 characters (for example the basename "my_app" of "~/code/my_app" becomes
// "my-app"). It returns an empty string when normalization yields nothing; the
// caller must treat that as an error.
func NormalizeName(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	return out
}

// NormalizePath normalizes a path mount by dropping the trailing "/" ("/"
// itself is equivalent to having no path mount, so it becomes empty).
func NormalizePath(p string) string {
	if p == "" {
		return ""
	}
	p = strings.TrimRight(p, "/")
	return p
}

// URLs returns the URLs derived from a registration
// (DESIGN.md "Routing model"). They are ordered so the first element is
// the primary URL to present to the user.
//
//   - https://<app>.<domain>/            — default service only (host resolution priority 3)
//   - https://<service>.<app>.<domain>/  — always produced by a service registration
//   - https://<app>.<domain><path>/      — only when a path is given
func (s Service) URLs(app, domain string) []string {
	urls := make([]string, 0, 3)
	add := func(u string) {
		for _, x := range urls {
			if x == u {
				return
			}
		}
		urls = append(urls, u)
	}
	if s.Name == DefaultService {
		add(fmt.Sprintf("https://%s.%s/", app, domain))
	}
	add(fmt.Sprintf("https://%s.%s.%s/", s.Name, app, domain))
	if p := NormalizePath(s.Path); p != "" {
		add(fmt.Sprintf("https://%s.%s%s/", app, domain, p))
	}
	return urls
}

// Status determines liveness from whether localhost:port accepts a TCP
// connection.
func Status(port int, timeout time.Duration) string {
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), timeout)
	if err != nil {
		return StatusDown
	}
	_ = conn.Close()
	return StatusUp
}
