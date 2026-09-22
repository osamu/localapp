package logstream

import (
	"fmt"
	"strings"
	"testing"
)

func TestRetention(t *testing.T) {
	s := &Store{}
	for i := 1; i <= MaxLines+5; i++ {
		s.Append("app", "web", fmt.Sprintf("line %d\n", i))
	}
	text, _ := s.Snapshot("app", "web")
	if !strings.HasPrefix(text, "line 6\n") || strings.Count(text, "\n") != MaxLines {
		t.Fatalf("line window: %.20q, %d lines", text, strings.Count(text, "\n"))
	}
	line := strings.Repeat("y", 1000) + "\n"
	s.Append("app", "api", strings.Repeat(line, 300)) // 300 lines, ~293 KiB
	if text, _ = s.Snapshot("app", "api"); len(text) > MaxBytes || len(text)%len(line) != 0 {
		t.Fatalf("byte cap must cut on a line boundary: len=%d", len(text))
	}
	for i := 0; i < MaxServices; i++ {
		s.Append(fmt.Sprint(i), "web", "x")
	}
	if _, ok := s.Snapshot("app", "web"); ok {
		t.Fatal("oldest service not evicted")
	}
}

func TestFollow(t *testing.T) {
	s := &Store{}
	wake, cancel := s.Subscribe("app", "web")
	defer cancel()
	s.Append("app", "web", "one\n")
	s.Append("app", "web", "two\n") // subscriber never drains; must not block
	text, next, _, reset := s.ReadFrom("app", "web", 0)
	if text != "one\ntwo\n" || next != 8 || reset {
		t.Fatalf("window: %q %d %v", text, next, reset)
	}
	s.Append("app", "web", "three\n")
	if text, next, _, reset = s.ReadFrom("app", "web", next); text != "three\n" || next != 14 || reset {
		t.Fatalf("delta: %q %d %v", text, next, reset)
	}
	if _, _, _, reset = s.ReadFrom("app", "web", 99); !reset {
		t.Fatal("offset outside the window must reset")
	}
	select {
	case <-wake:
	default:
		t.Fatal("subscriber not woken")
	}
}
