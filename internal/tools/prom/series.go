package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
)

// SeriesTool implements prom.series.
type SeriesTool struct {
	clients    map[string]*Client
	defaultKey string
}

// NewSeriesTool constructs a SeriesTool.
func NewSeriesTool(clients map[string]*Client) *SeriesTool {
	return &SeriesTool{clients: clients, defaultKey: firstKey(clients)}
}

func (t *SeriesTool) Name() string   { return "prom.series" }
func (t *SeriesTool) ReadOnly() bool { return true }
func (t *SeriesTool) Description() string {
	return "Return metadata for Prometheus series matching the given selectors."
}
func (t *SeriesTool) Schema() json.RawMessage {
	return schema(map[string]any{
		"endpoint": strProp("Named Prometheus endpoint (empty = default)."),
		"matchers": strArrayProp("Series selectors, e.g. [\"up\", \"{job=\\\"prometheus\\\"}\"]."),
		"start":    strProp("Start time in RFC3339 (optional)."),
		"end":      strProp("End time in RFC3339 (optional)."),
	}, []string{"matchers"})
}

func (t *SeriesTool) Run(ctx context.Context, args json.RawMessage) (tools.Observation, error) {
	var a struct {
		Endpoint string   `json:"endpoint"`
		Matchers []string `json:"matchers"`
		Start    string   `json:"start"`
		End      string   `json:"end"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Observation{}, fmt.Errorf("prom.series: parse args: %w", err)
	}
	if len(a.Matchers) == 0 {
		return tools.Observation{}, fmt.Errorf("prom.series: at least one matcher is required")
	}

	key := a.Endpoint
	if key == "" {
		key = t.defaultKey
	}
	c, ok := t.clients[key]
	if !ok {
		return tools.Observation{}, fmt.Errorf("prom.series: unknown endpoint %q", a.Endpoint)
	}

	var start, end time.Time
	var err error
	if a.Start != "" {
		start, err = time.Parse(time.RFC3339, a.Start)
		if err != nil {
			return tools.Observation{}, fmt.Errorf("prom.series: parse start: %w", err)
		}
	}
	if a.End != "" {
		end, err = time.Parse(time.RFC3339, a.End)
		if err != nil {
			return tools.Observation{}, fmt.Errorf("prom.series: parse end: %w", err)
		}
	}

	seriesList, err := c.Series(ctx, a.Matchers, start, end)
	if err != nil {
		return tools.Observation{}, fmt.Errorf("prom.series: %w", err)
	}

	// Collect all unique label names for table headers.
	labelSet := map[string]struct{}{}
	for _, s := range seriesList {
		for k := range s {
			labelSet[k] = struct{}{}
		}
	}
	headers := make([]string, 0, len(labelSet))
	for k := range labelSet {
		headers = append(headers, k)
	}
	sort.Strings(headers)

	tbl := &render.Table{Headers: headers}
	for _, s := range seriesList {
		row := make([]string, len(headers))
		for i, h := range headers {
			row[i] = s[h]
		}
		tbl.Rows = append(tbl.Rows, row)
	}

	text := fmt.Sprintf("series=%d matchers=%s", len(seriesList), strings.Join(a.Matchers, ","))
	return tools.Observation{Text: text, Table: tbl, Raw: seriesList}, nil
}
