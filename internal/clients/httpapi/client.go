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
	// TokenEnv is the environment variable holding a raw token injected as
	// "Authorization: <TokenScheme><token>". For APIs whose scheme is neither
	// Bearer nor Basic — e.g. PagerDuty's "Token token=<key>".
	TokenEnv string
	// TokenScheme is the Authorization-header prefix used with TokenEnv,
	// including any trailing space or "=" (e.g. "Token token="). Empty defaults
	// to "Bearer ".
	TokenScheme string
}

// Client is a read-only HTTP client bound to a single base URL. It enforces
// the transport-layer GET/HEAD/OPTIONS whitelist via transport.New.
type Client struct {
	Name    string
	BaseURL string
	// Accept overrides the request Accept header. Empty = "application/json".
	// PagerDuty's REST v2 wants "application/vnd.pagerduty+json;version=2", so
	// per-backend clients can set the vendor media type without changing the
	// shared default the log/trace/alert/gitops callers rely on.
	Accept string
	http   *http.Client
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
	wrapped := WrapAuth(rt, auth)

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
	accept := c.Accept
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)

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

// BearerTripper injects an Authorization: Bearer header.
type BearerTripper struct {
	Inner http.RoundTripper
	Token string
}

func (b *BearerTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+b.Token)
	return b.Inner.RoundTrip(r2)
}

// BasicTripper injects HTTP Basic Auth credentials.
type BasicTripper struct {
	Inner http.RoundTripper
	User  string
	Pass  string
}

func (b *BasicTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.SetBasicAuth(b.User, b.Pass)
	return b.Inner.RoundTrip(r2)
}

// TokenTripper injects a verbatim Authorization header value. Used for schemes
// that are neither Bearer nor Basic (e.g. PagerDuty's "Token token=<key>").
type TokenTripper struct {
	Inner  http.RoundTripper
	Header string
}

func (t *TokenTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", t.Header)
	return t.Inner.RoundTrip(r2)
}

// WrapAuth wraps inner with the auth tripper selected by env-resolved
// credentials in auth. Returns inner unchanged when no auth is configured
// or when the configured env var is empty.
func WrapAuth(inner http.RoundTripper, auth Auth) http.RoundTripper {
	switch {
	case auth.TokenEnv != "":
		if token := os.Getenv(auth.TokenEnv); token != "" {
			scheme := auth.TokenScheme
			if scheme == "" {
				scheme = "Bearer "
			}
			return &TokenTripper{Inner: inner, Header: scheme + token}
		}
	case auth.BearerEnv != "":
		if token := os.Getenv(auth.BearerEnv); token != "" {
			return &BearerTripper{Inner: inner, Token: token}
		}
	case auth.BasicUser != "":
		pass := ""
		if auth.BasicPassEnv != "" {
			pass = os.Getenv(auth.BasicPassEnv)
		}
		return &BasicTripper{Inner: inner, User: auth.BasicUser, Pass: pass}
	}
	return inner
}
