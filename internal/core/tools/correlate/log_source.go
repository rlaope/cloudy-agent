package correlate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	dockerclient "github.com/rlaope/cloudy/internal/clients/docker"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/change"
	tlog "github.com/rlaope/cloudy/internal/core/tools/log"
)

// esTimestampField is the document field the Elasticsearch log symptom source
// reads each hit's time from. @timestamp is the ECS/Filebeat/Logstash default;
// clusters using a different field (e.g. `time`, `ts`) should query ES via the
// log.es_search tool directly — see the esEvents doc.
const esTimestampField = "@timestamp"

// logSource folds log error spikes onto the change timeline as symptoms:
// ChangeEvents whose Kind is "log_error" and Source is "log". It draws from
// Loki and Elasticsearch backends — Docker container logs are already exposed
// by the separate log.container tool, so this source is a no-op when neither a
// Loki nor an ES client is configured.
type logSource struct {
	logs      tlog.Clients
	dockerHub *dockerclient.Hub
}

// newLogSource builds a logSource over the configured log backends. It returns
// nil — so callers can omit the source — when no Loki client, no Elasticsearch
// client, and no Docker hub is available to pull logs from.
func newLogSource(logs tlog.Clients, dockerHub *dockerclient.Hub) change.ChangeSource {
	if len(logs.Loki) == 0 && len(logs.ES) == 0 && dockerHub == nil {
		return nil
	}
	return &logSource{logs: logs, dockerHub: dockerHub}
}

func (s *logSource) Name() string { return "log" }

// RecentChanges scans recent logs for q.Workload and emits a "log_error"
// symptom event when error-level entries are found in the window, from
// whichever log backends are wired (Loki and/or Elasticsearch). Docker
// container logs are handled by the dedicated log.container tool. A deployment
// usually wires one log backend; when both are wired the source emits from
// each. Per-source errors are returned for the caller to tolerate.
func (s *logSource) RecentChanges(ctx context.Context, q change.ChangeQuery) ([]change.ChangeEvent, error) {
	since := q.Since
	if since == 0 {
		since = time.Hour
	}
	now := time.Now()
	start := now.Add(-since)

	var out []change.ChangeEvent

	if len(s.logs.Loki) > 0 {
		events, err := s.lokiEvents(ctx, q, start, now)
		if err != nil {
			return nil, err
		}
		out = append(out, events...)
	}

	if len(s.logs.ES) > 0 {
		events, err := s.esEvents(ctx, q, start, now)
		if err != nil {
			return nil, err
		}
		out = append(out, events...)
	}

	return out, nil
}

// lokiEvents scans recent Loki logs for q.Workload and emits a "log_error"
// symptom event when error-level lines are found. The Loki backend is chosen by
// deterministic default (PickDefaultEndpoint) rather than from q.Context, which
// carries the k8s context, not an endpoint name.
func (s *logSource) lokiEvents(ctx context.Context, q change.ChangeQuery, start, end time.Time) ([]change.ChangeEvent, error) {
	_, client, err := tools.PickDefaultEndpoint(s.logs.Loki, "correlate", "loki endpoint")
	if err != nil {
		return nil, err
	}

	// LogQL selector: {app="<workload>"} is the canonical k8s-via-Promtail
	// label, narrowed by namespace when q.Namespace is set. v2 supports only the
	// `app` label scheme; clusters using a different label (container/pod/
	// service_name) should query Loki via the log.* tools directly. %q escapes
	// values so they cannot break out of the selector string.
	selector := fmt.Sprintf(`{app=%q}`, q.Workload)
	if q.Namespace != "" {
		selector = fmt.Sprintf(`{app=%q, namespace=%q}`, q.Workload, q.Namespace)
	}
	logqlQuery := selector + ` |~ "(?i)(error|fatal|panic)"`

	params := url.Values{
		"query": {logqlQuery},
		"start": {strconv.FormatInt(start.Unix()*int64(time.Second), 10)},
		"end":   {strconv.FormatInt(end.Unix()*int64(time.Second), 10)},
		"limit": {"5000"},
	}

	body, err := client.RawGet(ctx, "/loki/api/v1/query_range", params)
	if err != nil {
		return nil, fmt.Errorf("log_source: loki query: %w", err)
	}

	tsLines, err := parseLokiTimestamped(body)
	if err != nil {
		return nil, fmt.Errorf("log_source: parse: %w", err)
	}

	return logErrorEvents(tsLines, q.Workload), nil
}

