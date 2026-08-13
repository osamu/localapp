package scan

import (
	"reflect"
	"testing"
)

// sample mimics the real output of
// lsof -nP -iTCP -sTCP:LISTEN -F pcn. It contains IPv4, all-interfaces, IPv6
// and duplicate entries.
const sample = `p1016
crapportd
f10
n*:60397
f11
n*:60397
p4410
cadb
f8
n127.0.0.1:5037
p6996
cnode
f3
n[::1]:5173
f4
n127.0.0.1:5173
p7000
cdocker
f5
n0.0.0.0:8080
f6
n[::]:8080
p7100
cbroken
f7
nno-port-here
`

func TestParseLsof(t *testing.T) {
	got := ParseLsof(sample)
	want := []Listener{
		{Port: 60397, PID: 1016, Command: "rapportd", Address: "*"},
		{Port: 60397, PID: 1016, Command: "rapportd", Address: "*"},
		{Port: 5037, PID: 4410, Command: "adb", Address: "127.0.0.1"},
		{Port: 5173, PID: 6996, Command: "node", Address: "127.0.0.1"},
		{Port: 8080, PID: 7000, Command: "docker", Address: "0.0.0.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLsof =\n%+v\nwant\n%+v", got, want)
	}
}

// TestParseLsofSkipsIPv6 checks that IPv6-only sockets are not candidates,
// because the proxy always forwards to 127.0.0.1
// (DESIGN.md "プロキシ実装要件").
func TestParseLsofSkipsIPv6(t *testing.T) {
	for _, in := range []string{"p1\nca\nn[::1]:3000\n", "p1\nca\nn[::]:3000\n", "p1\nca\nn[fe80::1]:3000\n"} {
		if got := ParseLsof(in); len(got) != 0 {
			t.Errorf("ParseLsof(%q) = %+v, want empty", in, got)
		}
	}
}

// TestParseLsofSkipsNonLoopbackIPv4 checks that concrete addresses other than
// 127.0.0.1 are not candidates.
func TestParseLsofSkipsNonLoopbackIPv4(t *testing.T) {
	if got := ParseLsof("p1\nca\nn192.168.1.5:3000\n"); len(got) != 0 {
		t.Errorf("ParseLsof = %+v, want empty", got)
	}
}

// TestParseLsofIgnoresOrphanSocket checks that an n field appearing before any
// p field is ignored.
func TestParseLsofIgnoresOrphanSocket(t *testing.T) {
	if got := ParseLsof("n127.0.0.1:3000\np1\nca\nn127.0.0.1:4000\n"); len(got) != 1 || got[0].Port != 4000 {
		t.Errorf("ParseLsof = %+v, want a single entry for port 4000", got)
	}
}

func TestFilterExcludesAndDedups(t *testing.T) {
	list := ParseLsof(sample)
	// Exclude the registered 5173 and the daemon's own 8080.
	got := Filter(list, map[int]bool{5173: true, 8080: true})
	want := []Listener{
		{Port: 5037, PID: 4410, Command: "adb", Address: "127.0.0.1"},
		{Port: 60397, PID: 1016, Command: "rapportd", Address: "*"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Filter =\n%+v\nwant\n%+v", got, want)
	}
}

func TestFilterEmpty(t *testing.T) {
	if got := Filter(nil, nil); len(got) != 0 {
		t.Errorf("Filter(nil, nil) = %+v, want empty", got)
	}
}

func TestPortOf(t *testing.T) {
	cases := map[string]int{
		"127.0.0.1:15353": 15353,
		"127.0.0.1:80":    80,
		"[::1]:443":       443,
		"":                0,
		"127.0.0.1":       0,
		"127.0.0.1:0":     0,
		"127.0.0.1:99999": 0,
		"127.0.0.1:abc":   0,
	}
	for in, want := range cases {
		if got := PortOf(in); got != want {
			t.Errorf("PortOf(%q) = %d, want %d", in, got, want)
		}
	}
}
