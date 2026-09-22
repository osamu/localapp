package logstream

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"context"
)

// KeepAlive is the idle interval between SSE comment lines. They keep
// intermediaries from timing the connection out and give ServeSSE a chance to
// notice that the mapping is gone.
const KeepAlive = 15 * time.Second

// Event is the JSON payload of every log event and of the snapshot
// endpoints. Text is the retained window (reset) or new output (append).
// Captured reports whether the service has forwarded output since the daemon
// started. Next is the offset to continue from (snapshot endpoints only; SSE
// carries it as the event id).
type Event struct {
	Text     string `json:"text"`
	Captured bool   `json:"captured"`
	Next     uint64 `json:"next,omitempty"`
}

// ServeSSE streams a service's output as Server-Sent Events (DESIGN.md "Web
// log preview"). Events: "reset" carries the whole retained window and
// replaces the reader's view; "append" carries only new output; "gone" ends
// the stream when alive reports false. Every event's id is the byte offset to
// resume from, which EventSource (and the CLI) send back as Last-Event-ID.
// Append never waits on a reader: the stream holds a one-slot wake channel and
// re-reads the store from its own offset. It returns when the request context
// ends, the write fails, or alive reports false.
func (s *Store) ServeSSE(w http.ResponseWriter, r *http.Request, app, service string, alive func() bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusNotImplemented)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	offset, _ := strconv.ParseUint(r.Header.Get("Last-Event-ID"), 10, 64)
	wake, cancel := s.Subscribe(app, service)
	defer cancel()

	send := func(event string, id uint64, payload Event) bool {
		data, _ := json.Marshal(payload) // one line: JSON escapes newlines
		_, err := w.Write([]byte("event: " + event + "\nid: " + strconv.FormatUint(id, 10) + "\ndata: " + string(data) + "\n\n"))
		flusher.Flush()
		return err == nil
	}
	// The first event always resets so a fresh reader starts from the
	// retained window; on reconnect a valid Last-Event-ID yields a delta.
	text, next, captured, reset := s.ReadFrom(app, service, offset)
	if offset == 0 {
		reset = true
	}
	if !send(eventName(reset), next, Event{Text: text, Captured: captured}) {
		return
	}
	offset = next
	keepAlive := time.NewTicker(KeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			if !alive() {
				send("gone", offset, Event{})
				return
			}
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-wake:
			text, next, captured, reset := s.ReadFrom(app, service, offset)
			if text == "" && !reset {
				continue
			}
			if !send(eventName(reset), next, Event{Text: text, Captured: captured}) {
				return
			}
			offset = next
		}
	}
}

func eventName(reset bool) string {
	if reset {
		return "reset"
	}
	return "append"
}

// ReadSSE consumes a Server-Sent Events body produced by ServeSSE and calls
// fn for each event with its name, id and payload. It returns nil at end of
// stream, fn's error when fn fails, or the read error. ctx cancels the read
// only through the body's owner (the caller closes it).
func ReadSSE(ctx context.Context, body interface{ ReadString(byte) (string, error) }, fn func(event string, id uint64, ev Event) error) error {
	var event string
	var id uint64
	var data string
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := body.ReadString('\n')
		if err != nil {
			if line == "" {
				return nil
			}
		}
		line = trimNewline(line)
		switch {
		case line == "":
			if event != "" {
				var ev Event
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					return err
				}
				if err := fn(event, id, ev); err != nil {
					return err
				}
			}
			event, data = "", ""
		case len(line) > 7 && line[:7] == "event: ":
			event = line[7:]
		case len(line) > 4 && line[:4] == "id: ":
			id, _ = strconv.ParseUint(line[4:], 10, 64)
		case len(line) > 6 && line[:6] == "data: ":
			data = line[6:]
		}
		if err != nil {
			return nil
		}
	}
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
