package control

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/osamu/localapp/internal/registry"
)

func newTestServer(t *testing.T) (*Server, *registry.Store) {
	t.Helper()
	store, err := registry.Open(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, Options{
		Domain:    "localapp",
		Version:   "0.1.0",
		Listeners: map[string]string{"dns": "127.0.0.1:15353", "http": "127.0.0.1:80", "https": "127.0.0.1:443"},
	})
	return srv, store
}

// do sends one request to the handler and returns the response.
func do(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "http://localapp"+path, nil)
	} else {
		r = httptest.NewRequest(method, "http://localapp"+path, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func decodeError(t *testing.T, w *httptest.ResponseRecorder) ErrorDetail {
	t.Helper()
	var eb ErrorBody
	if err := json.Unmarshal(w.Body.Bytes(), &eb); err != nil {
		t.Fatalf("parsing the error body: %v\n%s", err, w.Body.String())
	}
	if eb.Error.Code == "" {
		t.Fatalf("code is empty: %s", w.Body.String())
	}
	if eb.Error.Message == "" {
		t.Fatalf("message is empty: %s", w.Body.String())
	}
	return eb.Error
}

// closedPort returns a port nobody listens on. To keep liveness checks
// independent of the environment, it reserves a real port and releases it.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func assertStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("HTTP %d, want %d: %s", w.Code, want, w.Body.String())
	}
}

func TestGetStatus(t *testing.T) {
	srv, store := newTestServer(t)
	if _, err := store.Put("app1", registry.Service{Name: "web", Port: 5173}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("app1", registry.Service{Name: "api", Port: 8000}); err != nil {
		t.Fatal(err)
	}

	w := do(t, srv, http.MethodGet, "/v1/status", "")
	assertStatus(t, w, http.StatusOK)
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var st StatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Version != "0.1.0" || st.Domain != "localapp" {
		t.Errorf("status = %+v", st)
	}
	if st.Apps != 1 || st.Services != 2 {
		t.Errorf("apps=%d services=%d, want 1, 2", st.Apps, st.Services)
	}
	if st.Listeners["dns"] != "127.0.0.1:15353" {
		t.Errorf("listeners = %+v", st.Listeners)
	}
	if st.UptimeSec < 0 {
		t.Errorf("uptime_sec = %d", st.UptimeSec)
	}
}

// The PUT response has the same field layout as the example in
// DESIGN.md "Control Plane API" (the example's port 8000 / status "up" depend
// on the environment, so a closed port is used here).
func TestPutServiceMatchesDesignExample(t *testing.T) {
	srv, _ := newTestServer(t)
	port := closedPort(t)
	w := do(t, srv, http.MethodPut, "/v1/apps/app1/services/api",
		`{"port": `+strconv.Itoa(port)+`, "path": "/api", "strip_path": false}`)
	assertStatus(t, w, http.StatusOK)

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"app":        "app1",
		"service":    "api",
		"port":       float64(port),
		"path":       "/api",
		"strip_path": false,
		"status":     "down",
		"urls":       []any{"https://api.app1.localapp/", "https://app1.localapp/api/"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PUT response =\n%v\nwant\n%v", got, want)
	}
}

func TestPutServiceIsIdempotent(t *testing.T) {
	srv, _ := newTestServer(t)
	for range 3 {
		w := do(t, srv, http.MethodPut, "/v1/apps/app1/services/web", `{"port": 5173}`)
		assertStatus(t, w, http.StatusOK)
	}
	w := do(t, srv, http.MethodGet, "/v1/apps", "")
	var resp AppsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Apps) != 1 || len(resp.Apps[0].Services) != 1 {
		t.Fatalf("not idempotent: %+v", resp.Apps)
	}
}

// status / urls in the request body are ignored.
func TestPutIgnoresDerivedFields(t *testing.T) {
	srv, _ := newTestServer(t)
	body := `{"port": ` + strconv.Itoa(closedPort(t)) + `, "status": "up", "urls": ["https://evil.example/"], "app": "other", "service": "other"}`
	w := do(t, srv, http.MethodPut, "/v1/apps/app1/services/web", body)
	assertStatus(t, w, http.StatusOK)

	var v ServiceView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.App != "app1" || v.Service != "web" {
		t.Errorf("app/service were overwritten by the body: %+v", v)
	}
	if v.Status != registry.StatusDown {
		t.Errorf("status = %q, want down (derived by the server)", v.Status)
	}
	want := []string{"https://app1.localapp/", "https://web.app1.localapp/"}
	if !reflect.DeepEqual(v.URLs, want) {
		t.Errorf("urls = %v, want %v", v.URLs, want)
	}
}

