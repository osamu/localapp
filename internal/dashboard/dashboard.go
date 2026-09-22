// Package dashboard provides the listing page served at the apex
// (`https://<domain>/`).
//
// It uses server-rendered HTML and a small log preview script (no external assets). Registered
// values are printed through the automatic escaping of `html/template`
// (DESIGN.md "Security", the row about printing registered values).
//
// The dashboard is a convenience on top of the core and is not extended beyond
// this (DESIGN.md "Extension points").
package dashboard

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/osamu/localapp/internal/logstream"
	"github.com/osamu/localapp/internal/registry"
)

// Store is the registry the dashboard reads. *registry.Store satisfies it.
type Store interface {
	Apps() []registry.App
	RemoveService(app, service string) (bool, error)
}

// Options configures a Handler.
type Options struct {
	Logs *logstream.Store
	// Domain is the domain suffix (default "localapp").
	Domain string
	// Version is the daemon version to display.
	Version string
	// Listeners is the listener map to display (the value of
	// config.Listeners()).
	Listeners map[string]string
	// ProbeTimeout is the TCP connect timeout of liveness probes. 0 means the
	// default.
	ProbeTimeout time.Duration
}

// Handler is the http.Handler of the listing page.
type Handler struct {
	logs      *logstream.Store
	streams   atomic.Int32 // open SSE log streams (bounded by maxLogStreams)
	store     Store
	domain    string
	version   string
	listeners map[string]string
	probeTO   time.Duration
	csrfToken string
}

// New builds a Handler.
func New(store Store, opts Options) *Handler {
	h := &Handler{
		store:     store,
		logs:      opts.Logs,
		domain:    opts.Domain,
		version:   opts.Version,
		listeners: opts.Listeners,
		probeTO:   opts.ProbeTimeout,
		csrfToken: newCSRFToken(),
	}
	if h.domain == "" {
		h.domain = "localapp"
	}
	if h.probeTO <= 0 {
		h.probeTO = registry.DefaultProbeTimeout
	}
	return h
}

// ServeHTTP returns the listing page and handles the confirmation and removal
// of an individual service mapping.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.methodNotAllowed(w, "GET, HEAD")
			return
		}
		render(w, http.StatusOK, h.data())
	case "/logs", "/logs/data", "/logs/stream":
		h.serveLogs(w, r)
	case "/delete":
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			h.confirmDelete(w, r)
		case http.MethodPost:
			h.deleteMapping(w, r)
		default:
			h.methodNotAllowed(w, "GET, HEAD, POST")
		}
	default:
		render(w, http.StatusNotFound, pageData{
			Domain:  h.domain,
			Heading: "No such page",
			Message: "Return to the dashboard to find apps and logs.",
			Hint:    "The page of each app is https://<app>." + h.domain + "/.",
		})
	}
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	render(w, http.StatusMethodNotAllowed, pageData{
		Domain: h.domain, Heading: "Method not allowed",
		Message: "This action does not support that request method.",
	})
}

func (h *Handler) confirmDelete(w http.ResponseWriter, r *http.Request) {
	app, service := r.URL.Query().Get("app"), r.URL.Query().Get("service")
	if !h.mappingExists(app, service) {
		render(w, http.StatusNotFound, pageData{
			Domain: h.domain, Heading: "Mapping not found",
			Message: "The requested mapping does not exist or has already been deleted.",
			Hint:    "Return to the dashboard and refresh the list.",
		})
		return
	}
	render(w, http.StatusOK, pageData{
		Domain: h.domain, ConfirmDelete: true, DeleteApp: app,
		DeleteService: service, CSRFToken: h.csrfToken,
	})
}

func (h *Handler) deleteMapping(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		render(w, http.StatusBadRequest, pageData{
			Domain: h.domain, Heading: "Invalid request", Message: "The delete request could not be read.",
		})
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Form.Get("csrf_token")), []byte(h.csrfToken)) != 1 {
		render(w, http.StatusForbidden, pageData{
			Domain: h.domain, Heading: "Delete request expired",
			Message: "Return to the dashboard and try deleting the mapping again.",
		})
		return
	}
	app, service := r.Form.Get("app"), r.Form.Get("service")
	if err := registry.ValidateName("app", app); err != nil {
		h.deleteError(w, http.StatusBadRequest, "Invalid mapping", err.Error())
		return
	}
	if err := registry.ValidateName("service", service); err != nil {
		h.deleteError(w, http.StatusBadRequest, "Invalid mapping", err.Error())
		return
	}
	removed, err := h.store.RemoveService(app, service)
	if err != nil {
		h.deleteError(w, http.StatusInternalServerError, "Delete failed", err.Error())
		return
	}
	if !removed {
		h.deleteError(w, http.StatusNotFound, "Mapping not found", "The mapping does not exist or has already been deleted.")
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) deleteError(w http.ResponseWriter, status int, heading, message string) {
	render(w, status, pageData{Domain: h.domain, Heading: heading, Message: message, Hint: "Return to the dashboard and try again."})
}

