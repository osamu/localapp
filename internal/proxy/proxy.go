// Package proxy provides a reverse proxy driven by the registry.
//
// It determines the target service from the host name and path
// (DESIGN.md "ルーティングモデル") and forwards to `localhost:<port>` with
// `httputil.ReverseProxy`. Before forwarding it probes liveness with a TCP
// connection and, on failure, returns an HTML error page containing the
// registration details (DESIGN.md "登録ライフサイクル").
//
// The registry is consulted per request, so changes to it take effect from the
// next request on.
package proxy

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/osamu/localapp/internal/registry"
)

// Store is the registry the proxy consults. *registry.Store satisfies it.
type Store interface {
	App(name string) (registry.App, bool)
	Apps() []registry.App
}

// Options configures a Proxy.
type Options struct {
	// Domain is the domain suffix (default "localapp").
	Domain string
	// Dashboard is the handler for the apex (`https://<domain>/`).
	Dashboard http.Handler
	// ProbeTimeout is the TCP connect timeout of the liveness probe run before
	// forwarding. 0 means the default.
	ProbeTimeout time.Duration
	// ErrorLog is where forwarding errors go. nil means the standard log
	// package.
	ErrorLog *log.Logger
	// Transport is the RoundTripper used for forwarding. nil means the default
	// (a substitution point for tests).
	Transport http.RoundTripper
}

// Proxy is the http.Handler of the reverse proxy.
type Proxy struct {
	store     Store
	domain    string
	dashboard http.Handler
	probeTO   time.Duration
	rp        *httputil.ReverseProxy
}

// routeKey is the key used to carry the resolved route in the request context.
type routeKey struct{}

// New builds a Proxy.
func New(store Store, opts Options) *Proxy {
	p := &Proxy{
		store:     store,
		domain:    opts.Domain,
		dashboard: opts.Dashboard,
		probeTO:   opts.ProbeTimeout,
	}
	if p.probeTO <= 0 {
		p.probeTO = registry.DefaultProbeTimeout
	}
	transport := opts.Transport
	if transport == nil {
		transport = newTransport()
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite:   p.rewrite,
		Transport: transport,
		// Flush immediately for SSE and streaming
		// (DESIGN.md "プロキシ実装要件").
		FlushInterval: -1,
		ErrorLog:      opts.ErrorLog,
		ErrorHandler:  p.handleTransportError,
	}
	return p
}

// newTransport creates the RoundTripper used for forwarding.
// No response timeout is set, because the first compile of a dev server can
// take tens of seconds (DESIGN.md "プロキシ実装要件").
func newTransport() *http.Transport {
	return &http.Transport{
		// The target is always localhost; HTTP proxy environment variables are
		// not honored.
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// The target speaks plaintext HTTP; h2c is not attempted.
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// ResponseHeaderTimeout is deliberately left unset.
	}
}

// ServeHTTP resolves the host, probes liveness and forwards.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt := p.resolve(r.Host, r.URL.Path)

	switch rt.kind {
	case routeApex:
		p.serveApex(w, r)
		return
	case routeProxy:
		// Fall through to forwarding.
	default:
		p.serveNotFound(w, rt)
		return
	}

	// Liveness probe. Instead of a bare 502, return a page with the
	// registration details. With a pid, the process must be alive as well
	// (DESIGN.md "登録ライフサイクル").
	if registry.ServiceStatus(rt.svc, p.probeTO) == registry.StatusDown {
		p.serveUnavailable(w, rt, "")
		return
	}

	r = r.WithContext(context.WithValue(r.Context(), routeKey{}, rt))
	p.rp.ServeHTTP(w, r)
}

// serveApex hands the apex host to the dashboard.
func (p *Proxy) serveApex(w http.ResponseWriter, r *http.Request) {
	p.dashboard.ServeHTTP(w, r)
}

// rewrite builds the target URL and headers.
func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	rt := pr.In.Context().Value(routeKey{}).(route)

	// ReverseProxy already stripped client-supplied X-Forwarded-* / Forwarded.
	pr.SetXForwarded()
	// TLS is terminated by the proxy. Always report https, even though the
	// target is http (DESIGN.md "プロキシ実装要件").
	pr.Out.Header.Set("X-Forwarded-Proto", "https")

	pr.Out.URL.Scheme = "http"
	pr.Out.URL.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(rt.svc.Port))
	// The Host header keeps its original value.
	pr.Out.Host = pr.In.Host

	// Drop the prefix only on the path-mount route and only when strip_path is
	// set. The default is not to strip (DESIGN.md "ルーティングモデル").
	if rt.mount != "" && rt.svc.StripPath {
		stripMount(pr.Out.URL, rt.mount)
	}
}

// stripMount removes the path mount prefix from the URL.
func stripMount(u *url.URL, mount string) {
	p, ok := strings.CutPrefix(u.Path, mount)
	if !ok {
		return
	}
	if p == "" {
		p = "/"
	}
	u.Path = p
	if u.RawPath == "" {
		return
	}
	if rp, ok := strings.CutPrefix(u.RawPath, mount); ok {
		if rp == "" {
			rp = "/"
		}
		u.RawPath = rp
		return
	}
	// When the encoded path does not match the mount, let it be re-encoded from
	// Path.
	u.RawPath = ""
}

// handleTransportError turns an error during forwarding into a 503 error page.
// It is called, for example, when the backend dies after the liveness probe
// passed.
func (p *Proxy) handleTransportError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		// The client disconnected; a response would not reach it.
		return
	}
	rt := r.Context().Value(routeKey{}).(route)
	p.serveUnavailable(w, rt, err.Error())
}
