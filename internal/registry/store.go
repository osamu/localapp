package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// file is the on-disk representation of registry.json.
type file struct {
	Apps []App `json:"apps"`
}

// Store holds the in-memory registry and persists it to registry.json.
// Every operation is serialized by the lock (DESIGN.md "並行制御":
// last-write-wins).
type Store struct {
	mu   sync.RWMutex
	path string
	apps []App
}

// Open loads registry.json. It returns an empty Store when the file does not
// exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("reading registry.json: %w", err)
	}
	if len(data) == 0 {
		return s, nil
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing registry.json (%s): %w", path, err)
	}
	s.apps = f.Apps
	s.sortLocked()
	return s, nil
}

// Apps returns a copy of every app, ordered by name.
func (s *Store) Apps() []App {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneApps(s.apps)
}

// App returns a copy of a single app.
func (s *Store) App(name string) (App, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := s.indexLocked(name)
	if i < 0 {
		return App{}, false
	}
	return cloneApp(s.apps[i]), true
}

// Counts returns the number of apps and the total number of services.
func (s *Store) Counts() (apps, services int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.apps {
		services += len(a.Services)
	}
	return len(s.apps), services
}

// Put idempotently upserts a service and persists the registry.
// An existing service with the same name is replaced entirely (PUT semantics).
func (s *Store) Put(app string, svc Service) (Service, error) {
	if err := ValidateName("app", app); err != nil {
		return Service{}, err
	}
	if err := svc.Validate(); err != nil {
		return Service{}, err
	}
	svc.Path = NormalizePath(svc.Path)

	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.indexLocked(app)
	if i < 0 {
		s.apps = append(s.apps, App{Name: app, Services: []Service{svc}})
	} else {
		replaced := false
		for j := range s.apps[i].Services {
			if s.apps[i].Services[j].Name == svc.Name {
				s.apps[i].Services[j] = svc
				replaced = true
				break
			}
		}
		if !replaced {
			s.apps[i].Services = append(s.apps[i].Services, svc)
		}
	}
	s.sortLocked()
	if err := s.saveLocked(); err != nil {
		return Service{}, err
	}
	return svc, nil
}

// RemoveApp deletes an app entirely. It returns false when the app does not
// exist (idempotent).
func (s *Store) RemoveApp(app string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexLocked(app)
	if i < 0 {
		return false, nil
	}
	s.apps = append(s.apps[:i], s.apps[i+1:]...)
	if err := s.saveLocked(); err != nil {
		return false, err
	}
	return true, nil
}

// RemoveService deletes a service. It returns false when the service does not
// exist (idempotent). An app left with zero services is dropped from the
// registry.
func (s *Store) RemoveService(app, service string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexLocked(app)
	if i < 0 {
		return false, nil
	}
	svcs := s.apps[i].Services
	for j := range svcs {
		if svcs[j].Name != service {
			continue
		}
		s.apps[i].Services = append(svcs[:j], svcs[j+1:]...)
		if len(s.apps[i].Services) == 0 {
			s.apps = append(s.apps[:i], s.apps[i+1:]...)
		}
		if err := s.saveLocked(); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (s *Store) indexLocked(name string) int {
	for i := range s.apps {
		if s.apps[i].Name == name {
			return i
		}
	}
	return -1
}

func (s *Store) sortLocked() {
	sort.Slice(s.apps, func(i, j int) bool { return s.apps[i].Name < s.apps[j].Name })
	for i := range s.apps {
		svcs := s.apps[i].Services
		sort.Slice(svcs, func(a, b int) bool { return svcs[a].Name < svcs[b].Name })
	}
}

// saveLocked writes registry.json atomically (temp file + rename).
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	apps := s.apps
	if apps == nil {
		apps = []App{}
	}
	data, err := json.MarshalIndent(file{Apps: apps}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding registry.json: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating state directory (%s): %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".registry-*.json")
	if err != nil {
		return fmt.Errorf("creating temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeded

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temporary file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("renaming to registry.json: %w", err)
	}
	return nil
}

func cloneApps(in []App) []App {
	out := make([]App, 0, len(in))
	for _, a := range in {
		out = append(out, cloneApp(a))
	}
	return out
}

func cloneApp(a App) App {
	svcs := make([]Service, len(a.Services))
	copy(svcs, a.Services)
	return App{Name: a.Name, Services: svcs}
}
