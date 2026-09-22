package dashboard

import (
	"encoding/json"
	"net/http"
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
