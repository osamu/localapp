package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/osamu/localapp/internal/control"
	"github.com/osamu/localapp/internal/logstream"
	"github.com/osamu/localapp/internal/registry"
)

// startLogDaemon runs a control server on a temporary socket and points the
// CLI at it. The returned store receives forwarded output.
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
	if code := cmdRun([]string{"--app", "capture", "--", "sh", "-c", "printf 'stdout\\n'; printf 'stderr\\n' >&2; exit 7"}); code != 7 {
		t.Fatalf("exit=%d", code)
	}
	if text, _ := logs.Snapshot("capture", "web"); !strings.Contains(text, "stdout\n") || !strings.Contains(text, "stderr\n") {
		t.Fatalf("missing output: %q", text)
	}
}

func TestLogForwarderBoundsQueue(t *testing.T) {
	w := &logForwarder{}
	payload := []byte(strings.Repeat("x", logstream.MaxBytes*2))
	if n, _ := w.Write(payload); n != len(payload) || len(w.pending) > logstream.MaxBytes || !w.dropped {
		t.Fatal("queue not bounded")
	}
}

func TestLogForward(t *testing.T) {
	store, logs := startLogDaemon(t)
	store.Put("piped", registry.Service{Name: "api", Port: 65002})
	input := "plain\n\x00\xff binary あ\n" // invalid UTF-8 and a NUL must pass unchanged
	var out bytes.Buffer
	if code := forwardStdin("piped", "api", strings.NewReader(input), &out); code != exitOK || out.String() != input {
		t.Fatalf("exit=%d stdout=%q", code, out.String())
	}
	if text, _ := logs.Snapshot("piped", "api"); text != input {
		t.Fatalf("forwarded: %q", text)
	}
	// Unregistered: still copies, exits 0, warns exactly once.
	r, w, _ := os.Pipe()
	stderr := os.Stderr
	os.Stderr = w
	out.Reset()
	code := forwardStdin("nobody", "web", strings.NewReader("a\n"), &out)
	os.Stderr = stderr
	w.Close()
	var diag bytes.Buffer
	diag.ReadFrom(r)
	if code != exitOK || out.String() != "a\n" || strings.Count(diag.String(), "not registered") != 1 {
		t.Fatalf("unregistered: exit=%d stdout=%q stderr=%q", code, out.String(), diag.String())
	}
}
