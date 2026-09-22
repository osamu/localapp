package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/osamu/localapp/internal/control"
	"github.com/osamu/localapp/internal/logstream"
	"github.com/osamu/localapp/internal/registry"
)

// startLogDaemon runs a control server on a temporary socket and points the
// CLI at it. The returned store receives uploaded output.
func startLogDaemon(t *testing.T) (*registry.Store, *logstream.Store) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lalog-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "c.sock")
	store, err := registry.Open(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	logs := &logstream.Store{}
	ln, err := control.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: control.NewServer(store, control.Options{Logs: logs})}
	go server.Serve(ln)
	t.Cleanup(func() { server.Shutdown(context.Background()) })
	t.Setenv("LOCALAPP_SOCKET", socket)
	return store, logs
}

func TestRunCapturesOutput(t *testing.T) {
	_, logs := startLogDaemon(t)
	code := cmdRun([]string{"--app", "capture", "--", "sh", "-c", "printf 'stdout\\n'; printf 'stderr\\n' >&2; exit 7"})
	if code != 7 {
		t.Fatalf("exit=%d", code)
	}
	text, ok := logs.Snapshot("capture", "web")
	if !ok || !strings.Contains(text, "stdout\n") || !strings.Contains(text, "stderr\n") {
		t.Fatalf("missing output: %q", text)
	}
}

func TestLogUploaderBoundsQueue(t *testing.T) {
	w := &logUploader{}
	payload := []byte(strings.Repeat("x", logstream.MaxBytes*2))
	n, err := w.Write(payload)
	if err != nil || n != len(payload) || len(w.pending) > logstream.MaxBytes || !w.dropped {
		t.Fatal("queue not bounded")
	}
}

func TestLogUploaderWarnsOnceAndOnRecovery(t *testing.T) {
	store, logs := startLogDaemon(t)
	var warnings []string
	up := newLogUploader(newClient(), "later", "web", func(m string) { warnings = append(warnings, m) }, false)
	up.Write([]byte("early\n"))
	time.Sleep(3 * uploadInterval)
	up.Write([]byte("still early\n"))
	time.Sleep(3 * uploadInterval)
	port := 65001
	if _, err := store.Put("later", registry.Service{Name: "web", Port: port}); err != nil {
		t.Fatal(err)
	}
	up.Write([]byte("after\n"))
	up.Close()
	if len(warnings) != 2 || !strings.Contains(warnings[0], "not registered") || !strings.Contains(warnings[1], "resumed") {
		t.Fatalf("warnings: %q", warnings)
	}
	text, _ := logs.Snapshot("later", "web")
	if !strings.Contains(text, "output omitted") || !strings.HasSuffix(text, "after\n") || strings.Contains(text, "early") {
		t.Fatalf("recovered stream: %q", text)
	}
}

func TestTeePassesThroughAndUploads(t *testing.T) {
	store, logs := startLogDaemon(t)
	if _, err := store.Put("piped", registry.Service{Name: "api", Port: 65002}); err != nil {
		t.Fatal(err)
	}
	input := "plain\n\x00\xff binary あ\n" + strings.Repeat("y", 5000) // includes invalid UTF-8 and a NUL
	var out bytes.Buffer
	if code := teeStdin("piped", "api", strings.NewReader(input), &out); code != exitOK {
		t.Fatalf("exit=%d", code)
	}
	if out.String() != input {
		t.Fatalf("stdout altered: %q", out.String())
	}
	text, ok := logs.Snapshot("piped", "api")
	if !ok || text != input {
		t.Fatalf("uploaded output differs: %q", text)
	}
}

func TestTeeKeepsFlowingWhenUnregistered(t *testing.T) {
	startLogDaemon(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	var out bytes.Buffer
	code := teeStdin("nobody", "web", strings.NewReader("a\nb\n"), &out)
	os.Stderr = stderr
	w.Close()
	var diag bytes.Buffer
	diag.ReadFrom(r)
	if code != exitOK || out.String() != "a\nb\n" {
		t.Fatalf("exit=%d out=%q", code, out.String())
	}
	if n := strings.Count(diag.String(), "not registered"); n != 1 {
		t.Fatalf("expected exactly one warning, got %d: %q", n, diag.String())
	}
}

func TestTeeUsage(t *testing.T) {
	for _, args := range [][]string{{"a", "b"}, {"/web"}, {"app/"}, {"app/x/y"}} {
		if code := cmdTee(args); code != exitUsage {
			t.Fatalf("%v: exit=%d", args, code)
		}
	}
}
