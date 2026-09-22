package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/osamu/localapp/internal/logstream"
	"github.com/osamu/localapp/internal/registry"
)

func TestLogAppend(t *testing.T) {
	s, registryStore := newTestServer(t)
	s.logs = &logstream.Store{}
	if _, err := registryStore.Put("app", registry.Service{Name: "web", Port: 65000}); err != nil {
		t.Fatal(err)
	}
	path := "/v1/apps/app/services/web/logs"
	// A UTF-8 character split across batches must survive batch boundaries.
	for _, chunk := range [][]byte{{0xe3}, {0x81, 0x82}} {
		body, _ := json.Marshal(struct {
			Text []byte `json:"text"`
		}{chunk})
		if rec := do(t, s, "POST", path, string(body)); rec.Code != 204 {
			t.Fatalf("append: %d %s", rec.Code, rec.Body.String())
		}
	}
	if text, _ := s.logs.Snapshot("app", "web"); text != "あ" {
		t.Fatalf("corrupt UTF-8: %q", text)
	}
	if rec := do(t, s, "DELETE", path, ""); rec.Code != 405 {
		t.Fatal("DELETE on logs accepted")
	}
	if rec := do(t, s, "GET", path, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"text": "あ"`) {
		t.Fatalf("GET window: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s, "GET", path+"/other", ""); rec.Code != 404 {
		t.Fatal("unknown logs sub-path accepted")
	}
	if rec := do(t, s, "POST", "/v1/apps/missing/services/web/logs", `{}`); rec.Code != 404 {
		t.Fatal("unknown mapping accepted")
	}
	if rec := do(t, s, "POST", path, `{"text":"`+strings.Repeat("a", 2<<20)+`"}`); rec.Code != 400 {
		t.Fatal("oversized body accepted")
	}
}

func TestLogReadAndFollow(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lactl-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	store, err := registry.Open(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("app", registry.Service{Name: "web", Port: 65000}); err != nil {
		t.Fatal(err)
	}
	logs := &logstream.Store{}
	socket := filepath.Join(dir, "c.sock")
	ln, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: NewServer(store, Options{Logs: logs})}
	go server.Serve(ln)
	defer server.Shutdown(context.Background())
	c := NewClient(socket)
	ctx := context.Background()

	if _, _, err := c.Logs(ctx, "nobody", "web"); !IsNotFound(err) {
		t.Fatalf("unregistered: %v", err)
	}
	ev, _, err := c.Logs(ctx, "app", "web")
	if err != nil || ev.Captured || ev.Text != "" || ev.Next != 0 {
		t.Fatalf("empty window: %+v %v", ev, err)
	}
	logs.Append("app", "web", "one\n")
	if ev, _, err = c.Logs(ctx, "app", "web"); err != nil || !ev.Captured || ev.Text != "one\n" || ev.Next != 4 {
		t.Fatalf("window: %+v %v", ev, err)
	}

	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var got []string
	done := make(chan error, 1)
	go func() {
		done <- c.FollowLogs(fctx, "app", "web", 0, func(event string, id uint64, ev logstream.Event) error {
			got = append(got, event+":"+ev.Text)
			if len(got) == 2 {
				cancel()
			}
			return nil
		})
	}()
	time.Sleep(200 * time.Millisecond)
	logs.Append("app", "web", "two\n")
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("follow ended: %v", err)
	}
	if len(got) != 2 || got[0] != "reset:one\n" || got[1] != "append:two\n" {
		t.Fatalf("events: %q", got)
	}
	if err := c.FollowLogs(ctx, "nobody", "web", 0, nil); !IsNotFound(err) {
		t.Fatalf("follow unregistered: %v", err)
	}
}