// Liveness is determined by the server with a TCP connection to localhost:port.
func TestStatusDerivedFromTCPProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	srv, _ := newTestServer(t)
	w := do(t, srv, http.MethodPut, "/v1/apps/app1/services/web", `{"port": `+strconv.Itoa(port)+`}`)
	assertStatus(t, w, http.StatusOK)
	var v ServiceView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Status != registry.StatusUp {
		t.Errorf("status of a listening port = %q, want up", v.Status)
	}

	ln.Close()
	w = do(t, srv, http.MethodGet, "/v1/apps/app1", "")
	var app AppView
	if err := json.Unmarshal(w.Body.Bytes(), &app); err != nil {
		t.Fatal(err)
	}
	if app.Services[0].Status != registry.StatusDown {
		t.Errorf("status after shutdown = %q, want down", app.Services[0].Status)
	}
}

// TestStatusDownWhenWatchedPIDExited checks that a service is down once the
// `--pid` process has exited, even while the port is still listening
// (DESIGN.md "Registration lifecycle": `--pid` is optional).
func TestStatusDownWhenWatchedPIDExited(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)

	// The pid of a process that has exited and been reaped.
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running dummy process: %v", err)
	}
	dead := strconv.Itoa(cmd.Process.Pid)

	srv, _ := newTestServer(t)

	// The PUT response.
	w := do(t, srv, http.MethodPut, "/v1/apps/app1/services/web", `{"port": `+port+`, "pid": `+dead+`}`)
	assertStatus(t, w, http.StatusOK)
	var v ServiceView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Status != registry.StatusDown {
		t.Errorf("PUT: status = %q, want down", v.Status)
	}

	// The listing (GET /v1/apps) reaches the same conclusion.
	w = do(t, srv, http.MethodGet, "/v1/apps", "")
	assertStatus(t, w, http.StatusOK)
	var apps AppsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &apps); err != nil {
		t.Fatal(err)
	}
	if apps.Apps[0].Services[0].Status != registry.StatusDown {
		t.Errorf("GET /v1/apps: status = %q, want down", apps.Apps[0].Services[0].Status)
	}

	// A registration without a pid stays up on the same port.
	w = do(t, srv, http.MethodPut, "/v1/apps/app2/services/web", `{"port": `+port+`}`)
	assertStatus(t, w, http.StatusOK)
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Status != registry.StatusUp {
		t.Errorf("status without a pid = %q, want up", v.Status)
	}
}

func TestGetApps(t *testing.T) {
	srv, _ := newTestServer(t)
	do(t, srv, http.MethodPut, "/v1/apps/app2/services/web", `{"port": 3000}`)
	do(t, srv, http.MethodPut, "/v1/apps/app1/services/api", `{"port": 8000, "path": "/api"}`)

	w := do(t, srv, http.MethodGet, "/v1/apps", "")
	assertStatus(t, w, http.StatusOK)
	var resp AppsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Apps) != 2 || resp.Apps[0].Name != "app1" || resp.Apps[1].Name != "app2" {
		t.Fatalf("apps = %+v, want app1 then app2", resp.Apps)
	}
}

func TestGetAppsEmptyIsEmptyArray(t *testing.T) {
	srv, _ := newTestServer(t)
	w := do(t, srv, http.MethodGet, "/v1/apps", "")
	assertStatus(t, w, http.StatusOK)
	if got := strings.TrimSpace(w.Body.String()); !strings.Contains(got, `"apps": []`) {
		t.Errorf("empty listing = %s, want apps as an empty array (never null)", got)
	}
}

func TestGetAppNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	w := do(t, srv, http.MethodGet, "/v1/apps/nope", "")
	assertStatus(t, w, http.StatusNotFound)
	if code := decodeError(t, w).Code; code != CodeNotFound {
		t.Errorf("code = %q, want %q", code, CodeNotFound)
	}
}

