package registry

import (
	"errors"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestValidateName(t *testing.T) {
	valid := []string{"a", "app1", "my-app", "0", "a-b-c", stringOfLen(63)}
	for _, n := range valid {
		if err := ValidateName("app", n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{"", "App", "my_app", "app.1", "app/1", stringOfLen(64), "アプリ", "app 1"}
	for _, n := range invalid {
		err := ValidateName("app", n)
		if err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", n)
			continue
		}
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Code != CodeInvalidName {
			t.Errorf("ValidateName(%q) code = %v, want %s", n, err, CodeInvalidName)
		}
	}
}

func TestValidatePort(t *testing.T) {
	for _, p := range []int{1, 80, 5173, 65535} {
		if err := ValidatePort(p); err != nil {
			t.Errorf("ValidatePort(%d) = %v, want nil", p, err)
		}
	}
	for _, p := range []int{0, -1, 65536, 100000} {
		var ve *ValidationError
		if err := ValidatePort(p); !errors.As(err, &ve) || ve.Code != CodeInvalidPort {
			t.Errorf("ValidatePort(%d) = %v, want %s", p, err, CodeInvalidPort)
		}
	}
}

func TestValidatePath(t *testing.T) {
	for _, p := range []string{"", "/", "/api", "/api/v1", "/a-b_c"} {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil", p, err)
		}
	}
	for _, p := range []string{"api", "api/v1", "/api?x=1", "/api#f", "/a b"} {
		var ve *ValidationError
		if err := ValidatePath(p); !errors.As(err, &ve) || ve.Code != CodeInvalidPath {
			t.Errorf("ValidatePath(%q) = %v, want %s", p, err, CodeInvalidPath)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"my_app":     "my-app",
		"MyApp":      "myapp",
		"my app":     "my-app",
		"my__app":    "my-app",
		"-my-app-":   "my-app",
		"app1":       "app1",
		"":           "",
		"___":        "",
		"日本語":        "",
		"café":       "caf",
		"a.b.c":      "a-b-c",
		"UPPER_Case": "upper-case",
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
	long := NormalizeName(stringOfLen(200))
	if len(long) != 63 {
		t.Errorf("truncated long name = %d chars, want 63", len(long))
	}
	if err := ValidateName("app", long); err != nil {
		t.Errorf("normalized name fails validation: %v", err)
	}
}

func TestNormalizePath(t *testing.T) {
	cases := map[string]string{"": "", "/": "", "/api": "/api", "/api/": "/api", "/api//": "/api"}
	for in, want := range cases {
		if got := NormalizePath(in); got != want {
			t.Errorf("NormalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// URL derivation follows DESIGN.md "ルーティングモデル".
func TestServiceURLs(t *testing.T) {
	tests := []struct {
		name string
		svc  Service
		want []string
	}{
		{
			name: "default service has both apex and subdomain",
			svc:  Service{Name: DefaultService, Port: 5173},
			want: []string{"https://app1.localapp/", "https://web.app1.localapp/"},
		},
		{
			name: "non-default service has the subdomain only",
			svc:  Service{Name: "api", Port: 8000},
			want: []string{"https://api.app1.localapp/"},
		},
		{
			name: "a path adds the path mount (PUT response example in DESIGN.md)",
			svc:  Service{Name: "api", Port: 8000, Path: "/api"},
			want: []string{"https://api.app1.localapp/", "https://app1.localapp/api/"},
		},
		{
			name: "default service plus path",
			svc:  Service{Name: DefaultService, Port: 3000, Path: "/app"},
			want: []string{"https://app1.localapp/", "https://web.app1.localapp/", "https://app1.localapp/app/"},
		},
		{
			name: "a path with a trailing slash is normalized",
			svc:  Service{Name: "api", Port: 8000, Path: "/api/"},
			want: []string{"https://api.app1.localapp/", "https://app1.localapp/api/"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.svc.URLs("app1", "localapp")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("URLs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestServiceURLsHonorsDomain(t *testing.T) {
	got := Service{Name: DefaultService, Port: 1}.URLs("app1", "test")
	want := []string{"https://app1.test/", "https://web.app1.test/"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("URLs() = %v, want %v", got, want)
	}
}

func TestStatus(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if got := Status(port, 500*time.Millisecond); got != StatusUp {
		t.Errorf("status of listening port %d = %q, want %q", port, got, StatusUp)
	}

	ln.Close()
	if got := Status(port, 200*time.Millisecond); got != StatusDown {
		t.Errorf("status of closed port %d = %q, want %q", port, got, StatusDown)
	}
}

func stringOfLen(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
