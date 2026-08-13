package main

import (
	"net"
	"strconv"
	"testing"
)

// allocatePort returns a bindable loopback port.
func TestAllocatePort(t *testing.T) {
	port, err := allocatePort()
	if err != nil {
		t.Fatal(err)
	}
	if port <= 0 || port > 65535 {
		t.Fatalf("allocatePort = %d, want 1-65535", port)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("the allocated port is not bindable: %v", err)
	}
	ln.Close()
}

// run requires a command.
func TestRunRequiresCommand(t *testing.T) {
	if code := cmdRun(nil); code != exitUsage {
		t.Errorf("cmdRun() = %d, want %d", code, exitUsage)
	}
}

// The command runs with PORT injected, the service is registered with the
// child PID, and the exit code is passed through.
func TestRunInjectsPortAndRegisters(t *testing.T) {
	store := startTestDaemon(t)

	// Exits 7 only when PORT holds a positive number.
	code := cmdRun([]string{"--app", "runapp", "--", "sh", "-c", `[ "$PORT" -gt 0 ] && exit 7`})
	if code != 7 {
		t.Fatalf("cmdRun = %d, want the child's exit code 7", code)
	}

	apps := store.Apps()
	if len(apps) != 1 || apps[0].Name != "runapp" {
		t.Fatalf("registered apps = %+v, want one app 'runapp'", apps)
	}
	svc := apps[0].Services[0]
	if svc.Port <= 0 {
		t.Errorf("registered port = %d, want > 0", svc.Port)
	}
	if svc.PID <= 0 {
		t.Errorf("registered pid = %d, want the child pid", svc.PID)
	}
}

// A failure to start the command is an error, and the registration created
// before the spawn stays (registrations persist by design).
func TestRunCommandNotFound(t *testing.T) {
	startTestDaemon(t)
	if code := cmdRun([]string{"--app", "ghost", "--", "/nonexistent-command-xyz"}); code != exitError {
		t.Errorf("cmdRun = %d, want %d", code, exitError)
	}
}

// run fails fast when the daemon is unreachable, before running the command.
func TestRunFailsWithoutDaemon(t *testing.T) {
	t.Setenv("LOCALAPP_SOCKET", "/nonexistent/control.sock")
	if code := cmdRun([]string{"--app", "x", "--", "sh", "-c", "exit 0"}); code != exitError {
		t.Errorf("cmdRun = %d, want %d", code, exitError)
	}
}
