// Package httpapi is a thin read-only HTTP client shared by the log, trace,
// and any future query-API tool group. Every outbound request flows through
// transport.ReadOnlyRoundTripper so the GET/HEAD/OPTIONS contract is
// enforced uniformly regardless of which backend the caller wraps.
//
// The package is intentionally small: it does not parse responses, does not
// retry, and does not know about any specific backend's payload shape.
// Callers compose RawGet with per-backend decoders.
package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/transport"
)

// Auth describes how the client authenticates with a backend. At most one of
// Bearer or Basic should be configured; both empty means unauthenticated.
type Auth struct {
	// BearerEnv is the environment variable holding a Bearer token.
	BearerEnv string
	// BasicUser is the HTTP Basic Auth username.
	BasicUser string
	// BasicPassEnv is the environment variable holding the Basic Auth password.
	BasicPassEnv string
}

// Client is a read-only HTTP client bound to a single base URL. It enforces
// the transport-layer GET/HEAD/OPTIONS whitelist via transport.New.
type Client struct {
	Name    string
	BaseURL string
	http    *http.Client
}

// NewClient builds a Client. baseURL must be non-empty; the trailing slash is
// stripped. auth selects between Bearer / Basic / unauthenticated based on
// which fields are populated. Credentials are read from the environment at
// construction time so a missing env var does not silently change auth mode.
func NewClient(name, baseURL string, auth Auth) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("httpapi: baseURL is required")
	}
	baseURL = strings.TrimRight(baseURL, "/")

	rt := transport.New(nil)
	var wrapped http.RoundTripper = rt

	switch {
	case auth.BearerEnv != "":
		token := os.Getenv(auth.BearerEnv)
		if token != "" {
			wrapped = &bearerTripper{inner: rt, token: token}
		}
	case auth.BasicUser != "":
		pass := ""
		if auth.BasicPassEnv != "" {
			pass = os.Getenv(auth.BasicPassEnv)
		}
		wrapped = &basicTripper{inner: rt, user: auth.BasicUser, pass: pass}
	}

	return &Client{
		Name:    name,
		BaseURL: baseURL,
		http:    &http.Client{Transport: wrapped, Timeout: 15 * time.Second},
	}, nil
}

// RawGet issues a GET to path under the client's base URL and returns the
// raw body. Non-2xx responses include the body in the returned error so the
// caller can surface backend-specific error JSON to the LLM.
func (c *Client) RawGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
	u := c.BaseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("httpapi: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("httpapi: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
	if err != nil {
		return nil, fmt.Errorf("httpapi: read body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("httpapi: HTTP %d from %s: %s", resp.StatusCode, path, body)
	}
	return body, nil
}

// Ping issues a GET to path and returns nil iff the response is 2xx. Used by
// probes to decide whether a backend is reachable enough to register tools.
func (c *Client) Ping(ctx context.Context, path string) error {
	_, err := c.RawGet(ctx, path, nil)
	return err
}

// bearerTripper injects an Authorization: Bearer header.
type bearerTripper struct {
	inner http.RoundTripper
	token string
}

func (b *bearerTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+b.token)
	return b.inner.RoundTrip(r2)
}

// basicTripper injects HTTP Basic Auth credentials.
type basicTripper struct {
	inner      http.RoundTripper
	user, pass string
}

func (b *basicTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.SetBasicAuth(b.user, b.pass)
	return b.inner.RoundTrip(r2)
}
