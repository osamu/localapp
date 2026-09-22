package logstream

import (
	"fmt"
	"strings"
	"sync"
	"testing"
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

func TestKeepsLastLines(t *testing.T) {
	s := &Store{}
	for i := 1; i <= MaxLines+5; i++ {
		s.Append("app", "web", fmt.Sprintf("line %d\n", i))
	}
	text, _ := s.Snapshot("app", "web")
	if !strings.HasPrefix(text, "line 6\n") || !strings.HasSuffix(text, fmt.Sprintf("line %d\n", MaxLines+5)) {
		t.Fatalf("wrong window: %.40q ... %.40q", text, text[len(text)-40:])
	}
	if got := strings.Count(text, "\n"); got != MaxLines {
		t.Fatalf("kept %d lines", got)
	}
}

func TestPartialLineCountsAndSplitLinesJoin(t *testing.T) {
	s := &Store{}
	s.Append("app", "web", "a\n")
	s.Append("app", "web", "partial")
	s.Append("app", "web", " rest\n")
	if text, _ := s.Snapshot("app", "web"); text != "a\npartial rest\n" {
		t.Fatalf("batches not joined: %q", text)
	}
	s = &Store{}
	s.Append("app", "web", strings.Repeat("x\n", MaxLines)+"tail")
	text, _ := s.Snapshot("app", "web")
	if strings.Count(text, "\n") != MaxLines-1 || !strings.HasSuffix(text, "tail") {
		t.Fatalf("trailing partial line not counted: %d lines", strings.Count(text, "\n"))
	}
}

func TestByteCapStartsOnLineBoundary(t *testing.T) {
	s := &Store{}
	line := strings.Repeat("y", 1000) + "\n"
	s.Append("app", "web", strings.Repeat(line, 300)) // 300 lines, ~293 KiB
	text, _ := s.Snapshot("app", "web")
	if len(text) > MaxBytes || !strings.HasPrefix(text, "y") || len(text)%len(line) != 0 {
		t.Fatalf("byte cap left a partial first line: len=%d", len(text))
	}
	s = &Store{}
	s.Append("app", "web", strings.Repeat("z", MaxBytes+10))
	if text, _ := s.Snapshot("app", "web"); len(text) != MaxBytes {
		t.Fatalf("single huge line not capped: %d", len(text))
	}
}

func TestReadFromFollowsAndResets(t *testing.T) {
	s := &Store{}
	if text, next, captured, reset := s.ReadFrom("app", "web", 0); text != "" || next != 0 || captured || reset {
		t.Fatalf("empty store: %q %d %v %v", text, next, captured, reset)
	}
	if _, _, _, reset := s.ReadFrom("app", "web", 7); !reset {
		t.Fatal("stale offset on an unknown service must reset")
	}
	s.Append("app", "web", "one\n")
	text, next, _, reset := s.ReadFrom("app", "web", 0)
	if text != "one\n" || next != 4 || reset {
		t.Fatalf("first read inside the window must not reset: %q %d %v", text, next, reset)
	}
	s.Append("app", "web", "two\n")
	text, next, _, reset = s.ReadFrom("app", "web", next)
	if text != "two\n" || next != 8 || reset {
		t.Fatalf("delta: %q %d %v", text, next, reset)
	}
	if text, n, _, reset := s.ReadFrom("app", "web", next); text != "" || n != next || reset {
		t.Fatalf("no new output: %q %d %v", text, n, reset)
	}
	// Fill past the window so offset 0 is trimmed away.
	for i := 0; i < MaxLines; i++ {
		s.Append("app", "web", "x\n")
	}
	text, n, _, reset := s.ReadFrom("app", "web", 0)
	if !reset || strings.Count(text, "\n") != MaxLines || strings.Contains(text, "one") {
		t.Fatalf("trimmed offset: reset=%v lines=%d", reset, strings.Count(text, "\n"))
	}
	if _, _, _, reset := s.ReadFrom("app", "web", n+1); !reset {
		t.Fatal("offset beyond end must reset")
	}
}

func TestSubscribeNeverBlocksAppend(t *testing.T) {
	s := &Store{}
	wake, cancel := s.Subscribe("app", "web")
	for i := 0; i < 10; i++ {
		s.Append("app", "web", "x\n") // subscriber never drains; must not block
	}
	select {
	case <-wake:
	default:
		t.Fatal("no wake-up")
	}
	select {
	case <-wake:
		t.Fatal("signals must coalesce")
	default:
	}
	if s.Subscribers("app", "web") != 1 {
		t.Fatal("subscriber not registered")
	}
	cancel()
	if s.Subscribers("app", "web") != 0 {
		t.Fatal("subscriber not removed")
	}
	s.Append("app", "web", "after\n")
	select {
	case <-wake:
		t.Fatal("cancelled subscriber woken")
	default:
	}
}
