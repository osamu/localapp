package proxy

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/osamu/localapp/internal/registry"
)

// capture records the request a backend received.
type capture struct {
	Method   string
	Path     string
	RawQuery string
	Host     string
	Header   http.Header
}

// backend is a forwarding target used by the tests.
type backend struct {
	srv  *httptest.Server
	port int
	mu   sync.Mutex
	last capture
}

// newBackend starts a backend that responds with body.
func newBackend(t *testing.T, body string) *backend {
	t.Helper()
	b := &backend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.last = capture{
			Method:   r.Method,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
			Host:     r.Host,
			Header:   r.Header.Clone(),
		}
		b.mu.Unlock()
		fmt.Fprint(w, body)
	}))
	t.Cleanup(b.srv.Close)
	b.port = serverPort(t, b.srv)
	return b
}

// newRawBackend starts a backend with an arbitrary handler.
func newRawBackend(t *testing.T, h http.HandlerFunc) *backend {
	t.Helper()
	b := &backend{}
	b.srv = httptest.NewServer(h)
	t.Cleanup(b.srv.Close)
	b.port = serverPort(t, b.srv)
	return b
}

func (b *backend) got() capture {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing the test server URL: %v", err)
	}
	_, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("extracting the port: %v", err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("converting the port: %v", err)
	}
	return n
}

// freePort returns a port number nobody listens on (to make liveness probes
// fail).
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// newStore returns an empty registry in a temporary directory.
func newStore(t *testing.T) *registry.Store {
	t.Helper()
	s, err := registry.Open(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatalf("registry.Open: %v", err)
	}
	return s
}

func put(t *testing.T, s *registry.Store, app string, svc registry.Service) {
	t.Helper()
	if _, err := s.Put(app, svc); err != nil {
		t.Fatalf("registry.Put(%s/%s): %v", app, svc.Name, err)
	}
}

// quietLogger hides forwarding error logs from the test output.
func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// front starts the proxy as a real server (needed by the tests that hijack).
func front(t *testing.T, p *Proxy) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv
}

// do sends a request to the proxy with an explicit Host.
func do(t *testing.T, srv *httptest.Server, host, target string, header http.Header) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+target, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Host = host
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("sending the request: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	return resp, string(body)
}

// TestSubdomainRouting checks that `<service>.<app>.<domain>` forwards with the
// path passed through.
func TestSubdomainRouting(t *testing.T) {
	api := newBackend(t, "api-ok")
	store := newStore(t)
	// Even with strip_path set, the subdomain route does not drop the path.
	put(t, store, "app1", registry.Service{Name: "api", Port: api.port, Path: "/api", StripPath: true})
	srv := front(t, newProxy(store, "localapp"))

	resp, body := do(t, srv, "api.app1.localapp", "/api/users?x=1&y=%20z", nil)
	if resp.StatusCode != http.StatusOK || body != "api-ok" {
		t.Fatalf("status = %d, body = %q", resp.StatusCode, body)
	}
	got := api.got()
	if got.Path != "/api/users" {
		t.Errorf("forwarded path = %q, want /api/users (passed through)", got.Path)
	}
	if got.RawQuery != "x=1&y=%20z" {
		t.Errorf("query = %q, want x=1&y=%%20z", got.RawQuery)
	}
}

// TestPathMountLongestMatch checks the longest path prefix match of
// `<app>.<domain>`.
func TestPathMountLongestMatch(t *testing.T) {
	web := newBackend(t, "web")
	api := newBackend(t, "api")
	apiV2 := newBackend(t, "api-v2")

	store := newStore(t)
	put(t, store, "app1", registry.Service{Name: "web", Port: web.port})
	put(t, store, "app1", registry.Service{Name: "api", Port: api.port, Path: "/api"})
	put(t, store, "app1", registry.Service{Name: "api-v2", Port: apiV2.port, Path: "/api/v2"})
	srv := front(t, newProxy(store, "localapp"))

	tests := []struct {
		target   string
		wantBody string
		wantPath string
	}{
		{target: "/", wantBody: "web", wantPath: "/"},
		{target: "/apidocs", wantBody: "web", wantPath: "/apidocs"},
		{target: "/api/users", wantBody: "api", wantPath: "/api/users"},
		{target: "/api", wantBody: "api", wantPath: "/api"},
		{target: "/api/v2/items", wantBody: "api-v2", wantPath: "/api/v2/items"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			resp, body := do(t, srv, "app1.localapp", tt.target, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, body = %q", resp.StatusCode, body)
			}
			if body != tt.wantBody {
				t.Fatalf("target = %q, want %q", body, tt.wantBody)
			}
			// The default is not to strip (DESIGN.md "Routing model").
			var got capture
			switch tt.wantBody {
			case "web":
				got = web.got()
			case "api":
				got = api.got()
			default:
				got = apiV2.got()
			}
			if got.Path != tt.wantPath {
				t.Errorf("forwarded path = %q, want %q", got.Path, tt.wantPath)
			}
		})
	}
}

