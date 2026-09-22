// Package logstream keeps bounded, ephemeral application output.
package logstream

import (
	"bytes"
	"sync"
)

// MaxLines is the number of trailing lines kept per service.
const MaxLines = 1000

// MaxBytes is a hard cap per service so a few very long lines cannot grow
// memory without bound.
const MaxBytes = 256 * 1024

// MaxServices is the number of recently active services kept in memory.
const MaxServices = 64

type entry struct {
	buf  []byte
	used uint64
}

// trim keeps the last MaxLines lines, then enforces MaxBytes. The byte cut
// moves to the next line boundary when one exists so the retained text starts
// on a whole line.
func (e *entry) trim() {
	lines := bytes.Count(e.buf, []byte{'\n'})
	if len(e.buf) > 0 && e.buf[len(e.buf)-1] != '\n' {
		lines++ // trailing partial line
	}
	if lines > MaxLines {
		drop := lines - MaxLines
		cut := 0
		for i := 0; i < drop; i++ {
			cut += bytes.IndexByte(e.buf[cut:], '\n') + 1
		}
		e.buf = e.buf[cut:]
	}
	if len(e.buf) > MaxBytes {
		e.buf = e.buf[len(e.buf)-MaxBytes:]
		if i := bytes.IndexByte(e.buf, '\n'); i >= 0 && i+1 < len(e.buf) {
			e.buf = e.buf[i+1:]
		}
	}
	// A request body is capped at 1 MiB by the control server, so a single
	// Append cannot hold more than that before trim runs.
	// Copy so dropped prefixes are released to the GC.
	e.buf = append([]byte(nil), e.buf...)
}

type Store struct {
	mu      sync.Mutex
	entries map[string]entry
	clock   uint64
}

func (s *Store) Append(app, service, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]entry)
	}
	key := app + "/" + service
	e, exists := s.entries[key]
	if !exists && len(s.entries) >= MaxServices {
		var oldest string
		var age uint64 = ^uint64(0)
		for k, v := range s.entries {
			if v.used < age {
				oldest, age = k, v.used
			}
		}
		delete(s.entries, oldest)
	}
	e.buf = append(e.buf, text...)
	e.trim()
	s.clock++
	e.used = s.clock
	s.entries[key] = e
}

// Snapshot returns the retained output and whether the service has ever
// uploaded output since the daemon started.
func (s *Store) Snapshot(app, service string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[app+"/"+service]
	return string(e.buf), ok
}
