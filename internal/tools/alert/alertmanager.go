// Package alert provides read-only Alertmanager + Prometheus alert-rule
// tools. The Alertmanager v2 OpenAPI surface is reached via httpapi.Client
// so the transport-layer GET-only contract applies; Prometheus alert rules
// reuse the same prom.Client map already configured for metric queries.
package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/httpapi"
)

// AMClient wraps an httpapi.Client with the Alertmanager v2 path layout.
// The base URL configured by the operator points at Alertmanager's root;
// the /api/v2 prefix is appended per request.
type AMClient struct {
	*httpapi.Client
}

func pickAM(m map[string]*AMClient, name string) (*AMClient, error) {
	return tools.PickEndpoint(m, name, "alert", "alertmanager endpoint")
}

var amEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the alertmanager endpoint configured under alertmanager. Optional if exactly one is configured.",
}

// newAMListActiveTool wraps GET /api/v2/alerts. The Alertmanager v2 OpenAPI
// returns a flat array of GettableAlert objects — each carries labels,
// annotations, startsAt/endsAt, and a nested status block with state
// ("active" | "suppressed" | "unprocessed") and inhibitedBy/silencedBy refs.
// We surface the labels + state in a tight table and stash the raw envelope
// for downstream skills.
func newAMListActiveTool(clients map[string]*AMClient) tools.Tool {
	type args struct {
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  amEndpointSchema,
			"limit": map[string]any{"type": "integer", "description": "Max alerts to render (default 50, max 500).", "default": 50, "minimum": 1, "maximum": 500},
		},
	})
	return tools.Spec[args]{
		Name:        "alert.list_active",
		Description: "List currently active alerts from Alertmanager v2 (/api/v2/alerts). Grouped by state (firing/suppressed) with labels and severity.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Limit <= 0 {
				a.Limit = 50
			}
			if a.Limit > 500 {
				a.Limit = 500
			}
			c, err := pickAM(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			body, err := c.RawGet(ctx, "/api/v2/alerts", nil)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("alert.list_active: %w", err)
			}
			alerts, perr := parseAMAlerts(body)
			if perr != nil {
				return tools.Observation{}, fmt.Errorf("alert.list_active: decode: %w", perr)
			}
			return tools.Observation{
				Text:  formatAMAlerts(alerts, a.Limit),
				Table: tableAMAlerts(alerts, a.Limit),
				Raw:   alerts,
			}, nil
		},
	}.Build()
}

// AMAlert is a flattened Alertmanager v2 GettableAlert. The v2 schema is
// deeper than this — fingerprint, generatorURL, receivers — but the agent
// only needs the labels + state + annotations to triage. The full envelope
// is preserved via Observation.Raw.
type AMAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    string            `json:"startsAt"`
	EndsAt      string            `json:"endsAt"`
	State       string            `json:"state"`
	SilencedBy  []string          `json:"silencedBy,omitempty"`
	InhibitedBy []string          `json:"inhibitedBy,omitempty"`
}

