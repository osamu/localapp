package dashboard

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

	"github.com/osamu/localapp/internal/logstream"
)

// logChunk transports raw bytes so split UTF-8 sequences survive batch boundaries.
type logChunk struct {
	Data      []byte `json:"data"`
	ExpiresAt int64  `json:"expiresAt"`
}

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
	if r.URL.Path == "/logs/data" {
		var text strings.Builder
		chunks := []logChunk{}
		var captured bool
		if h.logs != nil {
			var retained []logstream.Chunk
			retained, captured = h.logs.Chunks(app, service)
			for _, chunk := range retained {
				text.WriteString(chunk.Text)
				chunks = append(chunks, logChunk{Data: []byte(chunk.Text), ExpiresAt: chunk.ExpiresAt})
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method != http.MethodHead {
			_ = json.NewEncoder(w).Encode(struct {
				Text     string     `json:"text"`
				Chunks   []logChunk `json:"chunks"`
				Captured bool       `json:"captured"`
			}{text.String(), chunks, captured})
		}
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
<p id="empty" hidden>No captured output yet. Start this service with <code>localapp run --app {{.App}} --service {{.Service}} -- &lt;command&gt;</code>. Apps registered only with <code>localapp add</code> do not provide process output.</p>
<pre id="output" tabindex="0" aria-label="Application log output"></pre>
<p class="hint">Updates every second. Only the last 5 minutes of combined stdout/stderr (up to 256 KiB); expired output is removed even while paused or disconnected; history is held in memory and cleared when the daemon restarts. Up to 64 recently active services are retained.</p>
<noscript>Enable JavaScript to preview live logs.</noscript>
<script nonce="{{.Nonce}}">
const output = document.getElementById('output');
const status = document.getElementById('status');
const pause = document.getElementById('pause');
let paused = false;
let chunks = [];
function expireAndRender() {
 chunks = chunks.filter(chunk => chunk.expiresAt > Date.now());
 const decoder = new TextDecoder();
 const text = chunks.map(chunk => decoder.decode(Uint8Array.from(atob(chunk.data), c => c.charCodeAt(0)), {stream: true})).join('') + decoder.decode();
 if (output.textContent !== text) {
  output.textContent = text;
  if (!paused && document.getElementById('scroll').checked) output.scrollTop = output.scrollHeight;
 }
}
setInterval(expireAndRender, 1000);
pause.addEventListener('click', () => { paused = !paused; pause.textContent = paused ? 'Resume' : 'Pause'; status.textContent = paused ? 'Paused' : 'Connecting…'; });
async function refresh() {
 if (!paused) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 5000);
  try {
   const response = await fetch('/logs/data' + location.search, {cache: 'no-store', signal: controller.signal});
   if (!response.ok) throw new Error(response.status === 404 ? 'Service removed. Return to the dashboard.' : 'Connection lost. Retrying…');
   const data = await response.json();
   if (!paused) {
    chunks = data.chunks;
    expireAndRender();
    document.getElementById('empty').hidden = data.captured;
    status.textContent = data.captured ? (data.text ? 'Live' : 'Waiting for output…') : 'No log stream';
   }
  } catch (error) { if (!paused) status.textContent = error.name === 'AbortError' ? 'Connection timed out. Retrying…' : error.message; }
  finally { clearTimeout(timeout); }
 }
 setTimeout(refresh, 1000);
}
refresh();
</script></main></body></html>`))
