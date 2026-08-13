package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/osamu/localapp/internal/registry"
)

// Options configures a Server.
type Options struct {
	// Domain is the domain suffix (used to derive URLs).
	Domain string
	// Version is the version reported by GET /v1/status.
	Version string
	// Listeners is the listener map reported by GET /v1/status.
	Listeners map[string]string
	// ProbeTimeout is the TCP connect timeout of liveness probes. 0 means the
	// default.
	ProbeTimeout time.Duration
	// StartedAt is the origin of uptime. The zero value means the time of the
	// NewServer call.
	StartedAt time.Time
}

// Server is the HTTP handler of the Control Plane API.
type Server struct {
	store     *registry.Store
	domain    string
	version   string
	listeners map[string]string
	probeTO   time.Duration
	startedAt time.Time
}

// NewServer builds a Server.
func NewServer(store *registry.Store, opts Options) *Server {
	s := &Server{
		store:     store,
		domain:    opts.Domain,
		version:   opts.Version,
		listeners: opts.Listeners,
		probeTO:   opts.ProbeTimeout,
		startedAt: opts.StartedAt,
	}
	if s.domain == "" {
		s.domain = "localapp"
	}
	if s.listeners == nil {
		s.listeners = map[string]string{}
	}
	if s.probeTO <= 0 {
		s.probeTO = registry.DefaultProbeTimeout
	}
	if s.startedAt.IsZero() {
		s.startedAt = time.Now()
	}
	return s
}

// Listen opens the Unix domain socket with mode 0600.
// An existing socket file is removed and reused only when it accepts no
// connections.
func Listen(socketPath string) (net.Listener, error) {
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating state directory (%s): %w", dir, err)
	}
	if _, err := os.Stat(socketPath); err == nil {
		if c, derr := net.DialTimeout("unix", socketPath, 200*time.Millisecond); derr == nil {
			c.Close()
			return nil, fmt.Errorf("another daemon is already using %s", socketPath)
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, fmt.Errorf("removing stale socket (%s): %w", socketPath, err)
		}
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listening on socket (%s): %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("setting socket permissions (%s): %w", socketPath, err)
	}
	return ln, nil
}

// Serve serves the API on ln. Cancelling ctx shuts it down gracefully.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

// ServeHTTP routes the request. The authority part of the URL is ignored.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(r.URL.Path)

	switch {
	case len(parts) == 2 && parts[0] == APIVersion && parts[1] == "status":
		if !allow(w, r, http.MethodGet) {
			return
		}
		s.handleStatus(w)

	case len(parts) == 2 && parts[0] == APIVersion && parts[1] == "apps":
		if !allow(w, r, http.MethodGet) {
			return
		}
		s.handleListApps(w)

	case len(parts) == 3 && parts[0] == APIVersion && parts[1] == "apps":
		if !allow(w, r, http.MethodGet, http.MethodDelete) {
			return
		}
		if r.Method == http.MethodGet {
			s.handleGetApp(w, parts[2])
		} else {
			s.handleDeleteApp(w, parts[2])
		}

	case len(parts) == 5 && parts[0] == APIVersion && parts[1] == "apps" && parts[3] == "services":
		if !allow(w, r, http.MethodPut, http.MethodDelete) {
			return
		}
		if r.Method == http.MethodPut {
			s.handlePutService(w, r, parts[2], parts[4])
		} else {
			s.handleDeleteService(w, parts[2], parts[4])
		}

	default:
		writeError(w, http.StatusNotFound, CodeNotFound, "no such endpoint")
	}
}

func (s *Server) handleStatus(w http.ResponseWriter) {
	apps, services := s.store.Counts()
	writeJSON(w, http.StatusOK, StatusResponse{
		Version:   s.version,
		UptimeSec: int64(time.Since(s.startedAt).Seconds()),
		Domain:    s.domain,
		Listeners: s.listeners,
		Apps:      apps,
		Services:  services,
	})
}

func (s *Server) handleListApps(w http.ResponseWriter) {
	views := s.appViews(s.store.Apps())
	writeJSON(w, http.StatusOK, AppsResponse{Apps: views})
}

