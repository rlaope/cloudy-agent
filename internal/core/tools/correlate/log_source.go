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

// logSource folds log error spikes onto the change timeline as symptoms:
// ChangeEvents whose Kind is "log_error" and Source is "log". It draws from
// Loki backends only — Docker container logs are already exposed by the
// separate log.container tool, so this source is a no-op when no Loki clients
// are configured.
type logSource struct {
	logs      tlog.Clients
	dockerHub *dockerclient.Hub
}

// newLogSource builds a logSource over the configured log backends. It returns
// nil — so callers can omit the source — when neither a Loki client nor a
// Docker hub is available to pull logs from.
func newLogSource(logs tlog.Clients, dockerHub *dockerclient.Hub) change.ChangeSource {
	if len(logs.Loki) == 0 && dockerHub == nil {
		return nil
	}
	return &logSource{logs: logs, dockerHub: dockerHub}
}

func (s *logSource) Name() string { return "log" }

// RecentChanges scans recent Loki logs for q.Workload and emits a "log_error"
// symptom event when error-level lines are found in the window. Docker
// container logs are handled by the dedicated log.container tool; this source
// focuses on Loki. If no Loki clients are configured, it returns (nil, nil).
func (s *logSource) RecentChanges(ctx context.Context, q change.ChangeQuery) ([]change.ChangeEvent, error) {
	// No Loki clients — nothing to do. Docker logs are out of scope here.
	if len(s.logs.Loki) == 0 {
		return nil, nil
	}

	client, err := tools.PickEndpoint(s.logs.Loki, q.Context, "correlate", "loki endpoint")
	if err != nil {
		return nil, err
	}

	since := q.Since
	if since == 0 {
		since = time.Hour
	}
	now := time.Now()
	end := now.Unix()
	start := now.Add(-since).Unix()

	// LogQL selector: {app="<workload>"} is the canonical k8s-via-Promtail
	// label. v2 supports only the `app` label scheme and is namespace-agnostic;
	// clusters using a different label (container/pod/service_name) or needing
	// namespace scoping should query Loki via the log.* tools directly. %q
	// escapes the workload so it cannot break out of the selector string.
	logqlQuery := fmt.Sprintf(`{app=%q} |~ "(?i)(error|fatal|panic)"`, q.Workload)

	params := url.Values{
		"query": {logqlQuery},
		"start": {strconv.FormatInt(start*int64(time.Second), 10)},
		"end":   {strconv.FormatInt(end*int64(time.Second), 10)},
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

	events := logErrorEvents(tsLines, q.Workload)
	return events, nil
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
