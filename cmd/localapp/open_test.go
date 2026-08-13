package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/osamu/localapp/internal/control"
	"github.com/osamu/localapp/internal/registry"
)

// startTestDaemon starts the Control Plane API on a test socket and points
// LOCALAPP_SOCKET / LOCALAPP_STATE_DIR at it.
func startTestDaemon(t *testing.T) *registry.Store {
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
	srv := control.NewServer(store, control.Options{Domain: "localapp", Version: "test"})
	ln, err := control.Listen(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- control.Serve(ctx, ln, srv) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve does not stop")
		}
	})

	t.Setenv("LOCALAPP_STATE_DIR", dir)
	t.Setenv("LOCALAPP_SOCKET", socketPath)
	return store
}

// stubOpenURL replaces openURL with a recording stub.
func stubOpenURL(t *testing.T, err error) *[]string {
	t.Helper()
	var opened []string
	orig := openURL
	openURL = func(url string) error {
		opened = append(opened, url)
		return err
	}
	t.Cleanup(func() { openURL = orig })
	return &opened
}

func TestOpenUsesDefaultServiceURL(t *testing.T) {
	store := startTestDaemon(t)
	if _, err := store.Put("app1", registry.Service{Name: "api", Port: 8000, Path: "/api"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("app1", registry.Service{Name: "web", Port: 5173}); err != nil {
		t.Fatal(err)
	}
	opened := stubOpenURL(t, nil)

	var code int
	out := captureStdout(t, func() { code = run([]string{"open", "app1"}) })
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	want := "https://app1.localapp/"
	if len(*opened) != 1 || (*opened)[0] != want {
		t.Errorf("opened URL = %v, want [%s]", *opened, want)
	}
	if strings.TrimSpace(out) != want {
		t.Errorf("stdout = %q, want %q", strings.TrimSpace(out), want)
	}
}

// TestOpenFallsBackToFirstService checks that the URL of the first service is
// used when there is no default service.
func TestOpenFallsBackToFirstService(t *testing.T) {
	store := startTestDaemon(t)
	if _, err := store.Put("app1", registry.Service{Name: "api", Port: 8000}); err != nil {
		t.Fatal(err)
	}
	opened := stubOpenURL(t, nil)

	captureStdout(t, func() {
		if code := run([]string{"open", "app1"}); code != exitOK {
			t.Fatalf("exit code = %d", code)
		}
	})
	want := "https://api.app1.localapp/"
	if len(*opened) != 1 || (*opened)[0] != want {
		t.Errorf("opened URL = %v, want [%s]", *opened, want)
	}
}

// TestOpenUnregisteredIsError checks that an unregistered app exits 1
// （DESIGN.md「CLI」）。
func TestOpenUnregisteredIsError(t *testing.T) {
	startTestDaemon(t)
	opened := stubOpenURL(t, nil)

	if code := run([]string{"open", "nosuchapp"}); code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if len(*opened) != 0 {
		t.Errorf("the browser was launched for an unregistered app: %v", *opened)
	}
}

// TestOpenBrowserFailureIsError checks that a failure to launch the browser
// exits 1.
func TestOpenBrowserFailureIsError(t *testing.T) {
	store := startTestDaemon(t)
	if _, err := store.Put("app1", registry.Service{Name: "web", Port: 5173}); err != nil {
		t.Fatal(err)
	}
	stubOpenURL(t, errors.New("no browser"))

	var code int
	captureStdout(t, func() { code = run([]string{"open", "app1"}) })
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
}

func TestOpenBadUsage(t *testing.T) {
	for _, args := range [][]string{{"open"}, {"open", "a", "b"}} {
		if code := run(args); code != exitUsage {
			t.Errorf("run(%v) = %d, want %d", args, code, exitUsage)
		}
	}
}
