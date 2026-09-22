package main

import (
	"bytes"
	"io"
	"syscall"
	"testing"
	"time"

	"github.com/osamu/localapp/internal/registry"
)

func TestTailLines(t *testing.T) {
	for _, c := range []struct {
		text string
		n    int
		want string
	}{{"a\nb\nc\n", 0, "a\nb\nc\n"}, {"a\nb\nc\n", 2, "b\nc\n"}, {"a\nb\nc", 5, "a\nb\nc"}} {
		if got := tailLines(c.text, c.n); got != c.want {
			t.Errorf("tailLines(%q, %d) = %q", c.text, c.n, got)
		}
	}
}

func TestLogcat(t *testing.T) {
	store, logs := startLogDaemon(t)
	store.Put("cat", registry.Service{Name: "web", Port: 65003})
	logs.Append("cat", "web", "one\ntwo\n")
	var out bytes.Buffer
	if code := logcat("cat", "web", false, 1, &out); code != exitOK || out.String() != "two\n" {
		t.Fatalf("tail: exit=%d out=%q", code, out.String())
	}
	if code := logcat("nobody", "web", false, 0, &out); code != exitError {
		t.Fatalf("unregistered: exit=%d", code)
	}

	pr, pw := io.Pipe()
	done := make(chan int, 1)
	go func() { done <- logcat("cat", "web", true, 1, pw) }()
	expect := func(want string) {
		t.Helper()
		buf := make([]byte, len(want))
		if _, err := io.ReadFull(pr, buf); err != nil || string(buf) != want {
			t.Fatalf("follow: %q %v", buf, err)
		}
	}
	expect("two\n")
	logs.Append("cat", "web", "new\n")
	expect("new\n")
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

func TestLogTargetUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"a", "b"}, {"/web"}, {"app/"}} {
		if code := cmdLogcat(args); code != exitUsage {
			t.Fatalf("logcat %v: exit=%d", args, code)
		}
	}
	if code := cmdLogForward([]string{"app/x/y"}); code != exitUsage {
		t.Fatal("logforward accepted a bad target")
	}
}
