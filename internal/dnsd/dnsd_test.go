package dnsd

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

const testDomain = "localapp"

// start runs a server on an ephemeral port and returns its listening address.
func start(t *testing.T, domain string) string {
	t.Helper()
	s, err := New(domain)
	if err != nil {
		t.Fatalf("New(%q): %v", domain, err)
	}
	l, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, l) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return l.Addr()
}

// exchange sends one query and returns the response. proto is "udp" or "tcp".
func exchange(t *testing.T, addr, proto string, q dns.Question) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.Id = dns.Id()
	m.RecursionDesired = true
	m.Question = []dns.Question{q}

	c := &dns.Client{Net: proto, Timeout: 5 * time.Second}
	resp, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatalf("%s %s %s: %v", proto, q.Name, dns.TypeToString[q.Qtype], err)
	}
	return resp
}

func question(name string, qtype uint16) dns.Question {
	return dns.Question{Name: name, Qtype: qtype, Qclass: dns.ClassINET}
}

// TestAnswersLoopback checks that A and AAAA queries for the apex, a subdomain
// and nested subdomains are answered with loopback addresses.
func TestAnswersLoopback(t *testing.T) {
	addr := start(t, testDomain)

	names := []string{
		"localapp.",              // apex
		"app1.localapp.",         // subdomain
		"api.app1.localapp.",     // nested subdomain
		"a.b.c.d.app1.localapp.", // even deeper
	}
	for _, name := range names {
		for _, proto := range []string{"udp", "tcp"} {
			t.Run(proto+"/"+name, func(t *testing.T) {
				resp := exchange(t, addr, proto, question(name, dns.TypeA))
				assertRcode(t, resp, dns.RcodeSuccess)
				if !resp.Authoritative {
					t.Error("the AA bit is not set")
				}
				if len(resp.Answer) != 1 {
					t.Fatalf("answer count = %d, want 1 (%v)", len(resp.Answer), resp.Answer)
				}
				a, ok := resp.Answer[0].(*dns.A)
				if !ok {
					t.Fatalf("answer type = %T, want *dns.A", resp.Answer[0])
				}
				if !a.A.Equal(net.IPv4(127, 0, 0, 1)) {
					t.Errorf("A = %v, want 127.0.0.1", a.A)
				}
				if a.Hdr.Name != name {
					t.Errorf("name = %q, want %q", a.Hdr.Name, name)
				}
				if a.Hdr.Ttl != uint32(DefaultTTL/time.Second) {
					t.Errorf("TTL = %d, want %d", a.Hdr.Ttl, uint32(DefaultTTL/time.Second))
				}
			})
		}
	}

	for _, name := range names {
		t.Run("AAAA/"+name, func(t *testing.T) {
			resp := exchange(t, addr, "udp", question(name, dns.TypeAAAA))
			assertRcode(t, resp, dns.RcodeSuccess)
			if len(resp.Answer) != 1 {
				t.Fatalf("answer count = %d, want 1", len(resp.Answer))
			}
			aaaa, ok := resp.Answer[0].(*dns.AAAA)
			if !ok {
				t.Fatalf("answer type = %T, want *dns.AAAA", resp.Answer[0])
			}
			if !aaaa.AAAA.Equal(net.IPv6loopback) {
				t.Errorf("AAAA = %v, want ::1", aaaa.AAAA)
			}
		})
	}
}

// TestCaseInsensitiveMatch checks that matching ignores case and that a
// 0x20-encoded question name is echoed verbatim.
func TestCaseInsensitiveMatch(t *testing.T) {
	addr := start(t, testDomain)

	const name = "ApP1.LoCaLaPp."
	resp := exchange(t, addr, "udp", question(name, dns.TypeA))
	assertRcode(t, resp, dns.RcodeSuccess)
	if len(resp.Answer) != 1 {
		t.Fatalf("answer count = %d, want 1", len(resp.Answer))
	}
	if got := resp.Answer[0].Header().Name; got != name {
		t.Errorf("answer name = %q, want %q (0x20 echo)", got, name)
	}
	if got := resp.Question[0].Name; got != name {
		t.Errorf("question name = %q, want %q", got, name)
	}
}

// TestRefusedOutsideDomain checks that queries outside the configured domain
// are REFUSED, and that a partial suffix match ("notlocalapp") is not mistaken
// for a match.
func TestRefusedOutsideDomain(t *testing.T) {
	addr := start(t, testDomain)

	names := []string{
		"example.com.",
		"localapp.example.com.",
		"notlocalapp.",
		"app1.notlocalapp.",
		"xlocalapp.",
		".",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			resp := exchange(t, addr, "udp", question(name, dns.TypeA))
			assertRcode(t, resp, dns.RcodeRefused)
			if len(resp.Answer) != 0 {
				t.Errorf("answer = %v, want empty", resp.Answer)
			}
		})
	}
}

