package control

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/osamu/localapp/internal/logstream"
	"github.com/osamu/localapp/internal/registry"
)

func TestLogUpload(t *testing.T) {
	s, registryStore := newTestServer(t)
	s.logs = &logstream.Store{}
	if _, err := registryStore.Put("app", registry.Service{Name: "web", Port: 65000}); err != nil {
		t.Fatal(err)
	}
	path := "/v1/apps/app/services/web/logs"
	// A UTF-8 character split across batches must survive upload boundaries.
	for _, chunk := range [][]byte{{0xe3}, {0x81, 0x82}} {
		body, _ := json.Marshal(struct {
			Text []byte `json:"text"`
		}{chunk})
		if rec := do(t, s, "POST", path, string(body)); rec.Code != 204 {
			t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
		}
	}
	if text, _ := s.logs.Snapshot("app", "web"); text != "あ" {
		t.Fatalf("corrupt UTF-8: %q", text)
	}
	if rec := do(t, s, "GET", path, ""); rec.Code != 405 {
		t.Fatal("GET upload accepted")
	}
	if rec := do(t, s, "POST", "/v1/apps/missing/services/web/logs", `{}`); rec.Code != 404 {
		t.Fatal("unknown mapping accepted")
	}
	if rec := do(t, s, "POST", path, `{"text":"`+strings.Repeat("a", 2<<20)+`"}`); rec.Code != 400 {
		t.Fatal("oversized body accepted")
	}
}
