package registry

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// ProcessAlive reports whether the process with the given pid exists.
// pid <= 0 means "nothing to watch" and always returns true.
//
// Existence is checked by sending signal 0. A process owned by another user
// returns EPERM, which still means the process exists, so it counts as alive.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

// ServiceStatus determines the liveness of a service
// (DESIGN.md "登録ライフサイクル").
//
// When `--pid` is given, process liveness is checked first and a terminated
// process means down. Even for a live process, the service is down when the
// target port does not accept a TCP connection. Services without a pid are
// judged by the port alone.
func ServiceStatus(svc Service, timeout time.Duration) string {
	if !ProcessAlive(svc.PID) {
		return StatusDown
	}
	return Status(svc.Port, timeout)
}