// TestNoDataForOtherTypes checks that types other than A/AAAA in the target
// domain yield NOERROR with an empty answer (NODATA).
func TestNoDataForOtherTypes(t *testing.T) {
	addr := start(t, testDomain)

	types := []uint16{dns.TypeMX, dns.TypeTXT, dns.TypeSOA, dns.TypeNS, dns.TypeCNAME, dns.TypeSRV, dns.TypeANY}
	for _, qtype := range types {
		t.Run(dns.TypeToString[qtype], func(t *testing.T) {
			resp := exchange(t, addr, "udp", question("app1.localapp.", qtype))
			assertRcode(t, resp, dns.RcodeSuccess)
			if len(resp.Answer) != 0 {
				t.Errorf("answer = %v, want empty", resp.Answer)
			}
			if !resp.Authoritative {
				t.Error("the AA bit is not set")
			}
		})
	}
}

// TestRefusedNonINClass checks that classes other than IN are REFUSED.
func TestRefusedNonINClass(t *testing.T) {
	addr := start(t, testDomain)

	resp := exchange(t, addr, "udp", dns.Question{
		Name: "app1.localapp.", Qtype: dns.TypeA, Qclass: dns.ClassCHAOS,
	})
	assertRcode(t, resp, dns.RcodeRefused)
}

// TestNonQueryOpcode checks that opcodes other than QUERY yield NOTIMP.
func TestNonQueryOpcode(t *testing.T) {
	addr := start(t, testDomain)

	m := new(dns.Msg)
	m.SetNotify("localapp.")
	c := &dns.Client{Timeout: 5 * time.Second}
	resp, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	assertRcode(t, resp, dns.RcodeNotImplemented)
}

// TestEDNS0 checks that a query with EDNS0 gets a response containing OPT.
func TestEDNS0(t *testing.T) {
	addr := start(t, testDomain)

	m := new(dns.Msg)
	m.SetQuestion("app1.localapp.", dns.TypeA)
	m.SetEdns0(4096, false)
	c := &dns.Client{UDPSize: 4096, Timeout: 5 * time.Second}
	resp, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	assertRcode(t, resp, dns.RcodeSuccess)
	if resp.IsEdns0() == nil {
		t.Error("the response has no OPT record")
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("answer count = %d, want 1", len(resp.Answer))
	}
}

// TestListenSamePort checks that UDP and TCP are opened on the same port.
func TestListenSamePort(t *testing.T) {
	l, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	_, udpPort, err := net.SplitHostPort(l.UDP.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, tcpPort, err := net.SplitHostPort(l.TCP.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if udpPort != tcpPort {
		t.Errorf("UDP port %s != TCP port %s", udpPort, tcpPort)
	}
	if l.Addr() != l.UDP.LocalAddr().String() {
		t.Errorf("Addr() = %q", l.Addr())
	}
	// Without Serve, calling Close twice must not fail.
	if err := l.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// waitBound waits until addr starts listening.
func waitBound(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s does not start listening", addr)
}

// TestNewValidation checks the validation of the domain setting.
func TestNewValidation(t *testing.T) {
	valid := []struct{ in, want string }{
		{"localapp", "localapp."},
		{"dev.internal", "dev.internal."},
	}
	for _, tc := range valid {
		s, err := New(tc.in)
		if err != nil {
			t.Errorf("New(%q): %v", tc.in, err)
			continue
		}
		if s.domain != tc.want {
			t.Errorf("New(%q).domain = %q, want %q", tc.in, s.domain, tc.want)
		}
	}

	invalid := []string{"", ".", "   ", "LocalApp", "localapp.", "local_app", "local app", "-localapp", "localapp-", "a..b", "*.localapp"}
	for _, in := range invalid {
		if _, err := New(in); err == nil {
			t.Errorf("New(%q) did not return an error", in)
		}
	}
}

// TestSubDomainOfConfiguredMultiLabel checks that matching also works for a
// domain setting with multiple labels.
func TestSubDomainOfConfiguredMultiLabel(t *testing.T) {
	addr := start(t, "dev.internal")

	resp := exchange(t, addr, "udp", question("app1.dev.internal.", dns.TypeA))
	assertRcode(t, resp, dns.RcodeSuccess)

	resp = exchange(t, addr, "udp", question("internal.", dns.TypeA))
	assertRcode(t, resp, dns.RcodeRefused)
}

func assertRcode(t *testing.T, resp *dns.Msg, want int) {
	t.Helper()
	if resp.Rcode != want {
		t.Errorf("rcode = %s, want %s", dns.RcodeToString[resp.Rcode], dns.RcodeToString[want])
	}
}
