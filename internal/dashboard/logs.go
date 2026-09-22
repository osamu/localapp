package dashboard

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"time"
)

// maxLogStreams bounds concurrent SSE readers; each holds a goroutine and a
// one-slot wake channel while connected.
const maxLogStreams = 32

// logKeepAlive is the idle interval between SSE comment lines, which keep
// intermediaries from timing the connection out and let the handler notice a
// removed mapping.
const logKeepAlive = 15 * time.Second

func (h *Handler) serveLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.methodNotAllowed(w, "GET, HEAD")
		return
	}
	app, service := r.URL.Query().Get("app"), r.URL.Query().Get("service")
	if !h.mappingExists(app, service) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch r.URL.Path {
	case "/logs/data":
		var text string
		var captured bool
		if h.logs != nil {
			text, captured = h.logs.Snapshot(app, service)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method != http.MethodHead {
			_ = json.NewEncoder(w).Encode(logEvent{Text: text, Captured: captured})
		}
		return
	case "/logs/stream":
		h.streamLogs(w, r, app, service)
		return
	}
	nonce := newCSRFToken()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+nonce+"'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	var buf bytes.Buffer
	if err := logsTemplate.Execute(&buf, struct{ App, Service, Nonce string }{app, service, nonce}); err != nil {
		http.Error(w, "logs", 500)
		return
	}
	if r.Method != http.MethodHead {
		_, _ = w.Write(buf.Bytes())
	}
}

// logEvent is the JSON payload of /logs/data and of each SSE event.
type logEvent struct {
	Text     string `json:"text"`
	Captured bool   `json:"captured"`
}

// streamLogs serves the log stream as Server-Sent Events (DESIGN.md "Web log
// preview"). Events: "reset" carries the whole retained window and replaces
// the client's view; "append" carries only new output; "gone" ends the stream
// when the mapping is removed. Every event's id is the byte offset to resume
// from, which EventSource sends back as Last-Event-ID on reconnect.
func (h *Handler) streamLogs(w http.ResponseWriter, r *http.Request, app, service string) {
	flusher, ok := w.(http.Flusher)
	if !ok || h.logs == nil {
		http.Error(w, "streaming unsupported", http.StatusNotImplemented)
		return
	}
	if h.streams.Add(1) > maxLogStreams {
		h.streams.Add(-1)
		http.Error(w, "too many log streams", http.StatusServiceUnavailable)
		return
	}
	defer h.streams.Add(-1)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	offset, _ := strconv.ParseUint(r.Header.Get("Last-Event-ID"), 10, 64)
	wake, cancel := h.logs.Subscribe(app, service)
	defer cancel()

	send := func(event string, id uint64, payload logEvent) bool {
		data, _ := json.Marshal(payload) // one line: JSON escapes newlines
		_, err := w.Write([]byte("event: " + event + "\nid: " + strconv.FormatUint(id, 10) + "\ndata: " + string(data) + "\n\n"))
		flusher.Flush()
		return err == nil
	}
	// The first event always resets so a fresh page starts from the
	// retained window; on reconnect a valid Last-Event-ID yields a delta.
	text, next, captured, reset := h.logs.ReadFrom(app, service, offset)
	if offset == 0 {
		reset = true
	}
	if !send(map[bool]string{true: "reset", false: "append"}[reset], next, logEvent{text, captured}) {
		return
	}
	offset = next
	keepAlive := time.NewTicker(logKeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			if !h.mappingExists(app, service) {
				send("gone", offset, logEvent{})
				return
			}
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-wake:
			text, next, captured, reset := h.logs.ReadFrom(app, service, offset)
			if text == "" && !reset {
				continue
			}
			event := "append"
			if reset {
				event = "reset"
			}
			if !send(event, next, logEvent{text, captured}) {
				return
			}
			offset = next
		}
	}
}

var logsTemplate = template.Must(template.New("logs").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.App}}/{{.Service}} logs</title><style>
:root { color-scheme: light dark; }
body { font: 16px/1.6 -apple-system,BlinkMacSystemFont,sans-serif; margin: 0; padding: 2rem; }
main { max-width: 80rem; margin: auto; } h1 { font-size: 1.3rem; }
.controls { display: flex; gap: 1rem; flex-wrap: wrap; align-items: center; }
button { font: inherit; padding: .3rem .8rem; cursor: pointer; }
pre { background: #111820; color: #e5edf5; padding: 1rem; height: 60vh; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; border-radius: .5rem; font: 13px/1.5 ui-monospace,monospace; }
.hint { opacity: .7; font-size: .9rem; }
</style></head><body><main>
<a href="/">← Dashboard</a><h1>{{.App}}/{{.Service}} logs</h1>
<div class="controls"><button id="pause" type="button">Pause</button><label><input id="scroll" type="checkbox" checked> Auto-scroll</label><span id="status" role="status">Connecting…</span></div>
<p id="empty" hidden>No captured output yet. Start this service with <code>localapp run --app {{.App}} --service {{.Service}} -- &lt;command&gt;</code>, or pipe an already running process into it: <code>&lt;command&gt; 2&gt;&amp;1 | localapp tee {{.App}}/{{.Service}}</code>.</p>
<pre id="output" tabindex="0" aria-label="Application log output"></pre>
<p class="hint">Live stream of combined stdout/stderr. Only the last 1000 lines (up to 256 KiB) are kept; history is held in memory and cleared when the daemon restarts. Up to 64 recently active services are retained.</p>
<noscript>Enable JavaScript to preview live logs.</noscript>
<script nonce="{{.Nonce}}">
const MAX_LINES = 1000, MAX_BYTES = 256 * 1024;
const output = document.getElementById('output');
const status = document.getElementById('status');
const pause = document.getElementById('pause');
let text = '', source = null;
// Mirror the daemon's retention so the page never grows past what it keeps.
function trim(s) {
 let lines = s.split('\n');
 if (lines.length > MAX_LINES + 1) s = lines.slice(-(MAX_LINES + 1)).join('\n');
 if (s.length > MAX_BYTES) { s = s.slice(-MAX_BYTES); const nl = s.indexOf('\n'); if (nl >= 0 && nl + 1 < s.length) s = s.slice(nl + 1); }
 return s;
}
function render() {
 output.textContent = text;
 if (document.getElementById('scroll').checked) output.scrollTop = output.scrollHeight;
}
function apply(event, replace) {
 const data = JSON.parse(event.data);
 text = trim(replace ? data.text : text + data.text);
 render();
 document.getElementById('empty').hidden = data.captured;
 status.textContent = data.captured ? (text ? 'Live' : 'Waiting for output…') : 'No log stream';
}
function connect() {
 source = new EventSource('/logs/stream' + location.search);
 source.addEventListener('reset', e => apply(e, true));
 source.addEventListener('append', e => apply(e, false));
 source.addEventListener('gone', () => { source.close(); status.textContent = 'Service removed. Return to the dashboard.'; });
 source.onerror = () => { status.textContent = 'Connection lost. Reconnecting…'; };
}
pause.addEventListener('click', () => {
 if (source) { source.close(); source = null; pause.textContent = 'Resume'; status.textContent = 'Paused'; }
 else { pause.textContent = 'Pause'; status.textContent = 'Connecting…'; connect(); }
});
connect();
</script></main></body></html>`))
