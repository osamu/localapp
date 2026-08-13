package proxy

import (
	"testing"

	"github.com/osamu/localapp/internal/registry"
)

// fakeStore is a fixed registry used by the host resolution tests.
type fakeStore struct{ apps []registry.App }

func (f fakeStore) Apps() []registry.App { return f.apps }

func (f fakeStore) App(name string) (registry.App, bool) {
	for _, a := range f.apps {
		if a.Name == name {
			return a, true
		}
	}
	return registry.App{}, false
}

func newProxy(store Store, domain string) *Proxy {
	return New(store, Options{Domain: domain})
}

func testProxy(apps ...registry.App) *Proxy {
	return newProxy(fakeStore{apps: apps}, "localapp")
}

func TestResolve(t *testing.T) {
	app1 := registry.App{Name: "app1", Services: []registry.Service{
		{Name: "web", Port: 5173},
		{Name: "api", Port: 8000, Path: "/api"},
		{Name: "api-v2", Port: 8001, Path: "/api/v2"},
		{Name: "admin", Port: 9000},
	}}
	// An app without a default service.
	app2 := registry.App{Name: "app2", Services: []registry.Service{
		{Name: "api", Port: 7000, Path: "/api"},
	}}
	p := testProxy(app1, app2)

	tests := []struct {
		name     string
		host     string
		path     string
		wantKind routeKind
		wantSvc  string
		wantPort int
		wantMnt  string
	}{
		{name: "apex", host: "localapp", wantKind: routeApex},
		{name: "apex matches with a port too", host: "localapp:14443", wantKind: routeApex},
		{name: "apex matches with a trailing dot too", host: "localapp.", wantKind: routeApex},

		{name: "subdomain passes the path through", host: "api.app1.localapp", path: "/users",
			wantKind: routeProxy, wantSvc: "api", wantPort: 8000, wantMnt: ""},
		{name: "subdomain ignores case", host: "API.App1.localapp", path: "/",
			wantKind: routeProxy, wantSvc: "api", wantPort: 8000},
		{name: "subdomain matches with a port too", host: "api.app1.localapp:14380", path: "/",
			wantKind: routeProxy, wantSvc: "api", wantPort: 8000},

		{name: "path match", host: "app1.localapp", path: "/api/users",
			wantKind: routeProxy, wantSvc: "api", wantPort: 8000, wantMnt: "/api"},
		{name: "longest path match", host: "app1.localapp", path: "/api/v2/items",
			wantKind: routeProxy, wantSvc: "api-v2", wantPort: 8001, wantMnt: "/api/v2"},
		{name: "the mount itself matches", host: "app1.localapp", path: "/api",
			wantKind: routeProxy, wantSvc: "api", wantPort: 8000, wantMnt: "/api"},
		{name: "no match off a segment boundary", host: "app1.localapp", path: "/apidocs",
			wantKind: routeProxy, wantSvc: "web", wantPort: 5173, wantMnt: ""},
		{name: "no match falls back to the default service", host: "app1.localapp", path: "/",
			wantKind: routeProxy, wantSvc: "web", wantPort: 5173, wantMnt: ""},

		{name: "unregistered app", host: "nope.localapp", path: "/", wantKind: routeUnknownApp},
		{name: "unregistered service", host: "nope.app1.localapp", path: "/", wantKind: routeUnknownService},
		{name: "no default service", host: "app2.localapp", path: "/", wantKind: routeNoDefault},
		{name: "a path match forwards even without a default service", host: "app2.localapp", path: "/api/x",
			wantKind: routeProxy, wantSvc: "api", wantPort: 7000, wantMnt: "/api"},

		{name: "outside the domain", host: "example.com", path: "/", wantKind: routeUnknownHost},
		{name: "a partial suffix match is not allowed", host: "notlocalapp", path: "/", wantKind: routeUnknownHost},
		{name: "too many labels", host: "x.api.app1.localapp", path: "/", wantKind: routeUnknownHost},
		{name: "empty host", host: "", path: "/", wantKind: routeUnknownHost},
		{name: "direct access by IP", host: "127.0.0.1:14380", path: "/", wantKind: routeUnknownHost},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.resolve(tt.host, tt.path)
			if got.kind != tt.wantKind {
				t.Fatalf("kind = %v, want %v", got.kind, tt.wantKind)
			}
			if tt.wantKind != routeProxy {
				return
			}
			if got.svcName != tt.wantSvc {
				t.Errorf("service = %q, want %q", got.svcName, tt.wantSvc)
			}
			if got.svc.Port != tt.wantPort {
				t.Errorf("port = %d, want %d", got.svc.Port, tt.wantPort)
			}
			if got.mount != tt.wantMnt {
				t.Errorf("mount = %q, want %q", got.mount, tt.wantMnt)
			}
		})
	}
}

// TestResolveCustomDomain checks that a domain change via LOCALAPP_DOMAIN is
// honored.
func TestResolveCustomDomain(t *testing.T) {
	app := registry.App{Name: "app1", Services: []registry.Service{{Name: "web", Port: 3000}}}
	p := newProxy(fakeStore{apps: []registry.App{app}}, "test.local")

	if p.domain != "test.local" {
		t.Fatalf("domain = %q, want %q", p.domain, "test.local")
	}
	if got := p.resolve("app1.test.local", "/").kind; got != routeProxy {
		t.Errorf("app1.test.local: kind = %v, want routeProxy", got)
	}
	if got := p.resolve("test.local", "/").kind; got != routeApex {
		t.Errorf("apex: kind = %v, want routeApex", got)
	}
	if got := p.resolve("app1.localapp", "/").kind; got != routeUnknownHost {
		t.Errorf("app1.localapp: kind = %v, want routeUnknownHost", got)
	}
}

func TestPathHasPrefix(t *testing.T) {
	tests := []struct {
		path, mount string
		want        bool
	}{
		{"/api", "/api", true},
		{"/api/", "/api", true},
		{"/api/users", "/api", true},
		{"/apidocs", "/api", false},
		{"/ap", "/api", false},
		{"/", "/api", false},
	}
	for _, tt := range tests {
		if got := pathHasPrefix(tt.path, tt.mount); got != tt.want {
			t.Errorf("pathHasPrefix(%q, %q) = %v, want %v", tt.path, tt.mount, got, tt.want)
		}
	}
}
