package main

import (
	"bytes"
	"syscall"
	"testing"
	"time"

	"github.com/osamu/localapp/internal/registry"
)

func TestTailLines(t *testing.T) {
	cases := []struct {
		text string
		n    int
		want string
	}{
		{"", 3, ""},
		{"a\nb\nc\n", 0, "a\nb\nc\n"},
		{"a\nb\nc\n", 2, "b\nc\n"},
		{"a\nb\nc\n", 5, "a\nb\nc\n"},
		{"a\nb\nc", 2, "b\nc"},
		{"a\nb\nc", 1, "c"},
		{"single", 1, "single"},
	}
	for _, c := range cases {
		if got := tailLines(c.text, c.n); got != c.want {
			t.Errorf("tailLines(%q, %d) = %q, want %q", c.text, c.n, got, c.want)
		}
	}
}

func TestLogcatPrintsWindow(t *testing.T) {
	store, logs := startLogDaemon(t)
	if _, err := store.Put("cat", registry.Service{Name: "web", Port: 65003}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := logcat("cat", "web", false, 0, &out); code != exitOK || out.Len() != 0 {
		t.Fatalf("empty: exit=%d out=%q", code, out.String())
	}
	for _, line := range []string{"one\n", "two\n", "three\n"} {
		logs.Append("cat", "web", line)
	}
	out.Reset()
	if code := logcat("cat", "web", false, 2, &out); code != exitOK || out.String() != "two\nthree\n" {
		t.Fatalf("tail: exit=%d out=%q", code, out.String())
	}
	out.Reset()
	if code := logcat("cat", "web", false, 0, &out); code != exitOK || out.String() != "one\ntwo\nthree\n" {
		t.Fatalf("all: exit=%d out=%q", code, out.String())
	}
	if code := logcat("nobody", "web", false, 0, &out); code != exitError {
		t.Fatalf("unregistered: exit=%d", code)
	}
}

func TestLogcatFollows(t *testing.T) {
	store, logs := startLogDaemon(t)
	if _, err := store.Put("cat", registry.Service{Name: "web", Port: 65003}); err != nil {
		t.Fatal(err)
	}
	logs.Append("cat", "web", "old1\nold2\n")
	pr, pw := pipeWriter(t)
	done := make(chan int, 1)
	go func() { done <- logcat("cat", "web", true, 1, pw) }()
	waitForOutput(t, pr, "old2\n")
	logs.Append("cat", "web", "new\n")
	waitForOutput(t, pr, "old2\nnew\n")
	syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow did not stop on SIGINT")
	}
}

func TestLogcatUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"a", "b"}, {"/web"}, {"-n", "-1", "app"}} {
		if code := cmdLogcat(args); code != exitUsage {
			t.Fatalf("%v: exit=%d", args, code)
		}
	}
}

// syncBuffer is a buffer the follower writes to while the test reads it.
type syncBuffer struct {
	mu  chan struct{}
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu <- struct{}{}
	defer func() { <-b.mu }()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu <- struct{}{}
	defer func() { <-b.mu }()
	return b.buf.String()
}

func pipeWriter(t *testing.T) (*syncBuffer, *syncBuffer) {
	b := &syncBuffer{mu: make(chan struct{}, 1)}
	return b, b
}

func waitForOutput(t *testing.T, b *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b.String() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("output %q, want %q", b.String(), want)
}
