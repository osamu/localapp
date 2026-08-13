// Package dnsd provides a DNS server that resolves everything under the
// configured domain to loopback.
//
// Answers are always 127.0.0.1 / ::1; no external IP is ever returned. Queries
// outside the configured domain are REFUSED (DESIGN.md "セキュリティ", DNS row).
// Zone transfers, recursion and upstream forwarding are not implemented.
package dnsd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// DefaultTTL is the TTL of answer records. It is kept short so that dev servers
// being swapped out are picked up quickly.
const DefaultTTL = 10 * time.Second

// shutdownTimeout is how long a graceful shutdown is awaited.
const shutdownTimeout = 5 * time.Second

// The fixed addresses that are answered.
var (
	loopbackV4 = net.IPv4(127, 0, 0, 1).To4()
	loopbackV6 = net.IPv6loopback
)

// Server is the authoritative DNS handler for the configured domain. It
// implements dns.Handler.
type Server struct {
	// domain is the lowercased FQDN (for example "localapp.").
	domain string
	ttl    uint32
}

// New builds a Server that answers for domain and its subdomains.
// An empty string or a value violating the label format is an error (so that a
// misconfiguration cannot make it answer for the whole root zone).
func New(domain string) (*Server, error) {
	if domain == "" {
		return nil, errors.New("the domain is empty")
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return nil, fmt.Errorf("domain %q is not a sequence of [a-z0-9-] labels", domain)
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return nil, fmt.Errorf("domain %q is not a sequence of [a-z0-9-] labels", domain)
			}
		}
	}
	return &Server{domain: dns.Fqdn(domain), ttl: uint32(DefaultTTL / time.Second)}, nil
}

// ServeDNS handles one query.
func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.RecursionAvailable = false

	switch {
	case r.Opcode != dns.OpcodeQuery:
		m.Rcode = dns.RcodeNotImplemented
	case len(r.Question) != 1:
		m.Rcode = dns.RcodeFormatError
	default:
		s.answer(m, r.Question[0])
	}
	s.write(w, r, m)
}

// answer writes the response to a single question into m.
func (s *Server) answer(m *dns.Msg, q dns.Question) {
	if q.Qclass != dns.ClassINET || !s.match(q.Name) {
		m.Rcode = dns.RcodeRefused
		return
	}
	m.Authoritative = true

	// The question name is echoed verbatim, 0x20 encoding included.
	hdr := func(rrtype uint16) dns.RR_Header {
		return dns.RR_Header{Name: q.Name, Rrtype: rrtype, Class: dns.ClassINET, Ttl: s.ttl}
	}
	switch q.Qtype {
	case dns.TypeA:
		m.Answer = append(m.Answer, &dns.A{Hdr: hdr(dns.TypeA), A: loopbackV4})
	case dns.TypeAAAA:
		m.Answer = append(m.Answer, &dns.AAAA{Hdr: hdr(dns.TypeAAAA), AAAA: loopbackV6})
	}
	// Anything other than A / AAAA is NODATA (NOERROR with an empty answer).
}

// match reports whether the name is the apex of the configured domain or one of
// its subdomains. The comparison is case-insensitive.
func (s *Server) match(name string) bool {
	n := strings.ToLower(dns.Fqdn(name))
	return n == s.domain || strings.HasSuffix(n, "."+s.domain)
}

// write sends the response. Over UDP it truncates to a size that accounts for
// EDNS0.
func (s *Server) write(w dns.ResponseWriter, r, m *dns.Msg) {
	size := dns.MinMsgSize
	if opt := r.IsEdns0(); opt != nil {
		if sz := int(opt.UDPSize()); sz > size {
			size = sz
		}
		m.SetEdns0(uint16(size), opt.Do())
	}
	if _, isUDP := w.RemoteAddr().(*net.UDPAddr); isUDP {
		m.Truncate(size)
	}
	_ = w.WriteMsg(m)
}

// Listeners is the UDP / TCP pair listening on the same port.
type Listeners struct {
	UDP net.PacketConn
	TCP net.Listener
}

// Addr returns the listening address.
func (l *Listeners) Addr() string { return l.UDP.LocalAddr().String() }

// Close closes both listeners. Calling it after Serve released them is
// harmless.
func (l *Listeners) Close() error {
	err := l.UDP.Close()
	if terr := l.TCP.Close(); err == nil {
		err = terr
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// Listen opens UDP and TCP on addr. With port 0 the TCP listener uses the port
// assigned to the UDP one.
func Listen(addr string) (*Listeners, error) {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("listening for DNS over UDP (%s): %w", addr, err)
	}
	ln, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("listening for DNS over TCP (%s): %w", addr, err)
	}
	return &Listeners{UDP: pc, TCP: ln}, nil
}

// running is one listener that has been started. err may only be read after
// done is closed.
type running struct {
	srv     *dns.Server
	started chan struct{}
	done    chan struct{}
	err     error
}

// Serve answers over both UDP and TCP on l. Cancelling ctx shuts the servers
// down gracefully and closes the listeners.
func (s *Server) Serve(ctx context.Context, l *Listeners) error {
	rs := make([]*running, 0, 2)
	for _, srv := range []*dns.Server{
		{PacketConn: l.UDP, Handler: s},
		{Listener: l.TCP, Handler: s},
	} {
		r := &running{srv: srv, started: make(chan struct{}), done: make(chan struct{})}
		srv.NotifyStartedFunc = func() { close(r.started) }
		go func() {
			r.err = r.srv.ActivateAndServe()
			close(r.done)
		}()
		rs = append(rs, r)
	}

	// Wait until one of them fails or ctx is cancelled.
	select {
	case <-rs[0].done:
	case <-rs[1].done:
	case <-ctx.Done():
	}

	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var err error
	for _, r := range rs {
		// A server that has not started yet does not accept Shutdown, so wait
		// until startup completes.
		select {
		case <-r.started:
		case <-r.done:
		}
		_ = r.srv.ShutdownContext(sctx)
		<-r.done
		if r.err != nil && err == nil {
			err = r.err
		}
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
