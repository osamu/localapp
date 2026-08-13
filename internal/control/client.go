package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Client is the client of the Control Plane API. The CLI calls the API through
// it.
type Client struct {
	socketPath string
	http       *http.Client
}

// NewClient builds an API client over a Unix socket.
func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
			// Generous, because the daemon side also runs liveness probes.
			Timeout: 10 * time.Second,
		},
	}
}

// APIError is a unified error returned by the API.
type APIError struct {
	HTTPStatus int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("HTTP %d", e.HTTPStatus)
	}
	return e.Code + ": " + e.Message
}

// ErrUnavailable reports that the daemon cannot be reached.
var ErrUnavailable = errors.New("cannot connect to the localapp daemon")

// IsNotFound reports whether the API returned 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.HTTPStatus == http.StatusNotFound
}

// do performs one request and returns the raw response body.
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	// The authority part is ignored by the server (DESIGN.md "全体規約").
	req, err := http.NewRequestWithContext(ctx, method, "http://localapp"+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			var oerr *net.OpError
			if errors.As(uerr.Err, &oerr) {
				return nil, fmt.Errorf("%w (%s)", ErrUnavailable, c.socketPath)
			}
		}
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode >= 400 {
		ae := &APIError{HTTPStatus: resp.StatusCode}
		var eb ErrorBody
		if json.Unmarshal(raw, &eb) == nil {
			ae.Code = eb.Error.Code
			ae.Message = eb.Error.Message
		}
		return raw, ae
	}
	return raw, nil
}

// Status calls GET /v1/status. It also returns the raw JSON (for --json output).
func (c *Client) Status(ctx context.Context) (StatusResponse, []byte, error) {
	raw, err := c.do(ctx, http.MethodGet, "/v1/status", nil)
	if err != nil {
		return StatusResponse{}, raw, err
	}
	var out StatusResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return StatusResponse{}, raw, fmt.Errorf("decoding response: %w", err)
	}
	return out, raw, nil
}

// ListApps calls GET /v1/apps.
func (c *Client) ListApps(ctx context.Context) (AppsResponse, []byte, error) {
	raw, err := c.do(ctx, http.MethodGet, "/v1/apps", nil)
	if err != nil {
		return AppsResponse{}, raw, err
	}
	var out AppsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return AppsResponse{}, raw, fmt.Errorf("decoding response: %w", err)
	}
	return out, raw, nil
}

// GetApp calls GET /v1/apps/{app}.
func (c *Client) GetApp(ctx context.Context, app string) (AppView, []byte, error) {
	raw, err := c.do(ctx, http.MethodGet, "/v1/apps/"+app, nil)
	if err != nil {
		return AppView{}, raw, err
	}
	var out AppView
	if err := json.Unmarshal(raw, &out); err != nil {
		return AppView{}, raw, fmt.Errorf("decoding response: %w", err)
	}
	return out, raw, nil
}

// PutService calls PUT /v1/apps/{app}/services/{service} (idempotent upsert).
func (c *Client) PutService(ctx context.Context, app, service string, req ServiceRequest) (ServiceView, []byte, error) {
	raw, err := c.do(ctx, http.MethodPut, "/v1/apps/"+app+"/services/"+service, req)
	if err != nil {
		return ServiceView{}, raw, err
	}
	var out ServiceView
	if err := json.Unmarshal(raw, &out); err != nil {
		return ServiceView{}, raw, fmt.Errorf("decoding response: %w", err)
	}
	return out, raw, nil
}

// DeleteApp calls DELETE /v1/apps/{app}.
func (c *Client) DeleteApp(ctx context.Context, app string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/apps/"+app, nil)
	return err
}

// DeleteService calls DELETE /v1/apps/{app}/services/{service}.
func (c *Client) DeleteService(ctx context.Context, app, service string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/apps/"+app+"/services/"+service, nil)
	return err
}