func parseAMAlerts(body []byte) ([]AMAlert, error) {
	// v2 GettableAlert puts state under "status": { "state": "...", ... }.
	var raw []struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		StartsAt    string            `json:"startsAt"`
		EndsAt      string            `json:"endsAt"`
		Status      struct {
			State       string   `json:"state"`
			SilencedBy  []string `json:"silencedBy"`
			InhibitedBy []string `json:"inhibitedBy"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]AMAlert, len(raw))
	for i, r := range raw {
		out[i] = AMAlert{
			Labels:      r.Labels,
			Annotations: r.Annotations,
			StartsAt:    r.StartsAt,
			EndsAt:      r.EndsAt,
			State:       r.Status.State,
			SilencedBy:  r.Status.SilencedBy,
			InhibitedBy: r.Status.InhibitedBy,
		}
	}
	// Stable sort: firing first, then by alertname for deterministic output.
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := stateRank(out[i].State), stateRank(out[j].State)
		if si != sj {
			return si < sj
		}
		return out[i].Labels["alertname"] < out[j].Labels["alertname"]
	})
	return out, nil
}

// stateRank pushes firing alerts to the top so the LLM sees the most
// actionable rows first when the observation is truncated.
func stateRank(s string) int {
	switch s {
	case "active":
		return 0
	case "suppressed":
		return 1
	default:
		return 2
	}
}

func tableAMAlerts(alerts []AMAlert, limit int) *render.Table {
	tbl := &render.Table{Headers: []string{"STATE", "ALERTNAME", "SEVERITY", "INSTANCE", "STARTS_AT"}}
	for i, a := range alerts {
		if i >= limit {
			break
		}
		tbl.Rows = append(tbl.Rows, []string{
			a.State,
			a.Labels["alertname"],
			a.Labels["severity"],
			pickInstanceLabel(a.Labels),
			a.StartsAt,
		})
	}
	return tbl
}

// pickInstanceLabel finds the most-useful "what is this about" label.
// Alertmanager doesn't have a canonical single field — different alert
// rules tag by service / pod / job / instance — so we walk a priority
// list and fall back to the first non-meta label.
func pickInstanceLabel(m map[string]string) string {
	for _, k := range []string{"instance", "pod", "service", "job", "namespace"} {
		if v := m[k]; v != "" {
			return v
		}
	}
	// Fall back to whatever non-meta label exists (sorted for stability).
	keys := make([]string, 0, len(m))
	for k := range m {
		if k == "alertname" || k == "severity" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return keys[0] + "=" + m[keys[0]]
	}
	return ""
}

func formatAMAlerts(alerts []AMAlert, limit int) string {
	if len(alerts) == 0 {
		return "(no active alerts)"
	}
	var firing, suppressed int
	for _, a := range alerts {
		switch a.State {
		case "active":
			firing++
		case "suppressed":
			suppressed++
		}
	}
	summary := fmt.Sprintf("%d alerts (firing=%d suppressed=%d)", len(alerts), firing, suppressed)
	shown := len(alerts)
	if shown > limit {
		shown = limit
		summary += fmt.Sprintf(", showing top %d", shown)
	}
	var b strings.Builder
	b.WriteString(summary)
	b.WriteByte('\n')
	for i := 0; i < shown; i++ {
		a := alerts[i]
		summary := a.Annotations["summary"]
		if summary == "" {
			summary = a.Annotations["description"]
		}
		fmt.Fprintf(&b, "  [%s] %s sev=%s on %s%s\n",
			a.State,
			a.Labels["alertname"],
			a.Labels["severity"],
			pickInstanceLabel(a.Labels),
			ifNonEmpty(" — ", summary),
		)
	}
	return b.String()
}

func ifNonEmpty(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}

// newAMListSilencesTool wraps GET /api/v2/silences. v2 returns
// GettableSilence objects with matchers, status.state, createdBy, comment,
// startsAt/endsAt. Expired silences are included by Alertmanager unless the
// caller filters with ?filter=…; we leave that filter to the caller and
// just rank active ones first.
func newAMListSilencesTool(clients map[string]*AMClient) tools.Tool {
	type args struct {
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  amEndpointSchema,
			"limit": map[string]any{"type": "integer", "description": "Max silences to render (default 50, max 500).", "default": 50, "minimum": 1, "maximum": 500},
		},
	})
	return tools.Spec[args]{
		Name:        "alert.list_silences",
		Description: "List Alertmanager silences (/api/v2/silences) with matcher, creator, comment, and expiry.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Limit <= 0 {
				a.Limit = 50
			}
			if a.Limit > 500 {
				a.Limit = 500
			}
			c, err := pickAM(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			body, err := c.RawGet(ctx, "/api/v2/silences", nil)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("alert.list_silences: %w", err)
			}
			silences, perr := parseAMSilences(body)
			if perr != nil {
				return tools.Observation{}, fmt.Errorf("alert.list_silences: decode: %w", perr)
			}
			return tools.Observation{
				Text:  formatAMSilences(silences, a.Limit),
				Table: tableAMSilences(silences, a.Limit),
				Raw:   silences,
			}, nil
		},
	}.Build()
}

// AMSilence is the flattened v2 GettableSilence. Matchers in v2 are objects
// with name/value/isRegex/isEqual; we render them as "k=v" / "k=~v" pairs.
type AMSilence struct {
	ID        string      `json:"id"`
	Matchers  []AMMatcher `json:"matchers"`
	StartsAt  string      `json:"startsAt"`
	EndsAt    string      `json:"endsAt"`
	CreatedBy string      `json:"createdBy"`
	Comment   string      `json:"comment"`
	State     string      `json:"state"`
}

type AMMatcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

func parseAMSilences(body []byte) ([]AMSilence, error) {
	var raw []struct {
		ID        string      `json:"id"`
		Matchers  []AMMatcher `json:"matchers"`
		StartsAt  string      `json:"startsAt"`
		EndsAt    string      `json:"endsAt"`
		CreatedBy string      `json:"createdBy"`
		Comment   string      `json:"comment"`
		Status    struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]AMSilence, len(raw))
	for i, r := range raw {
		out[i] = AMSilence{
			ID:        r.ID,
			Matchers:  r.Matchers,
			StartsAt:  r.StartsAt,
			EndsAt:    r.EndsAt,
			CreatedBy: r.CreatedBy,
			Comment:   r.Comment,
			State:     r.Status.State,
		}
	}
	// Active silences first, then pending, then expired.
	sort.SliceStable(out, func(i, j int) bool {
		return silenceStateRank(out[i].State) < silenceStateRank(out[j].State)
	})
	return out, nil
}

func silenceStateRank(s string) int {
	switch s {
	case "active":
		return 0
	case "pending":
		return 1
	case "expired":
		return 2
	default:
		return 3
	}
}

func tableAMSilences(silences []AMSilence, limit int) *render.Table {
	tbl := &render.Table{Headers: []string{"STATE", "MATCHERS", "CREATOR", "COMMENT", "ENDS_AT"}}
	for i, s := range silences {
		if i >= limit {
			break
		}
		tbl.Rows = append(tbl.Rows, []string{
			s.State,
			renderMatchers(s.Matchers),
			s.CreatedBy,
			s.Comment,
			s.EndsAt,
		})
	}
	return tbl
}

func renderMatchers(ms []AMMatcher) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		op := "="
		switch {
		case m.IsRegex && m.IsEqual:
			op = "=~"
		case m.IsRegex && !m.IsEqual:
			op = "!~"
		case !m.IsRegex && !m.IsEqual:
			op = "!="
		}
		parts[i] = m.Name + op + strconv.Quote(m.Value)
	}
	return strings.Join(parts, ",")
}

func formatAMSilences(silences []AMSilence, limit int) string {
	if len(silences) == 0 {
		return "(no silences)"
	}
	shown := len(silences)
	if shown > limit {
		shown = limit
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d silences (showing %d)\n", len(silences), shown)
	for i := 0; i < shown; i++ {
		s := silences[i]
		fmt.Fprintf(&b, "  [%s] %s by %s until %s — %s\n",
			s.State,
			renderMatchers(s.Matchers),
			s.CreatedBy,
			s.EndsAt,
			s.Comment,
		)
	}
	return b.String()
}

var mustJSON = tools.MustJSON
