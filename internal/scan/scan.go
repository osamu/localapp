// Package scan enumerates listening TCP ports and picks out the unregistered
// ones as registration candidates (DESIGN.md "CLI", `localapp scan`).
//
// Enumeration uses `lsof`. Only sockets listening on the IPv4 loopback
// (`127.0.0.1`) or on all interfaces (`*` / `0.0.0.0`) are considered: the
// former is the default of dev servers, and the latter is reachable through
// loopback as well. Sockets listening on IPv6 only are excluded, because the
// proxy always forwards to `127.0.0.1`.
package scan

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// Listener is one listening socket.
type Listener struct {
	// Port is the port being listened on.
	Port int `json:"port"`
	// PID is the ID of the listening process.
	PID int `json:"pid"`
	// Command is the process name (the COMMAND column of lsof).
	Command string `json:"command"`
	// Address is the listening address ("127.0.0.1" or "*").
	Address string `json:"address"`
}

// ErrLsofMissing reports that lsof could not be found.
var ErrLsofMissing = errors.New("lsof not found")

// Listeners runs lsof and enumerates the listening IPv4 sockets.
func Listeners(ctx context.Context) ([]Listener, error) {
	path, err := exec.LookPath("lsof")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLsofMissing, err)
	}
	// -F pcn: print the process ID, command name and socket name in field form.
	cmd := exec.CommandContext(ctx, path, "-nP", "-iTCP", "-sTCP:LISTEN", "-F", "pcn")
	out, err := cmd.Output()
	// lsof exits with 1 when no socket matches. Empty output is not an error.
	if err != nil && len(out) == 0 {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("running lsof: %w", err)
	}
	return ParseLsof(string(out)), nil
}

// ParseLsof parses the output of `lsof -F pcn`.
//
// The output holds one field per line, the first character being the field
// type. A `p` (process ID) and a `c` (command name) apply to every following
// `n` (socket name). Lines that cannot be parsed are ignored.
func ParseLsof(out string) []Listener {
	var (
		list    []Listener
		pid     int
		command string
	)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 2 {
			continue
		}
		value := line[1:]
		switch line[0] {
		case 'p':
			n, err := strconv.Atoi(value)
			if err != nil {
				pid = 0
				continue
			}
			pid, command = n, ""
		case 'c':
			command = value
		case 'n':
			addr, port, ok := parseSocketName(value)
			if !ok || pid == 0 {
				continue
			}
			list = append(list, Listener{Port: port, PID: pid, Command: command, Address: addr})
		}
	}
	return list
}

// parseSocketName splits the n field of lsof (for example "127.0.0.1:3000" or
// "*:8080") into an address and a port. Anything other than the IPv4 loopback
// and all interfaces yields ok=false.
func parseSocketName(name string) (addr string, port int, ok bool) {
	i := strings.LastIndex(name, ":")
	if i < 0 {
		return "", 0, false
	}
	addr, rawPort := name[:i], name[i+1:]
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, false
	}
	switch addr {
	case "127.0.0.1", "*", "0.0.0.0":
		return addr, port, true
	default:
		// IPv6 notation such as [::1] / [::], and addresses other than
		// 127.0.0.1.
		return "", 0, false
	}
}

// Filter removes the excluded ports and returns the candidates sorted by port
// number. Several sockets found on the same port are collapsed into one entry.
func Filter(list []Listener, exclude map[int]bool) []Listener {
	seen := make(map[int]bool, len(list))
	out := make([]Listener, 0, len(list))
	for _, l := range list {
		if exclude[l.Port] || seen[l.Port] {
			continue
		}
		seen[l.Port] = true
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// PortOf extracts the port number from an address such as "127.0.0.1:15353".
// It returns 0 when the address cannot be parsed. It is used to exclude the
// daemon's own listeners.
func PortOf(addr string) int {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0
	}
	port, err := strconv.Atoi(addr[i+1:])
	if err != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}
