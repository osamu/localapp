// Package control provides the Control Plane API (HTTP+JSON) over a Unix
// socket and its client. The CLI is a thin client of this API
// (DESIGN.md "Control Plane API").
package control

// APIVersion is the version prefix of the URL.
const APIVersion = "v1"

// ServiceView is the API representation of a service. status / urls are
// derived by the server.
type ServiceView struct {
	App       string   `json:"app"`
	Service   string   `json:"service"`
	Port      int      `json:"port"`
	Path      string   `json:"path,omitempty"`
	StripPath bool     `json:"strip_path"`
	PID       int      `json:"pid,omitempty"`
	Status    string   `json:"status"`
	URLs      []string `json:"urls"`
}

// AppView is the API representation of an app.
type AppView struct {
	Name     string        `json:"name"`
	Services []ServiceView `json:"services"`
}

// AppsResponse is the response of GET /v1/apps.
type AppsResponse struct {
	Apps []AppView `json:"apps"`
}

// StatusResponse is the response of GET /v1/status.
type StatusResponse struct {
	Version   string            `json:"version"`
	UptimeSec int64             `json:"uptime_sec"`
	Domain    string            `json:"domain"`
	Listeners map[string]string `json:"listeners"`
	Apps      int               `json:"apps"`
	Services  int               `json:"services"`
}

// ServiceRequest is the request body of
// PUT /v1/apps/{app}/services/{service}. Only port is required; status / urls
// are ignored when present.
type ServiceRequest struct {
	Port      *int   `json:"port"`
	Path      string `json:"path,omitempty"`
	StripPath bool   `json:"strip_path,omitempty"`
	PID       int    `json:"pid,omitempty"`
}

// ErrorBody is the unified error format (DESIGN.md "エラー").
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the breakdown of an error. code is a stable machine-readable
// identifier.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error codes.
const (
	CodeBadJSON          = "bad_json"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeInternal         = "internal"
)