// esEvents runs an Elasticsearch URI search for error-level logs of q.Workload
// over [start, end] and emits a "log_error" symptom event. The time range and
// (when set) namespace are folded into the query string; @timestamp is the
// time field (see esTimestampField). The ES backend is chosen by deterministic
// default. Non-standard log schemas (different time/namespace fields) should
// use the log.es_search tool directly.
func (s *logSource) esEvents(ctx context.Context, q change.ChangeQuery, start, end time.Time) ([]change.ChangeEvent, error) {
	_, client, err := tools.PickDefaultEndpoint(s.logs.ES, "correlate", "elasticsearch endpoint")
	if err != nil {
		return nil, err
	}

	params := url.Values{
		"q":       {esErrorQuery(q.Workload, q.Namespace, start, end)},
		"size":    {"5000"},
		"sort":    {esTimestampField + ":asc"},
		"_source": {esTimestampField},
	}

	body, err := client.RawGet(ctx, "/_all/_search", params)
	if err != nil {
		return nil, fmt.Errorf("log_source: es query: %w", err)
	}

	times, err := parseESTimestamps(body, esTimestampField)
	if err != nil {
		return nil, fmt.Errorf("log_source: es parse: %w", err)
	}

	return esLogErrorEvents(times, q.Workload), nil
}

// esErrorQuery builds the Lucene query string (URI search) for error-level logs
// of a workload in [start, end], optionally scoped to a k8s namespace. Values
// are rendered with %q as phrases so a quote cannot break out of the query.
func esErrorQuery(workload, namespace string, start, end time.Time) string {
	parts := []string{
		fmt.Sprintf("%s:[%s TO %s]", esTimestampField, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)),
		"(level:ERROR OR level:FATAL OR level:error OR level:fatal)",
		fmt.Sprintf("%q", workload),
	}
	if namespace != "" {
		parts = append(parts, fmt.Sprintf("kubernetes.namespace_name:%q", namespace))
	}
	return strings.Join(parts, " AND ")
}

// parseESTimestamps decodes an Elasticsearch _search response and returns each
// hit's timestamp, read from the tsField inside _source. Hits whose timestamp
// is missing or unparseable (not RFC3339) are skipped.
func parseESTimestamps(body []byte, tsField string) ([]time.Time, error) {
	var env struct {
		Hits struct {
			Hits []struct {
				Source map[string]json.RawMessage `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	var out []time.Time
	for _, h := range env.Hits.Hits {
		raw, ok := h.Source[tsField]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// esLogErrorEvents emits a single "log_error" event anchored at the earliest
// hit timestamp, with the total error-log count in the Summary. The ES query
// already filters to error-level hits, so every timestamp counts.
func esLogErrorEvents(times []time.Time, workload string) []change.ChangeEvent {
	if len(times) == 0 {
		return nil
	}
	earliest := times[0]
	for _, t := range times[1:] {
		if t.Before(earliest) {
			earliest = t
		}
	}
	return []change.ChangeEvent{
		{
			Time:    earliest,
			Kind:    "log_error",
			Source:  "log",
			Target:  workload,
			Summary: fmt.Sprintf("%d error log(s) in window", len(times)),
		},
	}
}

// TimestampedLine is a single log line with its nanosecond-precision timestamp
// decoded from the Loki values array. It is exported at package level so the
// pure helper functions are directly testable without a live Loki client.
type TimestampedLine struct {
	Time time.Time
	Text string
}

// parseLokiTimestamped decodes the Loki query_range envelope and returns lines
// with real time.Time values (parsed from nanosecond Unix strings).
func parseLokiTimestamped(body []byte) ([]TimestampedLine, error) {
	var env struct {
		Data struct {
			Result []struct {
				Values [][2]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	var out []TimestampedLine
	for _, s := range env.Data.Result {
		for _, v := range s.Values {
			ns, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				continue
			}
			out = append(out, TimestampedLine{
				Time: time.Unix(0, ns),
				Text: v[1],
			})
		}
	}
	return out, nil
}

// isErrorLine reports whether a log line indicates an error condition.
// The check is case-insensitive; blank lines are never considered errors.
func isErrorLine(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	lower := strings.ToLower(line)
	return strings.Contains(lower, "error") ||
		strings.Contains(lower, "fatal") ||
		strings.Contains(lower, "panic") ||
		strings.Contains(lower, "level=error")
}

// logErrorEvents scans lines for error-level entries and, when at least one is
// found, emits a single ChangeEvent anchored at the earliest error line's
// timestamp. The Summary records the total error-line count in the window.
func logErrorEvents(lines []TimestampedLine, workload string) []change.ChangeEvent {
	var earliest time.Time
	count := 0
	for _, l := range lines {
		if !isErrorLine(l.Text) {
			continue
		}
		count++
		if earliest.IsZero() || l.Time.Before(earliest) {
			earliest = l.Time
		}
	}
	if count == 0 {
		return nil
	}
	return []change.ChangeEvent{
		{
			Time:    earliest,
			Kind:    "log_error",
			Source:  "log",
			Target:  workload,
			Summary: fmt.Sprintf("%d error line(s) in window", count),
		},
	}
}
