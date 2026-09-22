// Package logstream keeps bounded, ephemeral application output and lets
// readers follow it incrementally.
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

// entry is the retained output of one service. start is the absolute offset
// of buf[0]: offsets grow monotonically over everything ever appended, so a
// reader can resume after a trim or a reconnect.
type entry struct {
	buf   []byte
	start uint64
	used  uint64
}

func (e *entry) end() uint64 { return e.start + uint64(len(e.buf)) }

// trim keeps the last MaxLines lines, then enforces MaxBytes. The byte cut
// moves to the next line boundary when one exists so the retained text starts
// on a whole line.
func (e *entry) trim() {
	cut := 0
	lines := bytes.Count(e.buf, []byte{'\n'})
	if len(e.buf) > 0 && e.buf[len(e.buf)-1] != '\n' {
		lines++ // trailing partial line
	}
	for i := 0; i < lines-MaxLines; i++ {
		cut += bytes.IndexByte(e.buf[cut:], '\n') + 1
	}
	if len(e.buf)-cut > MaxBytes {
		cut = len(e.buf) - MaxBytes
		if i := bytes.IndexByte(e.buf[cut:], '\n'); i >= 0 && cut+i+1 < len(e.buf) {
			cut += i + 1
		}
	}
	if cut == 0 {
		return
	}
	// Copy so the dropped prefix is released to the GC.
	e.buf = append([]byte(nil), e.buf[cut:]...)
	e.start += uint64(cut)
}

type Store struct {
	mu      sync.Mutex
	entries map[string]*entry
	subs    map[string]map[chan struct{}]struct{}
	clock   uint64
}

// Append adds output and wakes subscribers. It never waits on a subscriber.
func (s *Store) Append(app, service, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]*entry)
	}
	key := app + "/" + service
	e, exists := s.entries[key]
	if !exists {
		if len(s.entries) >= MaxServices {
			var oldest string
			var age uint64 = ^uint64(0)
			for k, v := range s.entries {
				if v.used < age {
					oldest, age = k, v.used
				}
			}
			delete(s.entries, oldest)
		}
		e = &entry{}
		s.entries[key] = e
	}
	// A request body is capped at 1 MiB by the control server, so a single
	// Append cannot hold more than that before trim runs.
	e.buf = append(e.buf, text...)
	e.trim()
	s.clock++
	e.used = s.clock
	for ch := range s.subs[key] {
		select {
		case ch <- struct{}{}:
		default: // already signalled; the subscriber will catch up
		}
	}
}

// Snapshot returns the retained output and whether the service has forwarded
// output since the daemon started.
func (s *Store) Snapshot(app, service string) (string, bool) {
	text, _, captured, _ := s.ReadFrom(app, service, 0)
	return text, captured
}

// ReadFrom returns the output appended since offset (as returned by a
// previous call) and the offset to continue from. reset is true when offset
// is not inside the retained window — first read, output trimmed away, or a
// daemon restart — in which case text is the whole retained window and the
// reader must replace, not append. captured reports whether the service has
// forwarded output since the daemon started.
func (s *Store) ReadFrom(app, service string, offset uint64) (text string, next uint64, captured, reset bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[app+"/"+service]
	if !ok {
		return "", 0, false, offset != 0
	}
	if offset < e.start || offset > e.end() {
		return string(e.buf), e.end(), true, true
	}
	return string(e.buf[offset-e.start:]), e.end(), true, false
}

// Subscribe returns a channel that receives a value after each Append to the
// service (coalesced: at most one pending signal). The service need not have
// output yet. cancel must be called when the subscriber is done.
func (s *Store) Subscribe(app, service string) (wake <-chan struct{}, cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subs == nil {
		s.subs = make(map[string]map[chan struct{}]struct{})
	}
	key := app + "/" + service
	if s.subs[key] == nil {
		s.subs[key] = make(map[chan struct{}]struct{})
	}
	ch := make(chan struct{}, 1)
	s.subs[key][ch] = struct{}{}
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subs[key], ch)
		if len(s.subs[key]) == 0 {
			delete(s.subs, key)
		}
	}
}

// Subscribers reports how many readers follow the service (tests).
func (s *Store) Subscribers(app, service string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs[app+"/"+service])
}