func TestDelete(t *testing.T) {
	srv, _ := newTestServer(t)
	do(t, srv, http.MethodPut, "/v1/apps/app1/services/web", `{"port": 5173}`)
	do(t, srv, http.MethodPut, "/v1/apps/app1/services/api", `{"port": 8000}`)

	w := do(t, srv, http.MethodDelete, "/v1/apps/app1/services/api", "")
	assertStatus(t, w, http.StatusNoContent)
	if w.Body.Len() != 0 {
		t.Errorf("204 has a body: %s", w.Body.String())
	}

	w = do(t, srv, http.MethodDelete, "/v1/apps/app1/services/api", "")
	assertStatus(t, w, http.StatusNotFound)

	w = do(t, srv, http.MethodDelete, "/v1/apps/app1", "")
	assertStatus(t, w, http.StatusNoContent)

	w = do(t, srv, http.MethodDelete, "/v1/apps/app1", "")
	assertStatus(t, w, http.StatusNotFound)
}

func TestValidationErrors(t *testing.T) {
	srv, _ := newTestServer(t)
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"invalid app name", http.MethodPut, "/v1/apps/App_1/services/web", `{"port":80}`, 400, registry.CodeInvalidName},
		{"invalid service name", http.MethodPut, "/v1/apps/app1/services/WEB", `{"port":80}`, 400, registry.CodeInvalidName},
		{"port out of range", http.MethodPut, "/v1/apps/app1/services/web", `{"port":70000}`, 400, registry.CodeInvalidPort},
		{"port 0", http.MethodPut, "/v1/apps/app1/services/web", `{"port":0}`, 400, registry.CodeInvalidPort},
		{"missing port", http.MethodPut, "/v1/apps/app1/services/web", `{}`, 400, registry.CodeInvalidPort},
		{"invalid path", http.MethodPut, "/v1/apps/app1/services/web", `{"port":80,"path":"api"}`, 400, registry.CodeInvalidPath},
		{"invalid JSON", http.MethodPut, "/v1/apps/app1/services/web", `{`, 400, CodeBadJSON},
		{"no body", http.MethodPut, "/v1/apps/app1/services/web", "", 400, CodeBadJSON},
		{"wrong type for port", http.MethodPut, "/v1/apps/app1/services/web", `{"port":"80"}`, 400, CodeBadJSON},
		{"invalid app name on GET", http.MethodGet, "/v1/apps/App_1", "", 400, registry.CodeInvalidName},
		{"invalid app name on DELETE", http.MethodDelete, "/v1/apps/App_1", "", 400, registry.CodeInvalidName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := do(t, srv, tt.method, tt.path, tt.body)
			assertStatus(t, w, tt.wantStatus)
			if code := decodeError(t, w).Code; code != tt.wantCode {
				t.Errorf("code = %q, want %q (body: %s)", code, tt.wantCode, w.Body.String())
			}
		})
	}
}

func TestMethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer(t)
	tests := []struct{ method, path string }{
		{http.MethodPost, "/v1/status"},
		{http.MethodDelete, "/v1/status"},
		{http.MethodPost, "/v1/apps"},
		{http.MethodPut, "/v1/apps"},
		{http.MethodPost, "/v1/apps/app1"},
		{http.MethodPost, "/v1/apps/app1/services/web"},
		{http.MethodGet, "/v1/apps/app1/services/web"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			w := do(t, srv, tt.method, tt.path, "")
			assertStatus(t, w, http.StatusMethodNotAllowed)
			if code := decodeError(t, w).Code; code != CodeMethodNotAllowed {
				t.Errorf("code = %q, want %q", code, CodeMethodNotAllowed)
			}
			if allow := w.Header().Get("Allow"); allow == "" {
				t.Error("the Allow header is missing")
			}
		})
	}
}

func TestUnknownEndpoints(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, p := range []string{"/", "/v1", "/v2/apps", "/v1/nope", "/v1/apps/app1/nope/web", "/v1/apps/app1/services/web/extra"} {
		w := do(t, srv, http.MethodGet, p, "")
		assertStatus(t, w, http.StatusNotFound)
		if code := decodeError(t, w).Code; code != CodeNotFound {
			t.Errorf("code for %s = %q, want %q", p, code, CodeNotFound)
		}
	}
}

// The authority part is ignored (DESIGN.md "Control Plane API").
func TestAuthorityIgnored(t *testing.T) {
	srv, _ := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "http://whatever.example:9999/v1/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	assertStatus(t, w, http.StatusOK)
}

// Changes are persisted to registry.json.
func TestPutPersistsToDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	store, err := registry.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, Options{Domain: "localapp", Version: "0.1.0"})

	do(t, srv, http.MethodPut, "/v1/apps/app1/services/web", `{"port": 5173}`)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("registry.json was not created: %v", err)
	}
	reopened, err := registry.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	app, _ := reopened.App("app1")
	if _, ok := app.Service("web"); !ok {
		t.Error("not persisted")
	}
}
