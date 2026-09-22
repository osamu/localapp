package dashboard

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/osamu/localapp/internal/logstream"
	"github.com/osamu/localapp/internal/registry"
)

func TestLogPreview(t *testing.T) {
	logs := &logstream.Store{}
	h := New(&fakeStore{apps: []registry.App{{Name: "app", Services: []registry.Service{{Name: "web", Port: 65000}}}}}, Options{Logs: logs})
	page := get(t, h, "GET", "/logs?app=app&service=web")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "app/web logs") || !strings.Contains(page.Header().Get("Content-Security-Policy"), "script-src 'nonce-") {
		t.Fatal("missing preview or CSP")
	}
	logs.Append("app", "web", "<script>alert(1)</script>\n")
	rec := get(t, h, "GET", "/logs/data?app=app&service=web")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "<script>") {
		t.Fatalf("snapshot: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/logs", "/logs/data", "/logs/stream"} {
		if rec := get(t, h, "GET", path+"?app=missing&service=web"); rec.Code != 404 {
			t.Fatal("missing mapping allowed")
		}
		if rec := get(t, h, "POST", path+"?app=app&service=web"); rec.Code != http.StatusMethodNotAllowed {
			t.Fatal("mutation allowed")
		}
	}
}

// firstEvent opens the stream and returns its first event.
func firstEvent(t *testing.T, srv *httptest.Server, lastID string) (event string, id uint64, ev logstream.Event) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/logs/stream?app=app&service=web", nil)
	req.Header.Set("Last-Event-ID", lastID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	stop := errors.New("stop")
	err = logstream.ReadSSE(context.Background(), bufio.NewReader(resp.Body), func(e string, i uint64, v logstream.Event) error {
		event, id, ev = e, i, v
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("no event: %v", err)
	}
	return
}

func TestLogStream(t *testing.T) {
	logs := &logstream.Store{}
	srv := httptest.NewServer(New(&fakeStore{apps: []registry.App{{Name: "app", Services: []registry.Service{{Name: "web", Port: 65000}}}}}, Options{Logs: logs}))
	defer srv.Close()
	logs.Append("app", "web", "first\n")
	if event, id, ev := firstEvent(t, srv, ""); event != "reset" || id != 6 || ev.Text != "first\n" || !ev.Captured {
		t.Fatalf("initial: %s %d %+v", event, id, ev)
	}
	logs.Append("app", "web", "second\n")
	if event, id, ev := firstEvent(t, srv, "6"); event != "append" || id != 13 || ev.Text != "second\n" {
		t.Fatalf("resume: %s %d %+v", event, id, ev)
	}
	if event, _, ev := firstEvent(t, srv, "999"); event != "reset" || ev.Text != "first\nsecond\n" {
		t.Fatalf("stale id: %s %+v", event, ev)
	}
}
