package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/osamu/localapp/internal/registry"
)

// startDaemon starts the API on a real Unix socket and returns a client.
func startDaemon(t *testing.T) (*Client, string) {
	t.Helper()
	// Use a short directory so the path fits the Unix socket length limit
	// (104 bytes on macOS).
	dir, err := os.MkdirTemp("", "localapp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	socketPath := filepath.Join(dir, "control.sock")
	store, err := registry.Open(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, Options{
		Domain:    "localapp",
		Version:   "0.1.0",
		Listeners: map[string]string{"dns": "127.0.0.1:15353"},
	})
	ln, err := Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, srv) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve does not stop")
		}
	})
	return NewClient(socketPath), socketPath
}

// The socket is 0600 (its permissions are the authorization;
// DESIGN.md "全体規約").
func TestListenSocketPermissions(t *testing.T) {
	_, socketPath := startDaemon(t)
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket permissions = %o, want 600", perm)
	}
}

// The socket of a running daemon is never taken over.
func TestListenRefusesLiveSocket(t *testing.T) {
	_, socketPath := startDaemon(t)
	if _, err := Listen(socketPath); err == nil {
		t.Error("Listen succeeded on a live socket")
	}
}

// A stale socket (left by a crashed daemon) can be reused.
func TestListenReclaimsStaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "localapp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socketPath := filepath.Join(dir, "control.sock")
	if err := os.WriteFile(socketPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(socketPath)
	if err != nil {
		t.Fatalf("cannot reuse a stale socket: %v", err)
	}
	ln.Close()
}

// Check the register, list and delete round trip through the client.
func TestClientLifecycle(t *testing.T) {
	c, _ := startDaemon(t)
	ctx := context.Background()

	st, raw, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "0.1.0" || st.Apps != 0 || st.Services != 0 {
		t.Errorf("initial status = %+v", st)
	}
	if len(raw) == 0 {
		t.Error("raw JSON is empty")
	}

	view, _, err := c.PutService(ctx, "app1", "web", ServiceRequest{Port: intp(5173)})
	if err != nil {
		t.Fatal(err)
	}
	if view.App != "app1" || view.Service != "web" || view.Port != 5173 {
		t.Errorf("PUT result = %+v", view)
	}
	want := []string{"https://app1.localapp/", "https://web.app1.localapp/"}
	if !reflect.DeepEqual(view.URLs, want) {
		t.Errorf("urls = %v, want %v", view.URLs, want)
	}

	if _, _, err := c.PutService(ctx, "app1", "api", ServiceRequest{Port: intp(8000), Path: "/api"}); err != nil {
		t.Fatal(err)
	}

	apps, _, err := c.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps.Apps) != 1 || len(apps.Apps[0].Services) != 2 {
		t.Fatalf("listing = %+v", apps.Apps)
	}

	app, _, err := c.GetApp(ctx, "app1")
	if err != nil {
		t.Fatal(err)
	}
	if app.Name != "app1" {
		t.Errorf("GetApp = %+v", app)
	}

	if err := c.DeleteService(ctx, "app1", "api"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteApp(ctx, "app1"); err != nil {
		t.Fatal(err)
	}

	apps, _, err = c.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps.Apps) != 0 {
		t.Errorf("listing after deletion = %+v, want empty", apps.Apps)
	}
}

func TestClientAPIError(t *testing.T) {
	c, _ := startDaemon(t)
	ctx := context.Background()

	_, _, err := c.PutService(ctx, "App_1", "web", ServiceRequest{Port: intp(80)})
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if ae.HTTPStatus != 400 || ae.Code != registry.CodeInvalidName {
		t.Errorf("APIError = %+v, want 400 / %s", ae, registry.CodeInvalidName)
	}

	err = c.DeleteApp(ctx, "nope")
	if !IsNotFound(err) {
		t.Errorf("deleting an unregistered app = %v, want 404", err)
	}
}

// With the daemon stopped the error is ErrUnavailable; the CLI uses it to
// decide on exit 1.
func TestClientUnavailable(t *testing.T) {
	dir, err := os.MkdirTemp("", "localapp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	c := NewClient(filepath.Join(dir, "control.sock"))
	if _, _, err := c.Status(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func intp(n int) *int { return &n }
