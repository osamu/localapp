package registry

import (
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// livePID returns the pid of a running process. It is stopped when the test
// finishes.
func livePID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting dummy process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

// deadPID returns the pid of a process that has exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running dummy process: %v", err)
	}
	return cmd.Process.Pid
}

func TestProcessAlive(t *testing.T) {
	if !ProcessAlive(0) {
		t.Error("pid=0 (nothing to watch) should count as alive")
	}
	if !ProcessAlive(-1) {
		t.Error("a negative pid means nothing to watch and should count as alive")
	}
	if !ProcessAlive(os.Getpid()) {
		t.Error("the current process is not reported as alive")
	}
	if !ProcessAlive(livePID(t)) {
		t.Error("a running child process is not reported as alive")
	}
	if ProcessAlive(deadPID(t)) {
		t.Error("an exited process was reported as alive")
	}
	// pid 1 (launchd / init) is owned by another user but exists (EPERM counts
	// as alive).
	if !ProcessAlive(1) {
		t.Error("pid=1 is not reported as alive")
	}
}

// listenPort listens on a free port and returns its port number.
func listenPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("starting listener: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func TestServiceStatus(t *testing.T) {
	open := listenPort(t)
	// A port whose listener was just closed (cannot be connected to).
	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := closedLn.Addr().(*net.TCPAddr).Port
	closedLn.Close()

	timeout := 200 * time.Millisecond
	tests := []struct {
		name string
		svc  Service
		want string
	}{
		{"no pid, listening", Service{Port: open}, StatusUp},
		{"no pid, not listening", Service{Port: closed}, StatusDown},
		{"pid alive, listening", Service{Port: open, PID: os.Getpid()}, StatusUp},
		{"pid alive, not listening", Service{Port: closed, PID: os.Getpid()}, StatusDown},
		{"pid exited, listening", Service{Port: open, PID: deadPID(t)}, StatusDown},
		{"pid exited, not listening", Service{Port: closed, PID: deadPID(t)}, StatusDown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ServiceStatus(tt.svc, timeout); got != tt.want {
				t.Errorf("ServiceStatus = %q, want %q", got, tt.want)
			}
		})
	}
}
