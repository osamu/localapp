// Package logstream keeps bounded, ephemeral application output.
package logstream

import (
	"strings"
	"sync"
	"time"
)

const MaxBytes = 256 * 1024
const MaxServices = 64
const Retention = 5 * time.Minute

// Chunk is a batch of output with its daemon-receipt expiry time.
type Chunk struct {
	Text      string `json:"text"`
	ExpiresAt int64  `json:"expiresAt"`
}

type entry struct {
	chunks []Chunk
	size   int
	used   uint64
}

func (e *entry) prune(now int64) {
	for len(e.chunks) > 0 && e.chunks[0].ExpiresAt <= now {
		e.size -= len(e.chunks[0].Text)
		e.chunks[0] = Chunk{}
		e.chunks = e.chunks[1:]
	}
}

type Store struct {
	mu      sync.Mutex
	entries map[string]entry
	clock   uint64
	now     func() time.Time // nil uses the wall clock; tests can advance time.
}

func (s *Store) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Store) Append(app, service, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]entry)
	}
	now := s.currentTime()
	// Expire idle services too whenever output arrives.
	for key, e := range s.entries {
		e.prune(now.UnixMilli())
		s.entries[key] = e
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
	if len(text) > MaxBytes {
		text = strings.Clone(text[len(text)-MaxBytes:])
	}
	if text != "" {
		e.chunks = append(e.chunks, Chunk{Text: text, ExpiresAt: now.Add(Retention).UnixMilli()})
		e.size += len(text)
	}
	for e.size > MaxBytes {
		excess := e.size - MaxBytes
		if len(e.chunks[0].Text) <= excess {
			e.size -= len(e.chunks[0].Text)
			e.chunks[0] = Chunk{}
			e.chunks = e.chunks[1:]
		} else {
			e.chunks[0].Text = strings.Clone(e.chunks[0].Text[excess:])
			e.size -= excess
		}
	}
	s.clock++
	e.used = s.clock
	s.entries[key] = e
}

// Chunks expires old output even when no new output is being appended.
func (s *Store) Chunks(app, service string) ([]Chunk, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := app + "/" + service
	e, ok := s.entries[key]
	if !ok {
		return []Chunk{}, false
	}
	e.prune(s.currentTime().UnixMilli())
	s.entries[key] = e
	return append([]Chunk{}, e.chunks...), true
}

func (s *Store) Snapshot(app, service string) (string, bool) {
	chunks, ok := s.Chunks(app, service)
	var text strings.Builder
	for _, chunk := range chunks {
		text.WriteString(chunk.Text)
	}
	return text.String(), ok
}
