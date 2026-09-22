package main

import (
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

func TestRunCapturesOutput(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lalog-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
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
	defer server.Shutdown(context.Background())
	t.Setenv("LOCALAPP_SOCKET", socket)
	code := cmdRun([]string{"--app", "capture", "--", "sh", "-c", "printf 'stdout\\n'; printf 'stderr\\n' >&2; exit 7"})
	if code != 7 {
		t.Fatalf("exit=%d", code)
	}
	text, ok := logs.Snapshot("capture", "web")
	if !ok || !strings.Contains(text, "stdout\n") || !strings.Contains(text, "stderr\n") {
		t.Fatalf("missing output: %q", text)
	}
}

func TestRunLogWriterBoundsQueue(t *testing.T) {
	w := &runLogWriter{}
	payload := []byte(strings.Repeat("x", logstream.MaxBytes*2))
	n, err := w.Write(payload)
	if err != nil || n != len(payload) || len(w.pending) > logstream.MaxBytes || !w.dropped {
		t.Fatal("queue not bounded")
	}
}