func (s *Server) handleGetApp(w http.ResponseWriter, app string) {
	if err := registry.ValidateName("app", app); err != nil {
		writeErr(w, err)
		return
	}
	a, ok := s.store.App(app)
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "app not found: "+app)
		return
	}
	writeJSON(w, http.StatusOK, s.appViews([]registry.App{a})[0])
}

func (s *Server) handlePutService(w http.ResponseWriter, r *http.Request, app, service string) {
	if err := registry.ValidateName("app", app); err != nil {
		writeErr(w, err)
		return
	}
	if err := registry.ValidateName("service", service); err != nil {
		writeErr(w, err)
		return
	}

	var req ServiceRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, "request body must be a JSON object: "+err.Error())
		return
	}
	if req.Port == nil {
		writeError(w, http.StatusBadRequest, registry.CodeInvalidPort, `"port" is required`)
		return
	}

	svc := registry.Service{
		Name:      service,
		Port:      *req.Port,
		Path:      req.Path,
		StripPath: req.StripPath,
		PID:       req.PID,
	}
	saved, err := s.store.Put(app, svc)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.serviceView(app, saved, registry.ServiceStatus(saved, s.probeTO)))
}

func (s *Server) handleDeleteApp(w http.ResponseWriter, app string) {
	if err := registry.ValidateName("app", app); err != nil {
		writeErr(w, err)
		return
	}
	removed, err := s.store.RemoveApp(app)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, CodeNotFound, "app not found: "+app)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteService(w http.ResponseWriter, app, service string) {
	if err := registry.ValidateName("app", app); err != nil {
		writeErr(w, err)
		return
	}
	if err := registry.ValidateName("service", service); err != nil {
		writeErr(w, err)
		return
	}
	removed, err := s.store.RemoveService(app, service)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, CodeNotFound, "service not found: "+app+"/"+service)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// appViews converts registry Apps into their API representation. TCP liveness
// probes are deduplicated per port and run concurrently; only the process
// liveness check of services with a `--pid` is done per service.
func (s *Server) appViews(apps []registry.App) []AppView {
	seen := map[int]bool{}
	var ports []int
	for _, a := range apps {
		for _, svc := range a.Services {
			if !seen[svc.Port] {
				seen[svc.Port] = true
				ports = append(ports, svc.Port)
			}
		}
	}

	// Each goroutine writes only its own index, so a WaitGroup is enough
	// synchronization.
	probed := make([]string, len(ports))
	var wg sync.WaitGroup
	for i, port := range ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probed[i] = s.probeStatus(port)
		}()
	}
	wg.Wait()

	status := make(map[int]string, len(ports))
	for i, port := range ports {
		status[port] = probed[i]
	}

	views := make([]AppView, 0, len(apps))
	for _, a := range apps {
		av := AppView{Name: a.Name, Services: make([]ServiceView, 0, len(a.Services))}
		for _, svc := range a.Services {
			st := status[svc.Port]
			// With a pid, a terminated process means down
			// (DESIGN.md "登録ライフサイクル").
			if !registry.ProcessAlive(svc.PID) {
				st = registry.StatusDown
			}
			av.Services = append(av.Services, s.serviceView(a.Name, svc, st))
		}
		views = append(views, av)
	}
	return views
}

func (s *Server) serviceView(app string, svc registry.Service, status string) ServiceView {
	return ServiceView{
		App:       app,
		Service:   svc.Name,
		Port:      svc.Port,
		Path:      svc.Path,
		StripPath: svc.StripPath,
		PID:       svc.PID,
		Status:    status,
		URLs:      svc.URLs(app, s.domain),
	}
}

func (s *Server) probeStatus(port int) string { return registry.Status(port, s.probeTO) }

// splitPath breaks a URL path into segments, dropping empty ones.
func splitPath(p string) []string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	out := parts[:0]
	for _, x := range parts {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// allow checks the method and, when it is not allowed, writes 405 and returns
// false.
func allow(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, m := range methods {
		if r.Method == m {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed: "+r.Method)
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}

// writeErr splits errors into validation failures (400) and internal errors
// (500).
func writeErr(w http.ResponseWriter, err error) {
	var ve *registry.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, ve.Code, ve.Message)
		return
	}
	writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
}
