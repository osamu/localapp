package logstream

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBoundedAndIsolated(t *testing.T) {
	s := &Store{}
	s.Append("app", "web", strings.Repeat("a", MaxBytes)+"last")
	s.Append("app", "api", "other")
	text, ok := s.Snapshot("app", "web")
	if !ok || len(text) != MaxBytes || !strings.HasSuffix(text, "last") {
		t.Fatal("tail not bounded")
	}
	if text, _ := s.Snapshot("app", "api"); text != "other" {
		t.Fatal("mixed services")
	}
	for i := 0; i < MaxServices; i++ {
		s.Append(fmt.Sprint(i), "web", "x")
	}
	if _, ok := s.Snapshot("app", "web"); ok {
		t.Fatal("old stream not evicted")
	}
}

func TestConcurrent(t *testing.T) {
	s := &Store{}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.Append("app", "web", "x")
				s.Snapshot("app", "web")
			}
		}()
	}
	wg.Wait()
	if text, _ := s.Snapshot("app", "web"); len(text) != 1000 {
		t.Fatal("lost output")
	}
}

func TestRetention(t *testing.T) {
	now := time.Unix(1000, 0)
	s := &Store{now: func() time.Time { return now }}
	s.Append("app", "web", "old\n")
	now = now.Add(time.Minute)
	s.Append("app", "web", "recent\n")
	now = now.Add(Retention - time.Minute - time.Millisecond)
	if text, _ := s.Snapshot("app", "web"); text != "old\nrecent\n" {
		t.Fatalf("expired too soon: %q", text)
	}
	now = now.Add(time.Millisecond)
	if text, _ := s.Snapshot("app", "web"); text != "recent\n" {
		t.Fatalf("expiry boundary: %q", text)
	}
	now = now.Add(time.Minute)
	if text, ok := s.Snapshot("app", "web"); text != "" || !ok {
		t.Fatalf("idle stream: text=%q captured=%v", text, ok)
	}
	s.Append("app", "web", "new\n")
	if text, _ := s.Snapshot("app", "web"); text != "new\n" {
		t.Fatalf("old output restored: %q", text)
	}
}

func TestChunksAreCopiedAndByteBounded(t *testing.T) {
	s := &Store{}
	s.Append("app", "web", strings.Repeat("x", MaxBytes-1))
	s.Append("app", "web", "last")
	chunks, _ := s.Chunks("app", "web")
	if chunks[0].ExpiresAt == 0 {
		t.Fatal("expiry missing")
	}
	chunks[0].Text = "changed"
	text, _ := s.Snapshot("app", "web")
	if len(text) != MaxBytes || !strings.HasSuffix(text, "last") {
		t.Fatal("snapshot mutated or byte limit broken")
	}
}
