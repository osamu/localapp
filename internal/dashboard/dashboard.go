// Package dashboard provides the listing page served at the apex
// (`https://<domain>/`).
//
// It returns plain HTML only (no JS framework, no external assets). It is
// read-only and offers no mutating operations. Registered values are printed
// through the automatic escaping of `html/template`
// (DESIGN.md "セキュリティ", the row about printing registered values).
//
// The dashboard is a convenience on top of the core and is not extended beyond
// this (DESIGN.md "拡張ポイント").
package dashboard

import (
	"bytes"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/osamu/localapp/internal/registry"
)

// Store is the registry the dashboard reads. *registry.Store satisfies it.
type Store interface {
	Apps() []registry.App
}

// Options configures a Handler.
type Options struct {
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
	store     Store
	domain    string
	version   string
	listeners map[string]string
	probeTO   time.Duration
}

// New builds a Handler.
func New(store Store, opts Options) *Handler {
	h := &Handler{
		store:     store,
		domain:    opts.Domain,
		version:   opts.Version,
		listeners: opts.Listeners,
		probeTO:   opts.ProbeTimeout,
	}
	if h.domain == "" {
		h.domain = "localapp"
	}
	if h.probeTO <= 0 {
		h.probeTO = registry.DefaultProbeTimeout
	}
	return h
}

// ServeHTTP returns the listing page. Being read-only, it accepts GET and HEAD
// only.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		render(w, http.StatusMethodNotAllowed, pageData{
			Domain:  h.domain,
			Heading: "This page is read-only",
			Message: "The dashboard has no mutating operations. Change registrations with the `localapp` command or the API on control.sock.",
		})
		return
	}
	if r.URL.Path != "/" {
		render(w, http.StatusNotFound, pageData{
			Domain:  h.domain,
			Heading: "No such page",
			Message: "The dashboard serves / only.",
			Hint:    "The page of each app is https://<app>." + h.domain + "/.",
		})
		return
	}
	render(w, http.StatusOK, h.data())
}

// pageData is the rendering data passed to the template.
type pageData struct {
	Domain    string
	Version   string
	Heading   string
	Message   string
	Hint      string
	Apps      []appView
	Services  int
	Up        int
	Listeners []listenerView
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
	Up   bool
	URLs []string
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
				Name:   svc.Name,
				Port:   svc.Port,
				Path:   registry.NormalizePath(svc.Path),
				PID:    svc.PID,
				Status: status,
				Up:     status == registry.StatusUp,
				URLs:   svc.URLs(a.Name, h.domain),
			})
		}
		d.Apps = append(d.Apps, av)
	}
	return d
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
  footer { margin-top: 2.5rem; opacity: .5; font-size: .8rem; }
</style>
</head>
<body>
<main>
{{if .Heading}}
  <h1>{{.Heading}}</h1>
  {{if .Message}}<p>{{.Message}}</p>{{end}}
  {{if .Hint}}<p class="hint">{{.Hint}}</p>{{end}}
{{else}}
  <h1>{{.Domain}}</h1>
  <p class="sub">{{len .Apps}} apps / {{.Services}} services ({{.Up}} up)</p>

  {{if .Apps}}
  <table>
    <thead>
      <tr><th>app/service</th><th>status</th><th>target</th><th>URL</th></tr>
    </thead>
    <tbody>
    {{range .Apps}}{{$app := .Name}}{{range .Services}}
      <tr>
        <td class="mono">{{$app}}/{{.Name}}</td>
        <td><span class="status {{if .Up}}up{{else}}down{{end}}">{{.Status}}</span></td>
        <td class="mono">localhost:{{.Port}}{{if .Path}}<br>path {{.Path}}{{end}}{{if .PID}}<br>pid {{.PID}}{{end}}</td>
        <td><ul>{{range .URLs}}<li><a href="{{.}}">{{.}}</a></li>{{end}}</ul></td>
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
  <p class="hint">This page is read-only. Change registrations with <code>localapp add</code> / <code>localapp rm</code>.</p>
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
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
