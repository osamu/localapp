package dashboard

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/osamu/localapp/internal/registry"
)

// fakeStore stands in for the registry. It can also return values that never
// went through validation.
type fakeStore struct{ apps []registry.App }

func (s fakeStore) Apps() []registry.App { return s.apps }

// listen listens on a free port and returns its port number (to reproduce
// status=up).
func listen(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing the test server URL: %v", err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("parsing the host of the test server URL: %v", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parsing the port number: %v", err)
	}
	return n
}

// deadPID returns the pid of a process that has exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running dummy process: %v", err)
	}
	return cmd.Process.Pid
}

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestServeHTTPListsServices(t *testing.T) {
	port := listen(t)
	store := fakeStore{apps: []registry.App{{
		Name: "app1",
		Services: []registry.Service{
			{Name: "web", Port: port},
			{Name: "api", Port: 65000, Path: "/api"},
		},
	}}}
	h := New(store, Options{Domain: "localapp", Version: "9.9.9"})

	rec := get(t, h, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"app1/web",
		"app1/api",
		"https://app1.localapp/",
		"https://api.app1.localapp/",
		"https://app1.localapp/api/",
		"9.9.9",
		"path /api",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the body does not contain %q", want)
		}
	}
	// A listening port is up, an unused one is down.
	if !strings.Contains(body, `class="status up">up<`) {
		t.Errorf("no service is shown as up:\n%s", body)
	}
	if !strings.Contains(body, `class="status down">down<`) {
		t.Errorf("no service is shown as down:\n%s", body)
	}
}

func TestServeHTTPEmpty(t *testing.T) {
	h := New(fakeStore{}, Options{Domain: "localapp"})
	rec := get(t, h, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No registrations yet") {
		t.Errorf("the empty-state message is not shown")
	}
}

// TestPIDDeadIsDown checks that a service is shown as down once its pid has
// exited, even while the port is listening.
func TestPIDDeadIsDown(t *testing.T) {
	port := listen(t)
	store := fakeStore{apps: []registry.App{{
		Name:     "app1",
		Services: []registry.Service{{Name: "web", Port: port, PID: deadPID(t)}},
	}}}
	h := New(store, Options{Domain: "localapp"})
	body := get(t, h, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, `class="status down">down<`) {
		t.Errorf("a service whose pid exited is not shown as down:\n%s", body)
	}
}

// TestEscapesRegisteredValues checks that XSS through registered values is
// prevented. App and service names are validated server-side, but the output
// side escapes them automatically as well (DESIGN.md "セキュリティ").
func TestEscapesRegisteredValues(t *testing.T) {
	store := fakeStore{apps: []registry.App{{
		Name: `<script>alert("x")</script>`,
		Services: []registry.Service{{
			Name: `"><img src=x onerror=alert(1)>`,
			Port: 65000,
			Path: `/<svg onload=alert(2)>`,
		}},
	}}}
	h := New(store, Options{Domain: `evil"><script>`})

	body := get(t, h, http.MethodGet, "/").Body.String()
	for _, bad := range []string{
		"<script>alert",
		"<img src=x",
		"<svg onload",
		`evil"><script>`,
	} {
		if strings.Contains(body, bad) {
			t.Errorf("unescaped output found: %q\n%s", bad, body)
		}
	}
	if !strings.Contains(body, "&lt;script&gt;alert") {
		t.Errorf("the escaped app name is missing:\n%s", body)
	}
	// URLs placed in href are escaped the same way.
	if strings.Contains(body, `href="https://`+"\"") {
		t.Errorf("the href is broken:\n%s", body)
	}
}

// TestEscapesHref checks that nothing can break out of the href attribute.
// URLs are always built starting with https://, so a crafted value stays
// escaped inside the attribute.
func TestEscapesHref(t *testing.T) {
	store := fakeStore{apps: []registry.App{{
		Name:     "app1",
		Services: []registry.Service{{Name: "web", Port: 65000}},
	}}}
	// Craft the domain to inject an attribute delimiter and a tag into urls.
	h := New(store, Options{Domain: `x/"><a href=javascript:alert(1)>`})
	body := get(t, h, http.MethodGet, "/").Body.String()
	for _, bad := range []string{
		`href="javascript:`,
		`"><a `,
	} {
		if strings.Contains(body, bad) {
			t.Errorf("output escaping out of the href found: %q\n%s", bad, body)
		}
	}
}

func TestReadOnly(t *testing.T) {
	h := New(fakeStore{}, Options{Domain: "localapp"})
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := get(t, h, m, "/")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", m, rec.Code)
		}
		if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
			t.Errorf("%s: Allow = %q", m, allow)
		}
	}
	// The page has no form element that could lead to a mutation.
	body := get(t, h, http.MethodGet, "/").Body.String()
	for _, bad := range []string{"<form", "<button", "<input", "<script"} {
		if strings.Contains(strings.ToLower(body), bad) {
			t.Errorf("the read-only page contains %q", bad)
		}
	}
}

func TestNotFoundPath(t *testing.T) {
	h := New(fakeStore{}, Options{Domain: "localapp"})
	rec := get(t, h, http.MethodGet, "/other")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