// TestStripPath checks that the mount prefix is dropped when --strip-path is
// set.
func TestStripPath(t *testing.T) {
	api := newBackend(t, "api")
	store := newStore(t)
	put(t, store, "app1", registry.Service{Name: "api", Port: api.port, Path: "/api", StripPath: true})
	srv := front(t, newProxy(store, "localapp"))

	tests := []struct{ target, wantPath string }{
		{target: "/api/users", wantPath: "/users"},
		{target: "/api", wantPath: "/"},
		{target: "/api/", wantPath: "/"},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			if resp, body := do(t, srv, "app1.localapp", tt.target, nil); resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, body = %q", resp.StatusCode, body)
			}
			if got := api.got().Path; got != tt.wantPath {
				t.Errorf("forwarded path = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

// TestForwardedHeaders checks that Host keeps its original value and that
// X-Forwarded-* are set.
func TestForwardedHeaders(t *testing.T) {
	web := newBackend(t, "ok")
	store := newStore(t)
	put(t, store, "app1", registry.Service{Name: "web", Port: web.port})
	srv := front(t, newProxy(store, "localapp"))

	// Headers spoofed by the client must be overwritten.
	h := http.Header{}
	h.Set("X-Forwarded-Proto", "http")
	h.Set("X-Forwarded-Host", "evil.example.com")
	h.Set("X-Forwarded-For", "203.0.113.9")
	if resp, body := do(t, srv, "app1.localapp", "/", h); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %q", resp.StatusCode, body)
	}

	got := web.got()
	if got.Host != "app1.localapp" {
		t.Errorf("Host = %q, want app1.localapp (original value kept)", got.Host)
	}
	if v := got.Header.Get("X-Forwarded-Proto"); v != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want https", v)
	}
	if v := got.Header.Get("X-Forwarded-Host"); v != "app1.localapp" {
		t.Errorf("X-Forwarded-Host = %q, want app1.localapp", v)
	}
	xff := got.Header.Get("X-Forwarded-For")
	if xff == "" {
		t.Error("X-Forwarded-For was not set")
	}
	if strings.Contains(xff, "203.0.113.9") {
		t.Errorf("X-Forwarded-For = %q: the client-supplied value survived", xff)
	}
}

// TestUnavailablePage checks the 503 page shown when the target is not
// listening.
func TestUnavailablePage(t *testing.T) {
	port := freePort(t)
	store := newStore(t)
	put(t, store, "app1", registry.Service{Name: "api", Port: port, Path: "/api"})
	srv := front(t, newProxy(store, "localapp"))

	resp, body := do(t, srv, "app1.localapp", "/api/users", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	// It contains the registration details (app / service / port).
	for _, want := range []string{"app1", "api", strconv.Itoa(port), "https://api.app1.localapp/"} {
		if !strings.Contains(body, want) {
			t.Errorf("the error page does not contain %q:\n%s", want, body)
		}
	}
}

// TestTransportErrorPage checks that a forwarding failure after the liveness
// probe passed also yields a 503 page.
func TestTransportErrorPage(t *testing.T) {
	// A backend that accepts connections but disconnects without answering.
	be := newRawBackend(t, func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		conn.Close()
	})
	store := newStore(t)
	put(t, store, "app1", registry.Service{Name: "web", Port: be.port})
	srv := front(t, New(store, Options{Domain: "localapp", ErrorLog: quietLogger()}))

	resp, body := do(t, srv, "app1.localapp", "/", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "app1") {
		t.Errorf("the error page does not contain the registration details:\n%s", body)
	}
}

// TestUnknownAppPage checks that a host with an unregistered app yields a 404
// page.
func TestUnknownAppPage(t *testing.T) {
	store := newStore(t)
	srv := front(t, newProxy(store, "localapp"))

	resp, body := do(t, srv, "nope.localapp", "/", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if !strings.Contains(body, "nope") {
		t.Errorf("the error page does not contain the app name:\n%s", body)
	}
}

// TestErrorPageEscapesHost checks that the error page is HTML-escaped
// (DESIGN.md "Security", output of registered values).
func TestErrorPageEscapesHost(t *testing.T) {
	p := newProxy(fakeStore{}, "localapp")
	req := httptest.NewRequest(http.MethodGet, "http://placeholder/", nil)
	req.Host = "<script>alert(1)</script>.localapp"
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if strings.Contains(body, "<script>") {
		t.Errorf("the host name is not escaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("the escaped host name is not present:\n%s", body)
	}
}

// TestApex checks that the apex is handed to the injected dashboard.
func TestApex(t *testing.T) {
	store := newStore(t)
	dash := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "dashboard:%s", r.URL.Path)
	})
	srv := front(t, New(store, Options{Domain: "localapp", Dashboard: dash}))
	resp, body := do(t, srv, "localapp", "/apps", nil)
	if resp.StatusCode != http.StatusOK || body != "dashboard:/apps" {
		t.Fatalf("status = %d, body = %q", resp.StatusCode, body)
	}
}

