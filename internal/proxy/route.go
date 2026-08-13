package proxy

import (
	"net"
	"strings"

	"github.com/osamu/localapp/internal/registry"
)

// routeKind is the kind of a resolution result.
type routeKind int

const (
	// routeProxy means the forwarding target was determined.
	routeProxy routeKind = iota
	// routeApex is access to the apex (`<domain>`); the dashboard answers.
	routeApex
	// routeUnknownHost is a host outside the configured domain, or one with an
	// invalid number of labels.
	routeUnknownHost
	// routeUnknownApp is an unregistered app.
	routeUnknownApp
	// routeUnknownService means the app exists but the service is not
	// registered.
	routeUnknownService
	// routeNoDefault means no path matched and the default service (web) is not
	// registered either.
	routeNoDefault
)

// route is the result of host resolution
// (DESIGN.md "Routing model", host resolution priorities).
type route struct {
	kind routeKind
	// host is the normalized host name (lowercased, port removed).
	host string
	// appName / svcName are the names being resolved. They are also used by the
	// error page shown when they are not registered.
	appName string
	svcName string
	// app is the app, when it was found in the registry.
	app registry.App
	// svc is the target service (valid when kind == routeProxy).
	svc registry.Service
	// mount is the path mount that matched. It is empty on the subdomain route
	// (where the path is passed through).
	mount string
}

// normalizeHost brings the Host header into a comparable form.
// It strips the port, a trailing dot, uppercase letters and IPv6 brackets.
func normalizeHost(host string) string {
	h := strings.TrimSpace(host)
	if hostOnly, _, err := net.SplitHostPort(h); err == nil {
		h = hostOnly
	}
	h = strings.TrimSuffix(h, ".")
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	return strings.ToLower(h)
}

// resolve determines the forwarding target from the Host header and the request
// path.
//
//  1. `<service>.<app>.<domain>` → that service, path passed through
//  2. `<app>.<domain>`           → longest path prefix match
//  3. no match                   → the default service (web)
//  4. apex (`<domain>`)          → the dashboard
//
// A host matching none of the above becomes routeUnknownHost, and the caller
// returns a 404 error page.
func (p *Proxy) resolve(host, path string) route {
	h := normalizeHost(host)
	r := route{host: h}

	if h == p.domain {
		r.kind = routeApex
		return r
	}
	sub, ok := strings.CutSuffix(h, "."+p.domain)
	if !ok || sub == "" {
		r.kind = routeUnknownHost
		return r
	}

	labels := strings.Split(sub, ".")
	switch len(labels) {
	case 1:
		r.appName = labels[0]
	case 2:
		r.svcName, r.appName = labels[0], labels[1]
	default:
		// Deeper hosts such as `<x>.<service>.<app>.<domain>` are not defined.
		r.kind = routeUnknownHost
		return r
	}

	app, found := p.store.App(r.appName)
	if !found {
		r.kind = routeUnknownApp
		return r
	}
	r.app = app

	// Priority 1: forward to the service named by the subdomain, path passed
	// through.
	if r.svcName != "" {
		svc, ok := app.Service(r.svcName)
		if !ok {
			r.kind = routeUnknownService
			return r
		}
		r.kind, r.svc = routeProxy, svc
		return r
	}

	// Priority 2: longest path mount match.
	if svc, mount, ok := matchPath(app, path); ok {
		r.kind, r.svc, r.svcName, r.mount = routeProxy, svc, svc.Name, mount
		return r
	}

	// Priority 3: with no match, the default service.
	if svc, ok := app.Service(registry.DefaultService); ok {
		r.kind, r.svc, r.svcName = routeProxy, svc, svc.Name
		return r
	}
	r.kind, r.svcName = routeNoDefault, registry.DefaultService
	return r
}

// matchPath returns the longest matching path mount. Services without a mount
// are not considered.
func matchPath(app registry.App, path string) (registry.Service, string, bool) {
	var (
		best      registry.Service
		bestMount string
		found     bool
	)
	for _, svc := range app.Services {
		mount := registry.NormalizePath(svc.Path)
		if mount == "" || !pathHasPrefix(path, mount) {
			continue
		}
		if !found || len(mount) > len(bestMount) {
			best, bestMount, found = svc, mount, true
		}
	}
	return best, bestMount, found
}

// pathHasPrefix reports whether the path is under the mount. It splits on
// segment boundaries, so `/api` matches `/api` and `/api/x` but not `/apidocs`.
func pathHasPrefix(path, mount string) bool {
	return path == mount || strings.HasPrefix(path, mount+"/")
}
