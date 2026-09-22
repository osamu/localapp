package control

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/osamu/localapp/internal/logstream"
	"github.com/osamu/localapp/internal/registry"
)

func TestLogs(t *testing.T) {
	s, registryStore := newTestServer(t)
	s.logs = &logstream.Store{}
	registryStore.Put("app", registry.Service{Name: "web", Port: 65000})
	path := "/v1/apps/app/services/web/logs"
	// A UTF-8 character split across batches must survive batch boundaries.
	for _, chunk := range [][]byte{[]byte("\xe3\x81"), []byte("\x82")} {
		body, _ := json.Marshal(struct {
			Text []byte `json:"text"`
		}{chunk})
		if rec := do(t, s, "POST", path, string(body)); rec.Code != 204 {
			t.Fatalf("append: %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, s, "GET", path, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"text": "あ"`) {
		t.Fatalf("window: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, s, "POST", "/v1/apps/missing/services/web/logs", `{}`); rec.Code != 404 {
		t.Fatal("unregistered service accepted")
	}
	if rec := do(t, s, "POST", path, `{"text":"`+strings.Repeat("a", 2<<20)+`"}`); rec.Code != 400 {
		t.Fatal("oversized batch accepted")
	}
}
