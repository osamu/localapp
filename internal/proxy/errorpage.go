package proxy

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strconv"

	"github.com/osamu/localapp/internal/registry"
)

// pageData is the rendering data of an error page.
// Every value is escaped automatically by html/template
// (DESIGN.md "Security").
type pageData struct {
	Title   string
	Heading string
	Message string
	// Rows holds the registration details as "label: value" pairs.
	Rows []pageRow
	// Links is the list of related URLs.
	Links []string
	Hint  string
}

type pageRow struct {
	Label string
	Value string
}

// pageTmpl is the template shared by every page. Styling is kept minimal.
var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 16px/1.6 -apple-system, BlinkMacSystemFont, "Helvetica Neue", sans-serif;
         margin: 0; padding: 3rem 1.5rem; display: flex; justify-content: center; }
  main { max-width: 40rem; width: 100%; }
  h1 { font-size: 1.3rem; margin: 0 0 .75rem; }
  p { margin: 0 0 1rem; }
  dl { display: grid; grid-template-columns: auto 1fr; gap: .25rem 1rem; margin: 0 0 1rem;
       padding: 1rem; border: 1px solid rgba(128,128,128,.35); border-radius: 6px; }
  dt { opacity: .7; }
  dd { margin: 0; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  ul { margin: 0 0 1rem; padding-left: 1.25rem; }
  code, li { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
  .hint { opacity: .7; font-size: .9rem; }
  footer { margin-top: 2rem; opacity: .5; font-size: .8rem; }
</style>
</head>
<body>
<main>
  <h1>{{.Heading}}</h1>
  {{if .Message}}<p>{{.Message}}</p>{{end}}
  {{if .Rows}}<dl>{{range .Rows}}<dt>{{.Label}}</dt><dd>{{.Value}}</dd>{{end}}</dl>{{end}}
  {{if .Links}}<ul>{{range .Links}}<li>{{.}}</li>{{end}}</ul>{{end}}
  {{if .Hint}}<p class="hint">{{.Hint}}</p>{{end}}
  <footer>localapp</footer>
</main>
</body>
</html>
`))

// renderPage renders the page and writes the response. It renders into a buffer
// first so that a template failure never emits partial HTML.
func renderPage(w http.ResponseWriter, status int, data pageData) {
	var buf bytes.Buffer
	if err := pageTmpl.Execute(&buf, data); err != nil {
		http.Error(w, data.Heading, status)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// serveUnavailable returns the 503 page shown when the target cannot be
// reached. A status-only response is not acceptable
// (DESIGN.md "Registration lifecycle").
func (p *Proxy) serveUnavailable(w http.ResponseWriter, rt route, detail string) {
	rows := []pageRow{
		{Label: "app", Value: rt.appName},
		{Label: "service", Value: rt.svcName},
		{Label: "target", Value: "localhost:" + strconv.Itoa(rt.svc.Port)},
	}
	if mount := registry.NormalizePath(rt.svc.Path); mount != "" {
		rows = append(rows, pageRow{Label: "path", Value: mount})
	}
	if rt.svc.PID > 0 {
		state := "running"
		if !registry.ProcessAlive(rt.svc.PID) {
			state = "exited"
		}
		rows = append(rows, pageRow{Label: "pid", Value: strconv.Itoa(rt.svc.PID) + " (" + state + ")"})
	}
	if detail != "" {
		rows = append(rows, pageRow{Label: "error", Value: detail})
	}
	renderPage(w, http.StatusServiceUnavailable, pageData{
		Title:   "503 " + rt.appName + "/" + rt.svcName,
		Heading: "Cannot reach the target",
		Message: fmt.Sprintf(
			"%s/%s is registered on localhost:%d, but no process is listening on that port.",
			rt.appName, rt.svcName, rt.svc.Port),
		Rows:  rows,
		Links: rt.svc.URLs(rt.appName, p.domain),
		Hint: fmt.Sprintf(
			"Start the dev server and reload. If the port changed, update the registration with `localapp add <port> --app %s --service %s`.",
			rt.appName, rt.svcName),
	})
}

// serveNotFound returns the 404 page for a host / service that could not be
// resolved.
func (p *Proxy) serveNotFound(w http.ResponseWriter, rt route) {
	data := pageData{Title: "404 " + rt.host, Rows: []pageRow{{Label: "host", Value: rt.host}}}

	switch rt.kind {
	case routeUnknownHost:
		data.Heading = "This host is not managed by localapp"
		data.Message = fmt.Sprintf("localapp handles the forms `<app>.%s` and `<service>.<app>.%s`.", p.domain, p.domain)
		data.Links = p.appLinks()
		data.Hint = "List the registered apps with `localapp ls`."

	case routeUnknownApp:
		data.Heading = "The app is not registered"
		data.Message = fmt.Sprintf("app `%s` is not in the registry.", rt.appName)
		data.Links = p.appLinks()
		data.Hint = fmt.Sprintf("Register it with `localapp add <port> --app %s`.", rt.appName)

	case routeUnknownService:
		data.Heading = "The service is not registered"
		data.Message = fmt.Sprintf("app `%s` has no service `%s`.", rt.appName, rt.svcName)
		data.Rows = append(data.Rows, pageRow{Label: "app", Value: rt.appName})
		data.Links = p.serviceLinks(rt.app)
		data.Hint = fmt.Sprintf("Register it with `localapp add <port> --app %s --service %s`.", rt.appName, rt.svcName)

	case routeNoDefault:
		data.Heading = "The default service is not registered"
		data.Message = fmt.Sprintf(
			"No service of app `%s` matches the path, and the default service `%s` is not registered either.",
			rt.appName, registry.DefaultService)
		data.Rows = append(data.Rows, pageRow{Label: "app", Value: rt.appName})
		data.Links = p.serviceLinks(rt.app)
		data.Hint = fmt.Sprintf("Register the default service with `localapp add <port> --app %s`.", rt.appName)

	default:
		data.Heading = "Not found"
	}

	renderPage(w, http.StatusNotFound, data)
}

// appLinks returns the representative URLs of the registered apps.
func (p *Proxy) appLinks() []string {
	var links []string
	for _, app := range p.store.Apps() {
		links = append(links, p.serviceLinks(app)...)
	}
	return links
}

// serviceLinks returns the URLs of every service of an app.
func (p *Proxy) serviceLinks(app registry.App) []string {
	var links []string
	for _, svc := range app.Services {
		links = append(links, svc.URLs(app.Name, p.domain)...)
	}
	return links
}
