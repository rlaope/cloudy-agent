// Package prom provides read-only Prometheus tools for the cloudy SRE agent.
//
// All HTTP traffic from this package flows through transport.New so that the
// read-only contract (GET/HEAD/OPTIONS only) is enforced at the transport
// layer, in addition to any network-level controls on the Prometheus endpoint.
package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/tools/httpapi"
	"github.com/rlaope/cloudy/internal/transport"
)

// Result holds a decoded Prometheus API response.
type Result struct {
	// ResultType is "matrix", "vector", "scalar", or "string".
	ResultType string
	// Vector holds instant-query results (ResultType == "vector").
	Vector []Sample
	// Matrix holds range-query results (ResultType == "matrix").
	Matrix []Series
	// Scalar holds a scalar result.
	Scalar *Sample
}

// Sample is a single (label-set, value) pair from an instant query.
type Sample struct {
	Labels map[string]string
	// Value is the float64 value at the queried timestamp.
	Value float64
	// Timestamp is the Unix timestamp of the sample.
	Timestamp float64
}

// Series is a labelled time series from a range query.
type Series struct {
	Labels map[string]string
	// Values is the sequence of (timestamp, value) pairs.
	Values [][2]float64
}

// Client is a read-only Prometheus HTTP client. Every request is dispatched
// through transport.ReadOnlyRoundTripper so mutating calls are rejected.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient constructs a Client. Exactly one of basicUser+basicPass or bearer
// should be non-empty; both empty means unauthenticated. The underlying
// http.Transport is wrapped by transport.New to enforce the read-only contract.
func NewClient(baseURL, basicUser, basicPass, bearer string) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("prom: baseURL is required")
	}
	// Normalise: strip trailing slash.
	baseURL = strings.TrimRight(baseURL, "/")

	rt := transport.New(nil) // wraps http.DefaultTransport
	var wrapped http.RoundTripper = rt
	if bearer != "" {
		wrapped = &httpapi.BearerTripper{Inner: rt, Token: bearer}
	} else if basicUser != "" {
		wrapped = &httpapi.BasicTripper{Inner: rt, User: basicUser, Pass: basicPass}
	}

	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Transport: wrapped},
	}, nil
}

// Query executes an instant PromQL query at time t (zero = now).
func (c *Client) Query(ctx context.Context, query string, t time.Time) (*Result, error) {
	if err := checkPromQL(query); err != nil {
		return nil, err
	}
	params := url.Values{"query": {query}}
	if !t.IsZero() {
		params.Set("time", fmt.Sprintf("%g", float64(t.UnixNano())/1e9))
	}
	return c.apiGet(ctx, "/api/v1/query", params)
}

// QueryRange executes a range PromQL query.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*Result, error) {
	if err := checkPromQL(query); err != nil {
		return nil, err
	}
	params := url.Values{
		"query": {query},
		"start": {fmt.Sprintf("%g", float64(start.UnixNano())/1e9)},
		"end":   {fmt.Sprintf("%g", float64(end.UnixNano())/1e9)},
		"step":  {fmt.Sprintf("%gs", step.Seconds())},
	}
	return c.apiGet(ctx, "/api/v1/query_range", params)
}

// LabelValues returns all values for a label, optionally filtered by matchers
// and restricted to [start, end].
func (c *Client) LabelValues(ctx context.Context, label string, match []string, start, end time.Time) ([]string, error) {
	if label == "" {
		return nil, fmt.Errorf("prom: label is required")
	}
	params := url.Values{}
	for _, m := range match {
		params.Add("match[]", m)
	}
	if !start.IsZero() {
		params.Set("start", fmt.Sprintf("%g", float64(start.UnixNano())/1e9))
	}
	if !end.IsZero() {
		params.Set("end", fmt.Sprintf("%g", float64(end.UnixNano())/1e9))
	}

	path := "/api/v1/label/" + url.PathEscape(label) + "/values"
	raw, err := c.rawGet(ctx, path, params)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("prom: decode label values: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prom: API error for label values")
	}
	return resp.Data, nil
}

