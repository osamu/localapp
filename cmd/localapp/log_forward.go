package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/osamu/localapp/internal/control"
	"github.com/osamu/localapp/internal/logstream"
)

// forwardInterval is how often queued output is sent to the daemon.
const forwardInterval = 300 * time.Millisecond

// logForwarder queues bytes and forwards them to the daemon's log stream over the control socket. Write never waits on
// the daemon, so a slow or absent daemon cannot stall the producer. Close
// allows one bounded final send. It is shared by `run` and `logforward`.
type logForwarder struct {
	mu      sync.Mutex
	pending []byte
	dropped bool
	stop    chan struct{}
	done    chan struct{}
}

// newLogForwarder starts the forwarding loop. warn, when non-nil, is called once
// each time forwarding starts failing or recovers, with a one-line message.
// failing seeds that state: a caller that already reported a problem passes
// true so the first failed send is not reported twice.
func newLogForwarder(client *control.Client, app, service string, warn func(string), failing bool) *logForwarder {
	w := &logForwarder{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(forwardInterval)
		defer ticker.Stop()
		send := func() {
			w.mu.Lock()
			if len(w.pending) == 0 && !w.dropped {
				w.mu.Unlock()
				return
			}
			text := string(w.pending)
			if w.dropped {
				text = "\n[localapp: output omitted while log forwarding was behind]\n" + text
			}
			w.pending = nil
			w.dropped = false
			w.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := client.AppendLogs(ctx, app, service, text)
			if err != nil {
				w.mu.Lock()
				w.dropped = true
				w.mu.Unlock()
				if !failing && warn != nil {
					warn(forwardFailureMessage(err, app, service))
				}
				failing = true
				return
			}
			if failing && warn != nil {
				warn("log forwarding to " + app + "/" + service + " resumed")
			}
			failing = false
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

// forwardFailureMessage explains why output is not being captured.
func forwardFailureMessage(err error, app, service string) string {
	var apiErr *control.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Code == control.CodeNotFound:
		return app + "/" + service + " is not registered; output is shown but not captured until it is (localapp add <port> --app " + app + " --service " + service + ")"
	case errors.Is(err, control.ErrUnavailable):
		return "cannot reach the daemon; output is shown but not captured (" + err.Error() + ")"
	default:
		return "log forwarding failed; output is shown but not captured (" + err.Error() + ")"
	}
}

func (w *logForwarder) Write(p []byte) (int, error) {
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

func (w *logForwarder) Close() { close(w.stop); <-w.done }