func (h *Handler) mappingExists(app, service string) bool {
	if registry.ValidateName("app", app) != nil || registry.ValidateName("service", service) != nil {
		return false
	}
	for _, a := range h.store.Apps() {
		if a.Name == app {
			for _, svc := range a.Services {
				if svc.Name == service {
					return true
				}
			}
		}
	}
	return false
}

func newCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("dashboard: generating CSRF token: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// pageData is the rendering data passed to the template.
type pageData struct {
	Domain        string
	Version       string
	Heading       string
	Message       string
	Hint          string
	Apps          []appView
	Services      int
	Up            int
	Listeners     []listenerView
	ConfirmDelete bool
	DeleteApp     string
	DeleteService string
	CSRFToken     string
}

type appView struct {
	Name     string
	Services []serviceView
}

type serviceView struct {
	Name   string
	Port   int
	Path   string
	PID    int
	Status string
	// Up reports whether the status is up. The template branches on it.
	Up        bool
	URLs      []string
	DeleteURL string
	LogsURL   string
}

type listenerView struct {
	Name string
	Addr string
}

// data assembles the rendering data from the registry. Liveness probes run
// concurrently, one per service.
func (h *Handler) data() pageData {
	apps := h.store.Apps()

	d := pageData{
		Domain:    h.domain,
		Version:   h.version,
		Listeners: listenerViews(h.listeners),
		Apps:      make([]appView, 0, len(apps)),
	}

	// Each goroutine writes only its own index, so a WaitGroup is enough
	// synchronization.
	type ref struct{ app, svc int }
	var refs []ref
	for i, a := range apps {
		for j := range a.Services {
			refs = append(refs, ref{i, j})
		}
	}
	statuses := make([]string, len(refs))
	var wg sync.WaitGroup
	for i, rf := range refs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = registry.ServiceStatus(apps[rf.app].Services[rf.svc], h.probeTO)
		}()
	}
	wg.Wait()

	statusOf := make(map[ref]string, len(refs))
	for i, rf := range refs {
		statusOf[rf] = statuses[i]
	}

	for i, a := range apps {
		av := appView{Name: a.Name, Services: make([]serviceView, 0, len(a.Services))}
		for j, svc := range a.Services {
			status := statusOf[ref{i, j}]
			d.Services++
			if status == registry.StatusUp {
				d.Up++
			}
			av.Services = append(av.Services, serviceView{
				Name:      svc.Name,
				Port:      svc.Port,
				Path:      registry.NormalizePath(svc.Path),
				PID:       svc.PID,
				Status:    status,
				Up:        status == registry.StatusUp,
				URLs:      svc.URLs(a.Name, h.domain),
				DeleteURL: deleteURL(a.Name, svc.Name),
				LogsURL:   "/logs?" + url.Values{"app": {a.Name}, "service": {svc.Name}}.Encode(),
			})
		}
		d.Apps = append(d.Apps, av)
	}
	return d
}

func deleteURL(app, service string) string {
	return "/delete?" + url.Values{"app": {app}, "service": {service}}.Encode()
}