// TestRegistryChangeIsImmediate checks that additions and changes to the
// registry take effect on the next request.
func TestRegistryChangeIsImmediate(t *testing.T) {
	store := newStore(t)
	srv := front(t, newProxy(store, "localapp"))

	if resp, _ := do(t, srv, "app1.localapp", "/", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("before registration: status = %d, want 404", resp.StatusCode)
	}

	first := newBackend(t, "first")
	put(t, store, "app1", registry.Service{Name: "web", Port: first.port})
	if resp, body := do(t, srv, "app1.localapp", "/", nil); resp.StatusCode != http.StatusOK || body != "first" {
		t.Fatalf("after registration: status = %d, body = %q", resp.StatusCode, body)
	}

	// A port change (simulating a dev server restart) also takes effect at once.
	second := newBackend(t, "second")
	put(t, store, "app1", registry.Service{Name: "web", Port: second.port})
	if resp, body := do(t, srv, "app1.localapp", "/", nil); resp.StatusCode != http.StatusOK || body != "second" {
		t.Fatalf("after the port change: status = %d, body = %q", resp.StatusCode, body)
	}

	// After removal it goes back to the 404 page.
	if _, err := store.RemoveApp("app1"); err != nil {
		t.Fatalf("RemoveApp: %v", err)
	}
	if resp, _ := do(t, srv, "app1.localapp", "/", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after removal: status = %d, want 404", resp.StatusCode)
	}
}

// TestWebSocketPassthrough checks that Upgrade requests pass through (verified
// with a plain HTTP Upgrade rather than gorilla/websocket).
func TestWebSocketPassthrough(t *testing.T) {
	be := newRawBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "missing upgrade header", http.StatusBadRequest)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nX-Backend-Path: %s\r\n\r\n", r.URL.Path)
		if err := buf.Flush(); err != nil {
			return
		}
		// Receive one line and echo it back.
		line, err := buf.ReadString('\n')
		if err != nil {
			return
		}
		fmt.Fprintf(buf, "echo:%s", line)
		_ = buf.Flush()
	})

	store := newStore(t)
	put(t, store, "app1", registry.Service{Name: "web", Port: be.port})
	srv := front(t, newProxy(store, "localapp"))

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("connecting to the proxy: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("setting the deadline: %v", err)
	}

	req := "GET /hmr HTTP/1.1\r\n" +
		"Host: app1.localapp\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatalf("sending the Upgrade request: %v", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	if got := resp.Header.Get("Upgrade"); !strings.EqualFold(got, "websocket") {
		t.Errorf("Upgrade = %q, want websocket", got)
	}
	if got := resp.Header.Get("X-Backend-Path"); got != "/hmr" {
		t.Errorf("path received by the backend = %q, want /hmr", got)
	}

	// Data flows in both directions.
	if _, err := io.WriteString(conn, "ping\n"); err != nil {
		t.Fatalf("sending data: %v", err)
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the echo: %v", err)
	}
	if line != "echo:ping\n" {
		t.Errorf("echo = %q, want %q", line, "echo:ping\n")
	}
}

// TestStreamingIsFlushed checks that FlushInterval: -1 makes the response flow
// immediately (SSE and dev server progress output).
func TestStreamingIsFlushed(t *testing.T) {
	release := make(chan struct{})
	be := newRawBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		fmt.Fprint(w, "data: second\n\n")
	})

	store := newStore(t)
	put(t, store, "app1", registry.Service{Name: "web", Port: be.port})
	srv := front(t, newProxy(store, "localapp"))

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/events", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Host = "app1.localapp"
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("sending the request: %v", err)
	}
	defer resp.Body.Close()

	// The first chunk arrives before the backend handler returns.
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the first chunk: %v", err)
	}
	if line != "data: first\n" {
		t.Fatalf("first chunk = %q, want %q", line, "data: first\n")
	}
	close(release)
}

// TestMethodAndBodyPassthrough checks that the method and body of a POST pass
// through.
func TestMethodAndBodyPassthrough(t *testing.T) {
	var (
		mu      sync.Mutex
		gotBody string
	)
	be := newRawBackend(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = string(b)
		mu.Unlock()
		fmt.Fprintf(w, "%s", r.Method)
	})
	store := newStore(t)
	put(t, store, "app1", registry.Service{Name: "web", Port: be.port})
	srv := front(t, newProxy(store, "localapp"))

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/submit", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Host = "app1.localapp"
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("sending the request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != http.MethodPost {
		t.Errorf("method = %q, want POST", string(body))
	}
	mu.Lock()
	defer mu.Unlock()
	if gotBody != `{"a":1}` {
		t.Errorf("body = %q, want %q", gotBody, `{"a":1}`)
	}
}
