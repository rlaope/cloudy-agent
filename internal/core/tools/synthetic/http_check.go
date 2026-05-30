// Package synthetic provides active read-only probes — currently an HTTP
// uptime/latency check. Unlike the rest of cloudy, which passively reads
// telemetry that already exists, this group actively reaches out from the
// operator's vantage to answer "is this endpoint actually reachable, and how
// fast". Every request flows through transport.ReadOnlyRoundTripper, so only
// GET/HEAD probes are possible — the read-only contract holds.
package synthetic

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/transport"
)

const (
	defaultProbeTimeout = 10 * time.Second
	maxProbeTimeout     = 30 * time.Second
)

// newHTTPCheckTool builds synthetic.http_check.
func newHTTPCheckTool() tools.Tool {
	type args struct {
		URL            string `json:"url"`
		Method         string `json:"method"`
		ExpectStatus   int    `json:"expect_status"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	schema := tools.MustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":             map[string]any{"type": "string", "description": "Absolute http(s) URL to probe."},
			"method":          map[string]any{"type": "string", "description": "HTTP method: GET (default) or HEAD. Other methods are rejected — the probe is read-only.", "enum": []string{"GET", "HEAD"}},
			"expect_status":   map[string]any{"type": "integer", "description": "Exact status code that counts as healthy; 0 (default) accepts any 2xx."},
			"timeout_seconds": map[string]any{"type": "integer", "description": "Per-probe timeout in seconds (default 10, max 30).", "minimum": 1, "maximum": 30},
		},
		"required": []string{"url"},
	})
	return tools.Spec[args]{
		Name:        "synthetic.http_check",
		Description: "Actively probe an HTTP(S) endpoint from the operator's vantage and report whether it is reachable, its status code, response latency, redirect count, and (for HTTPS) the TLS certificate's days-to-expiry. Read-only — GET/HEAD only.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			return runHTTPCheck(ctx, a.URL, a.Method, a.ExpectStatus, a.TimeoutSeconds)
		},
	}.Build()
}

// probeResult is the outcome of one HTTP check.
type probeResult struct {
	URL       string `json:"url"`
	Method    string `json:"method"`
	Up        bool   `json:"up"`
	Status    int    `json:"status"`
	Expected  bool   `json:"expected"`
	LatencyMs int64  `json:"latency_ms"`
	Redirects int    `json:"redirects"`
	CertDays  *int   `json:"cert_days_to_expiry,omitempty"`
	Error     string `json:"error,omitempty"`
}

func runHTTPCheck(ctx context.Context, rawURL, method string, expectStatus, timeoutSeconds int) (tools.Observation, error) {
	if rawURL == "" {
		return tools.Observation{}, fmt.Errorf("synthetic.http_check: url is required")
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return tools.Observation{}, fmt.Errorf("synthetic.http_check: url must be an absolute http(s) URL")
	}

	method = strings.ToUpper(strings.TrimSpace(method))
	switch method {
	case "":
		method = http.MethodGet
	case http.MethodGet, http.MethodHead:
		// allowed
	default:
		return tools.Observation{}, fmt.Errorf("synthetic.http_check: method %q is not allowed — the probe is read-only (GET/HEAD)", method)
	}

	timeout := defaultProbeTimeout
	if timeoutSeconds > 0 {
		timeout = time.Duration(timeoutSeconds) * time.Second
		if timeout > maxProbeTimeout {
			timeout = maxProbeTimeout
		}
	}

	redirects := 0
	client := &http.Client{
		Transport: transport.New(nil),
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			redirects = len(via)
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("synthetic.http_check: build request: %w", err)
	}

	res := probeResult{URL: rawURL, Method: method, Redirects: redirects}
	start := time.Now()
	resp, err := client.Do(req)
	res.LatencyMs = time.Since(start).Milliseconds()
	res.Redirects = redirects
	if err != nil {
		// A transport-level failure (DNS, connection refused, timeout, TLS) is
		// a DOWN verdict, not a tool error — the agent should reason about it.
		res.Up = false
		res.Error = err.Error()
		return tools.Observation{Text: formatProbe(res), Table: tableProbe(res), Raw: res}, nil
	}
	defer func() { _ = resp.Body.Close() }()

	res.Up = true
	res.Status = resp.StatusCode
	if expectStatus > 0 {
		res.Expected = resp.StatusCode == expectStatus
	} else {
		res.Expected = resp.StatusCode/100 == 2
	}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		days := int(time.Until(resp.TLS.PeerCertificates[0].NotAfter).Hours() / 24)
		res.CertDays = &days
	}
	return tools.Observation{Text: formatProbe(res), Table: tableProbe(res), Raw: res}, nil
}

func probeVerdict(r probeResult) string {
	switch {
	case !r.Up:
		return "DOWN"
	case !r.Expected:
		return "UNEXPECTED"
	default:
		return "UP"
	}
}

func tableProbe(r probeResult) *render.Table {
	tbl := &render.Table{
		Headers: []string{"URL", "VERDICT", "STATUS", "LATENCY_MS", "REDIRECTS", "CERT_DAYS"},
		Aligns: []render.Align{
			render.AlignLeft, render.AlignLeft, render.AlignRight,
			render.AlignRight, render.AlignRight, render.AlignRight,
		},
	}
	status := "—"
	if r.Up {
		status = strconv.Itoa(r.Status)
	}
	cert := "—"
	if r.CertDays != nil {
		cert = strconv.Itoa(*r.CertDays)
	}
	tbl.Rows = append(tbl.Rows, []string{
		r.URL, probeVerdict(r), status,
		strconv.FormatInt(r.LatencyMs, 10),
		strconv.Itoa(r.Redirects), cert,
	})
	return tbl
}

func formatProbe(r probeResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s → %s", r.Method, r.URL, probeVerdict(r))
	if r.Up {
		fmt.Fprintf(&b, " (status=%d, %dms", r.Status, r.LatencyMs)
		if r.Redirects > 0 {
			fmt.Fprintf(&b, ", %d redirect(s)", r.Redirects)
		}
		if r.CertDays != nil {
			fmt.Fprintf(&b, ", cert expires in %dd", *r.CertDays)
		}
		b.WriteByte(')')
		if !r.Expected {
			b.WriteString(" — status did not match expectation")
		}
	} else {
		fmt.Fprintf(&b, " after %dms: %s", r.LatencyMs, r.Error)
	}
	return b.String()
}