// Series returns metadata for series matching the given selectors.
func (c *Client) Series(ctx context.Context, matchers []string, start, end time.Time) ([]map[string]string, error) {
	params := url.Values{}
	for _, m := range matchers {
		params.Add("match[]", m)
	}
	if !start.IsZero() {
		params.Set("start", fmt.Sprintf("%g", float64(start.UnixNano())/1e9))
	}
	if !end.IsZero() {
		params.Set("end", fmt.Sprintf("%g", float64(end.UnixNano())/1e9))
	}

	raw, err := c.rawGet(ctx, "/api/v1/series", params)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Status string              `json:"status"`
		Data   []map[string]string `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("prom: decode series: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("prom: API error for series")
	}
	return resp.Data, nil
}

// apiGet issues a GET request to the Prometheus HTTP API and decodes the
// common {status, data: {resultType, result}} envelope.
func (c *Client) apiGet(ctx context.Context, path string, params url.Values) (*Result, error) {
	raw, err := c.rawGet(ctx, path, params)
	if err != nil {
		return nil, err
	}
	return decodeAPIResponse(raw)
}

func (c *Client) rawGet(ctx context.Context, path string, params url.Values) ([]byte, error) {
	u := c.baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("prom: build request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prom: HTTP GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("prom: read response body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("prom: HTTP %d from %s: %s", resp.StatusCode, path, body)
	}
	return body, nil
}

// decodeAPIResponse parses the Prometheus HTTP API envelope.
func decodeAPIResponse(raw []byte) (*Result, error) {
	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("prom: decode envelope: %w", err)
	}
	if envelope.Status != "success" {
		return nil, fmt.Errorf("prom: API error: %s", envelope.Error)
	}

	var dataEnv struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(envelope.Data, &dataEnv); err != nil {
		return nil, fmt.Errorf("prom: decode data: %w", err)
	}

	res := &Result{ResultType: dataEnv.ResultType}

	switch dataEnv.ResultType {
	case "vector":
		var items []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]json.Number    `json:"value"`
		}
		if err := json.Unmarshal(dataEnv.Result, &items); err != nil {
			return nil, fmt.Errorf("prom: decode vector: %w", err)
		}
		for _, it := range items {
			ts, _ := it.Value[0].Float64()
			val, _ := it.Value[1].Float64()
			res.Vector = append(res.Vector, Sample{Labels: it.Metric, Timestamp: ts, Value: val})
		}

	case "matrix":
		var items []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]json.Number  `json:"values"`
		}
		if err := json.Unmarshal(dataEnv.Result, &items); err != nil {
			return nil, fmt.Errorf("prom: decode matrix: %w", err)
		}
		for _, it := range items {
			s := Series{Labels: it.Metric}
			for _, v := range it.Values {
				ts, _ := v[0].Float64()
				val, _ := v[1].Float64()
				s.Values = append(s.Values, [2]float64{ts, val})
			}
			res.Matrix = append(res.Matrix, s)
		}

	case "scalar":
		var pair [2]json.Number
		if err := json.Unmarshal(dataEnv.Result, &pair); err != nil {
			return nil, fmt.Errorf("prom: decode scalar: %w", err)
		}
		ts, _ := pair[0].Float64()
		val, _ := pair[1].Float64()
		res.Scalar = &Sample{Timestamp: ts, Value: val}
	}

	return res, nil
}

// checkPromQL performs a minimal syntax check on a PromQL expression.
// It rejects empty strings and unbalanced parentheses/braces.
func checkPromQL(query string) error {
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("prom: empty PromQL query")
	}
	depth := 0
	for _, r := range query {
		switch r {
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
			if depth < 0 {
				return fmt.Errorf("prom: unbalanced brackets in PromQL query")
			}
		}
	}
	if depth != 0 {
		return fmt.Errorf("prom: unbalanced brackets in PromQL query")
	}
	return nil
}
