package registry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, path
}

func TestOpenMissingFileIsEmpty(t *testing.T) {
	s, path := newStore(t)
	if apps, services := s.Counts(); apps != 0 || services != 0 {
		t.Errorf("Counts() = (%d, %d), want (0, 0)", apps, services)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Open created the file as a side effect: %v", err)
	}
}

func TestPutPersistsAtomically(t *testing.T) {
	s, path := newStore(t)
	if _, err := s.Put("app1", Service{Name: DefaultService, Port: 5173}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("registry.json was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("registry.json permissions = %o, want 600", perm)
	}

	// No temporary file is left behind (temp + rename).
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "registry.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("state directory contents = %v, want [registry.json] only", names)
	}

	// Reading it as another process would yields the same contents.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	app, ok := reopened.App("app1")
	svc, ok := app.Service(DefaultService)
	if !ok || svc.Port != 5173 {
		t.Errorf("service after re-Open = %+v (ok=%v), want port=5173", svc, ok)
	}
}

func TestPutIsIdempotentUpsert(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Put("app1", Service{Name: "api", Port: 8000, Path: "/api"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("app1", Service{Name: "api", Port: 9000}); err != nil {
		t.Fatal(err)
	}
	apps, services := s.Counts()
	if apps != 1 || services != 1 {
		t.Fatalf("Counts() = (%d, %d), want (1, 1)", apps, services)
	}
	app, _ := s.App("app1")
	svc, _ := app.Service("api")
	if svc.Port != 9000 {
		t.Errorf("port = %d, want 9000 (overwritten)", svc.Port)
	}
	if svc.Path != "" {
		t.Errorf("path = %q, want empty (PUT replaces everything)", svc.Path)
	}
}

func TestPutNormalizesPath(t *testing.T) {
	s, _ := newStore(t)
	saved, err := s.Put("app1", Service{Name: "api", Port: 8000, Path: "/api/"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Path != "/api" {
		t.Errorf("path = %q, want /api", saved.Path)
	}
}

func TestPutValidates(t *testing.T) {
	s, _ := newStore(t)
	tests := []struct {
		name string
		app  string
		svc  Service
		code string
	}{
		{"invalid app name", "App_1", Service{Name: "web", Port: 80}, CodeInvalidName},
		{"invalid service name", "app1", Service{Name: "WEB", Port: 80}, CodeInvalidName},
		{"port out of range", "app1", Service{Name: "web", Port: 0}, CodeInvalidPort},
		{"invalid path", "app1", Service{Name: "web", Port: 80, Path: "api"}, CodeInvalidPath},
		{"negative PID", "app1", Service{Name: "web", Port: 80, PID: -1}, CodeInvalidPID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Put(tt.app, tt.svc)
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Code != tt.code {
				t.Fatalf("Put = %v, want code %s", err, tt.code)
			}
		})
	}
	if apps, _ := s.Counts(); apps != 0 {
		t.Errorf("a failed validation was registered: apps=%d", apps)
	}
}

func TestRemoveService(t *testing.T) {
	s, _ := newStore(t)
	mustPut(t, s, "app1", Service{Name: "web", Port: 5173})
	mustPut(t, s, "app1", Service{Name: "api", Port: 8000})

	removed, err := s.RemoveService("app1", "api")
	if err != nil || !removed {
		t.Fatalf("RemoveService = (%v, %v), want (true, nil)", removed, err)
	}
	app, _ := s.App("app1")
	if _, ok := app.Service("api"); ok {
		t.Error("the service still exists after removal")
	}
	if _, ok := s.App("app1"); !ok {
		t.Error("the app is gone even though a service remains")
	}

	// Idempotent: the second call returns false.
	if removed, err := s.RemoveService("app1", "api"); err != nil || removed {
		t.Errorf("second RemoveService = (%v, %v), want (false, nil)", removed, err)
	}

	// Removing the last service removes the app as well.
	if _, err := s.RemoveService("app1", "web"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.App("app1"); ok {
		t.Error("an app with zero services is still present")
	}
}

func TestRemoveApp(t *testing.T) {
	s, _ := newStore(t)
	mustPut(t, s, "app1", Service{Name: "web", Port: 5173})
	mustPut(t, s, "app2", Service{Name: "web", Port: 3000})

	removed, err := s.RemoveApp("app1")
	if err != nil || !removed {
		t.Fatalf("RemoveApp = (%v, %v), want (true, nil)", removed, err)
	}
	if apps, _ := s.Counts(); apps != 1 {
		t.Errorf("remaining apps = %d, want 1", apps)
	}
	if removed, _ := s.RemoveApp("app1"); removed {
		t.Error("the second RemoveApp returned true")
	}
	if removed, _ := s.RemoveApp("no-such-app"); removed {
		t.Error("RemoveApp on an unregistered app returned true")
	}
}

func TestAppsIsSortedAndCopied(t *testing.T) {
	s, _ := newStore(t)
	mustPut(t, s, "zebra", Service{Name: "web", Port: 1})
	mustPut(t, s, "alpha", Service{Name: "web", Port: 2})
	mustPut(t, s, "alpha", Service{Name: "api", Port: 3})

	apps := s.Apps()
	if len(apps) != 2 || apps[0].Name != "alpha" || apps[1].Name != "zebra" {
		t.Fatalf("Apps() order = %+v, want alpha, zebra", apps)
	}
	if apps[0].Services[0].Name != "api" || apps[0].Services[1].Name != "web" {
		t.Errorf("service order = %+v, want api, web", apps[0].Services)
	}

	// The return value is a copy; mutating it must not affect the Store.
	apps[0].Services[0].Port = 9999
	stored, _ := s.App("alpha")
	svc, _ := stored.Service("api")
	if svc.Port != 3 {
		t.Errorf("Apps() shares state with the Store: port=%d", svc.Port)
	}
}

func TestOnDiskFormat(t *testing.T) {
	s, path := newStore(t)
	mustPut(t, s, "app1", Service{Name: "api", Port: 8000, Path: "/api", StripPath: true, PID: 42})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Apps []struct {
			Name     string    `json:"name"`
			Services []Service `json:"services"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parsing registry.json: %v\n%s", err, data)
	}
	if len(f.Apps) != 1 || f.Apps[0].Name != "app1" {
		t.Fatalf("apps = %+v", f.Apps)
	}
	got := f.Apps[0].Services[0]
	want := Service{Name: "api", Port: 8000, Path: "/api", StripPath: true, PID: 42}
	if got != want {
		t.Errorf("service = %+v, want %+v", got, want)
	}
}

func TestOpenCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Error("Open did not fail on a corrupt registry.json")
	}
}

func TestConcurrentPut(t *testing.T) {
	s, path := newStore(t)
	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Put("app"+strconv.Itoa(i), Service{Name: "web", Port: 3000 + i}); err != nil {
				t.Errorf("Put: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if apps, services := s.Counts(); apps != n || services != n {
		t.Errorf("Counts() = (%d, %d), want (%d, %d)", apps, services, n, n)
	}
	// Even after concurrent writes the file is always valid JSON (atomic write).
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	if apps, _ := reopened.Counts(); apps != n {
		t.Errorf("apps after re-Open = %d, want %d", apps, n)
	}
}

func mustPut(t *testing.T, s *Store, app string, svc Service) {
	t.Helper()
	if _, err := s.Put(app, svc); err != nil {
		t.Fatalf("Put(%s, %+v): %v", app, svc, err)
	}
}
