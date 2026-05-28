package correlate

import (
	"context"
	"fmt"
	"time"

	promclient "github.com/rlaope/cloudy/internal/clients/prom"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/change"
)

// metricSource folds a PromQL breach onto the change timeline as a symptom: a
// ChangeEvent whose Kind is "metric_breach" and Source is "metric". The query
// and threshold are per-call args (see correlateTool.Run), so this source is
// built at Run time, not registration.
type metricSource struct {
	clients   map[string]*promclient.Client
	query     string
	threshold float64
}

// newMetricSource builds a metricSource over the configured Prometheus
// clients. It returns nil — so callers can omit the source — when there is no
// query to run or no Prometheus backend is wired.
func newMetricSource(prom map[string]*promclient.Client, query string, threshold float64) change.ChangeSource {
	if query == "" || len(prom) == 0 {
		return nil
	}
	return &metricSource{clients: prom, query: query, threshold: threshold}
}

func (s *metricSource) Name() string { return "metric" }

// RecentChanges range-queries the PromQL expression over the window derived
// from q.Since and emits a single "metric_breach" ChangeEvent at the earliest
// timestamp where the value exceeds the threshold. An empty q.Context resolves
// to the single configured endpoint (or errors when ambiguous).
func (s *metricSource) RecentChanges(ctx context.Context, q change.ChangeQuery) ([]change.ChangeEvent, error) {
	client, err := tools.PickEndpoint(s.clients, q.Context, "correlate", "prometheus endpoint")
	if err != nil {
		return nil, err
	}

	end := time.Now()
	window := q.Since
	if window <= 0 {
		window = time.Hour
	}
	start := end.Add(-window)

	// Pick a step that yields at most ~200 points; floor at 15 s.
	step := window / 200
	if step < 15*time.Second {
		step = 15 * time.Second
	}

	res, err := client.QueryRange(ctx, s.query, start, end, step)
	if err != nil {
		return nil, err
	}

	return metricBreachEvents(res, s.threshold, s.query), nil
}

// metricBreachEvents scans a Prometheus matrix result and returns a slice
// containing at most one ChangeEvent — at the earliest timestamp across all
// series where value > threshold. Values[i][0] is a Unix timestamp in seconds
// (float64); it is converted to time.Time via time.Unix with nanosecond
// precision for sub-second steps.
func metricBreachEvents(res *promclient.Result, threshold float64, query string) []change.ChangeEvent {
	if res == nil || len(res.Matrix) == 0 {
		return nil
	}

	var (
		breachTime  time.Time
		breachValue float64
		peakValue   float64
		found       bool
	)

	for _, series := range res.Matrix {
		for _, pt := range series.Values {
			tsSec := pt[0]
			val := pt[1]
			t := time.Unix(int64(tsSec), int64((tsSec-float64(int64(tsSec)))*1e9))

			if val > peakValue {
				peakValue = val
			}
			if val > threshold {
				if !found || t.Before(breachTime) {
					breachTime = t
					breachValue = val
					found = true
				}
			}
		}
	}

	if !found {
		return nil
	}

	// Re-scan to find the true peak across all series now that we know there is
	// a breach (peakValue already set above across the full scan).
	return []change.ChangeEvent{
		{
			Time:    breachTime,
			Kind:    "metric_breach",
			Target:  query,
			Summary: fmt.Sprintf("metric exceeded threshold %.4g (breach value %.4g, peak %.4g)", threshold, breachValue, peakValue),
			After:   fmt.Sprintf("%.4g", breachValue),
			Source:  "metric",
		},
	}
}
