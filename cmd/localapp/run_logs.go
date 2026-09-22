package main

import (
	"context"
	"sync"
	"time"

	"github.com/osamu/localapp/internal/control"
	"github.com/osamu/localapp/internal/logstream"
)

// runLogWriter never waits on the daemon from Write, so log preview cannot
// stall a child's output. Close allows one bounded final upload.
type runLogWriter struct {
	mu      sync.Mutex
	pending []byte
	dropped bool
	stop    chan struct{}
	done    chan struct{}
}

func newRunLogWriter(client *control.Client, app, service string) *runLogWriter {
	w := &runLogWriter{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		send := func() {
			w.mu.Lock()
			text := string(w.pending)
			if w.dropped {
				text = "\n[localapp: output omitted while log upload was behind]\n" + text
			}
			w.pending = nil
			w.dropped = false
			w.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := client.AppendLogs(ctx, app, service, text); err != nil {
				w.mu.Lock()
				w.dropped = true
				w.mu.Unlock()
			}
		}
		for {
			select {
			case <-ticker.C:
				send()
			case <-w.stop:
				send()
				return
			}
		}
	}()
	return w
}

func (w *runLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if len(p) >= logstream.MaxBytes {
		w.pending = append(w.pending[:0], p[len(p)-logstream.MaxBytes:]...)
		w.dropped = true
	} else {
		if len(w.pending)+len(p) > logstream.MaxBytes {
			w.pending = w.pending[len(w.pending)+len(p)-logstream.MaxBytes:]
			w.dropped = true
		}
		w.pending = append(w.pending, p...)
	}
	return n, nil
}

func (w *runLogWriter) Close() { close(w.stop); <-w.done }
