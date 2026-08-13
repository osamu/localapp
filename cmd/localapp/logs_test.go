package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLastLines(t *testing.T) {
	tests := []struct {
		name string
		data string
		n    int
		want string
	}{
		{"last 2 lines", "a\nb\nc\nd\n", 2, "c\nd\n"},
		{"more lines requested than present", "a\nb\n", 10, "a\nb\n"},
		{"n=0 means everything", "a\nb\n", 0, "a\nb\n"},
		{"a negative n means everything", "a\nb\n", -1, "a\nb\n"},
		{"no trailing newline", "a\nb\nc", 2, "b\nc"},
		{"a single line", "only\n", 1, "only\n"},
		{"empty", "", 5, ""},
		{"contains a blank line", "a\n\nb\n", 2, "\nb\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(lastLines([]byte(tt.data), tt.n)); got != tt.want {
				t.Errorf("lastLines(%q, %d) = %q, want %q", tt.data, tt.n, got, tt.want)
			}
		})
	}
}

func TestWriteTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	content := "l1\nl2\nl3\nl4\nl5\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var buf bytes.Buffer
	offset, err := writeTail(f, &buf, 2)
	if err != nil {
		t.Fatalf("writeTail: %v", err)
	}
	if got := buf.String(); got != "l4\nl5\n" {
		t.Errorf("output = %q, want %q", got, "l4\nl5\n")
	}
	if offset != int64(len(content)) {
		t.Errorf("offset = %d, want %d", offset, len(content))
	}
}

// The requested number of lines is returned even across the 64KB chunk
// boundary.
func TestWriteTailLargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	var sb strings.Builder
	for i := 0; i < 20000; i++ {
		sb.WriteString("line of daemon log output\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var buf bytes.Buffer
	if _, err := writeTail(f, &buf, 3); err != nil {
		t.Fatalf("writeTail: %v", err)
	}
	if got := strings.Count(buf.String(), "\n"); got != 3 {
		t.Errorf("line count = %d, want 3", got)
	}
}

// -f keeps streaming appended output and returns when ctx is cancelled.
func TestFollowFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- followFile(ctx, path, &lockedWriter{mu: &mu, w: &out}, int64(len("first\n")), 10*time.Millisecond)
	}()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		got := out.String()
		mu.Unlock()
		if got == "second\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("appended output does not arrive: %q", got)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("followFile: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("it does not return on cancellation")
	}
}

// After a rotation (truncation) it reads from the beginning again.
func TestFollowFileHandlesTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte("old content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	offset, err := drain(path, &buf, int64(len("old content\n")), make([]byte, 1024))
	if err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("already-read content was printed again: %q", buf.String())
	}
	if err := os.WriteFile(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := drain(path, &buf, offset, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "new\n" {
		t.Errorf("output = %q, want %q", got, "new\n")
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