// listenerViews orders the listeners by name.
func listenerViews(m map[string]string) []listenerView {
	out := make([]listenerView, 0, len(m))
	for k, v := range m {
		out = append(out, listenerView{Name: k, Addr: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// tmpl is the template of the listing page. It references no external asset and
// carries no script.
var tmpl = template.Must(template.New("dashboard").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Domain}}</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 16px/1.6 -apple-system, BlinkMacSystemFont, "Helvetica Neue", sans-serif;
         margin: 0; padding: 3rem 1.5rem; display: flex; justify-content: center; }
  main { max-width: 52rem; width: 100%; }
  h1 { font-size: 1.3rem; margin: 0 0 .25rem; }
  h2 { font-size: 1.05rem; margin: 2rem 0 .5rem; }
  p { margin: 0 0 1rem; }
  .sub { opacity: .7; font-size: .9rem; margin: 0 0 2rem; }
  table { border-collapse: collapse; width: 100%; margin: 0 0 1rem; }
  th, td { text-align: left; padding: .4rem .75rem .4rem 0; vertical-align: top;
           border-bottom: 1px solid rgba(128,128,128,.25); }
  th { font-weight: 600; opacity: .7; font-size: .85rem; }
  td.mono, code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  ul { margin: 0; padding-left: 1.1rem; }
  li { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .9rem; }
  .status { font-size: .8rem; padding: .05rem .5rem; border-radius: 999px;
            border: 1px solid rgba(128,128,128,.5); }
  .up { border-color: rgba(40,160,80,.6); color: rgb(30,130,65); }
  .down { border-color: rgba(190,70,70,.6); color: rgb(180,60,60); }
  .hint { opacity: .7; font-size: .9rem; }
  .actions { white-space: nowrap; }
  .delete { color: rgb(180,60,60); }
  .confirm { max-width: 34rem; padding: 1.25rem; border: 1px solid rgba(128,128,128,.35);
             border-radius: .6rem; }
  .confirm form { display: flex; gap: .75rem; align-items: center; }
  button { font: inherit; padding: .4rem .8rem; cursor: pointer; }
  footer { margin-top: 2.5rem; opacity: .5; font-size: .8rem; }
</style>
</head>
<body>
<main>
{{if .ConfirmDelete}}
  <h1>Delete mapping?</h1>
  <div class="confirm">
    <p><code>{{.DeleteApp}}/{{.DeleteService}}</code> will be removed from localapp.</p>
    <form method="post" action="/delete">
      <input type="hidden" name="app" value="{{.DeleteApp}}">
      <input type="hidden" name="service" value="{{.DeleteService}}">
      <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
      <button class="delete" type="submit">Delete mapping</button>
      <a href="/">Cancel</a>
    </form>
  </div>
{{else if .Heading}}
  <h1>{{.Heading}}</h1>
  {{if .Message}}<p>{{.Message}}</p>{{end}}
  {{if .Hint}}<p class="hint">{{.Hint}}</p>{{end}}
{{else}}
  <h1>{{.Domain}}</h1>
  <p class="sub">{{len .Apps}} apps / {{.Services}} services ({{.Up}} up)</p>

  {{if .Apps}}
  <table>
    <thead>
      <tr><th>app/service</th><th>status</th><th>target</th><th>URL</th><th></th></tr>
    </thead>
    <tbody>
    {{range .Apps}}{{$app := .Name}}{{range .Services}}
      <tr>
        <td class="mono">{{$app}}/{{.Name}}</td>
        <td><span class="status {{if .Up}}up{{else}}down{{end}}">{{.Status}}</span></td>
        <td class="mono">localhost:{{.Port}}{{if .Path}}<br>path {{.Path}}{{end}}{{if .PID}}<br>pid {{.PID}}{{end}}</td>
        <td><ul>{{range .URLs}}<li><a href="{{.}}">{{.}}</a></li>{{end}}</ul></td>
        <td class="actions"><a href="{{.LogsURL}}">Logs</a> · <a class="delete" href="{{.DeleteURL}}">Delete</a></td>
      </tr>
    {{end}}{{end}}
    </tbody>
  </table>
  {{else}}
  <p>No registrations yet.</p>
  <p class="hint">Run <code>localapp add &lt;port&gt;</code> in the directory of your dev server to register it.</p>
  {{end}}

  <h2>Daemon</h2>
  <table>
    <tbody>
      <tr><td>version</td><td class="mono">{{.Version}}</td></tr>
      <tr><td>domain</td><td class="mono">{{.Domain}}</td></tr>
      {{range .Listeners}}<tr><td>listener.{{.Name}}</td><td class="mono">{{.Addr}}</td></tr>{{end}}
    </tbody>
  </table>
  <p class="hint">Add or update registrations with <code>localapp add</code>. You can also remove them with <code>localapp rm</code>.</p>
{{end}}
  <footer>localapp</footer>
</main>
</body>
</html>
`))

// render renders the page and writes the response. It renders into a buffer
// first so that a template failure never emits partial HTML.
func render(w http.ResponseWriter, status int, data pageData) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "dashboard", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
