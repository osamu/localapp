package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The HTTP listener redirects to HTTPS with a 301, keeping the Host. When the
// HTTPS port is not 443, the Location includes the port
// (DESIGN.md "Architecture", HTTP row).
func TestRedirectHandler(t *testing.T) {
	tests := []struct {
		name       string
		httpsPort  int
		host       string
		target     string
		wantStatus int
		wantLoc    string
	}{
		{"the default port is not included", 443, "app1.localapp", "/", http.StatusMovedPermanently, "https://app1.localapp/"},
		{"the request port is not carried over", 443, "app1.localapp:80", "/x", http.StatusMovedPermanently, "https://app1.localapp/x"},
		{"a non-default port appears in the Location", 18443, "app1.localapp:18080", "/x", http.StatusMovedPermanently, "https://app1.localapp:18443/x"},
		{"the path and query are kept", 18443, "api.app1.localapp", "/a/b?c=d&e=f", http.StatusMovedPermanently, "https://api.app1.localapp:18443/a/b?c=d&e=f"},
		{"the subdomain is kept", 443, "api.app1.localapp", "/api/users", http.StatusMovedPermanently, "https://api.app1.localapp/api/users"},
		{"IPv6 literal", 443, "[::1]:80", "/", http.StatusMovedPermanently, "https://[::1]/"},
		{"IPv6 literal with a non-default port", 18443, "[::1]:80", "/", http.StatusMovedPermanently, "https://[::1]:18443/"},
		{"no Host yields 400", 443, "", "/", http.StatusBadRequest, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			redirectHandler(tt.httpsPort).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Location"); got != tt.wantLoc {
				t.Errorf("Location = %q, want %q", got, tt.wantLoc)
			}
		})
	}
}
