package dashboard

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	logs.Append("app", "web", "<script>alert(1)</script>\nstdout\nstderr")
	rec := get(t, h, "GET", "/logs/data?app=app&service=web")
	var data struct {
		Text     string
		Captured bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if !data.Captured || data.Text != "<script>alert(1)</script>\nstdout\nstderr" {
		t.Fatalf("bad snapshot: %+v", data)
	}
	if strings.Contains(rec.Body.String(), "<script>") {
		t.Fatal("unescaped script in JSON")
	}
	for _, path := range []string{"/logs", "/logs/data"} {
		if rec := get(t, h, "GET", path+"?app=missing&service=web"); rec.Code != 404 {
			t.Fatal("missing mapping allowed")
		}
		if rec := get(t, h, "POST", path+"?app=app&service=web"); rec.Code != http.StatusMethodNotAllowed {
			t.Fatal("mutation allowed")
		}
		if rec := get(t, h, "HEAD", path+"?app=app&service=web"); rec.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
	}
}

// sseReader reads one SSE event (event name, id, data) at a time.
type sseReader struct{ r *bufio.Reader }

func (s sseReader) next(t *testing.T) (event, id string, data logEvent) {
	t.Helper()
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if event != "" {
				return
			}
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func openStream(t *testing.T, srv *httptest.Server, lastID string) (sseReader, func()) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/logs/stream?app=app&service=web", nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d type=%s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	return sseReader{bufio.NewReader(resp.Body)}, func() { resp.Body.Close() }
}

func TestLogStream(t *testing.T) {
	logs := &logstream.Store{}
	store := &fakeStore{apps: []registry.App{{Name: "app", Services: []registry.Service{{Name: "web", Port: 65000}}}}}
	h := New(store, Options{Logs: logs})
	srv := httptest.NewServer(h)
	defer srv.Close()

	logs.Append("app", "web", "first\n")
	s, closeStream := openStream(t, srv, "")
	event, id, data := s.next(t)
	if event != "reset" || id != "6" || data.Text != "first\n" || !data.Captured {
		t.Fatalf("initial: %s %s %+v", event, id, data)
	}
	logs.Append("app", "web", "second\n")
	if event, id, data = s.next(t); event != "append" || id != "13" || data.Text != "second\n" {
		t.Fatalf("delta: %s %s %+v", event, id, data)
	}
	closeStream()
	waitFor(t, func() bool { return logs.Subscribers("app", "web") == 0 })

	// Reconnect with a valid id: only what was missed, as a delta.
	logs.Append("app", "web", "third\n")
	s, closeStream = openStream(t, srv, "13")
	if event, id, data = s.next(t); event != "append" || id != "19" || data.Text != "third\n" {
		t.Fatalf("resume: %s %s %+v", event, id, data)
	}
	closeStream()

	// Reconnect with an id outside the window: full reset.
	s, closeStream = openStream(t, srv, "999")
	if event, _, data = s.next(t); event != "reset" || data.Text != "first\nsecond\nthird\n" {
		t.Fatalf("stale id: %s %+v", event, data)
	}
	closeStream()

	// A service with no output yet streams an empty reset with captured=false.
	srv2 := httptest.NewServer(New(store, Options{Logs: &logstream.Store{}}))
	defer srv2.Close()
	s, closeStream = openStream(t, srv2, "")
	defer closeStream()
	if event, id, data = s.next(t); event != "reset" || id != "0" || data.Captured || data.Text != "" {
		t.Fatalf("no output yet: %s %s %+v", event, id, data)
	}
}

func TestLogStreamLimitAndCancel(t *testing.T) {
	logs := &logstream.Store{}
	h := New(&fakeStore{apps: []registry.App{{Name: "app", Services: []registry.Service{{Name: "web", Port: 65000}}}}}, Options{Logs: logs})
	srv := httptest.NewServer(h)
	defer srv.Close()
	var closers []func()
	for i := 0; i < maxLogStreams; i++ {
		s, c := openStream(t, srv, "")
		s.next(t)
		closers = append(closers, c)
	}
	resp, err := http.Get(srv.URL + "/logs/stream?app=app&service=web")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("over limit: %d", resp.StatusCode)
	}
	for _, c := range closers {
		c()
	}
	waitFor(t, func() bool { return h.streams.Load() == 0 && logs.Subscribers("app", "web") == 0 })
	if rec := get(t, h, "GET", "/logs/stream?app=missing&service=web"); rec.Code != 404 {
		t.Fatal("missing mapping streamed")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}
