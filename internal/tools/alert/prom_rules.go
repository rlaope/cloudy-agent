package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/httpapi"
)

// PromRulesClient is a thin wrapper that re-uses httpapi.Client to read
// Prometheus' /api/v1/rules. We don't reach into the existing prom.Client
// (its decoder owns the metric-query envelope and doesn't model the rule
// payload) — instead we hold a parallel httpapi.Client built from the same
// PrometheusEndpoint config in BuildClients.
type PromRulesClient struct {
	*httpapi.Client
}

func pickPromRules(m map[string]*PromRulesClient, name string) (*PromRulesClient, error) {
	return tools.PickEndpoint(m, name, "alert", "prometheus endpoint")
}

var promRulesEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the prometheus endpoint configured under prometheus. Optional if exactly one is configured.",
}

// newPromListRulesTool wraps GET /api/v1/rules. The Prometheus rules API
// returns a {data: {groups: [{name, file, rules: [...]}, ...]}} shape;
// each rule carries type ("alerting" | "recording"), name, query, and —
// for alerting rules — the current evaluation state ("inactive" |
// "pending" | "firing") plus the active-alert list.
func newPromListRulesTool(clients map[string]*PromRulesClient) tools.Tool {
	type args struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Limit int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  promRulesEndpointSchema,
			"type":  map[string]any{"type": "string", "description": "Filter by rule type: 'alert' or 'record'. Default: 'alert'."},
			"limit": map[string]any{"type": "integer", "description": "Max rules to render (default 50, max 500).", "default": 50, "minimum": 1, "maximum": 500},
		},
	})
	return tools.Spec[args]{
		Name:        "alert.list_rules",
		Description: "List Prometheus alert rules (/api/v1/rules) with their PromQL, severity label, and current evaluation state.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Limit <= 0 {
				a.Limit = 50
			}
			if a.Limit > 500 {
				a.Limit = 500
			}
			ruleType := strings.ToLower(a.Type)
			if ruleType == "" {
				ruleType = "alert"
			}
			c, err := pickPromRules(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			params := url.Values{}
			// The API accepts ?type=alert or ?type=record; pass through but
			// also re-filter on our side so older Prometheus versions that
			// ignore the param still produce a clean response.
			if ruleType == "alert" || ruleType == "record" {
				params.Set("type", ruleType)
			}
			body, err := c.RawGet(ctx, "/api/v1/rules", params)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("alert.list_rules: %w", err)
			}
			rules, perr := parsePromRules(body, ruleType)
			if perr != nil {
				return tools.Observation{}, fmt.Errorf("alert.list_rules: decode: %w", perr)
			}
			return tools.Observation{
				Text:  formatPromRules(rules, a.Limit),
				Table: tablePromRules(rules, a.Limit),
				Raw:   rules,
			}, nil
		},
	}.Build()
}

// PromRule is a flattened Prometheus alert/recording rule. Group/File come
// from the wrapping rule-group; Type is "alerting" or "recording" in the
// upstream API, normalised to "alert" / "record" for tool output.
type PromRule struct {
	Group     string            `json:"group"`
	File      string            `json:"file"`
	Type      string            `json:"type"`
	Name      string            `json:"name"`
	Query     string            `json:"query"`
	Duration  float64           `json:"duration"`
	Labels    map[string]string `json:"labels"`
	State     string            `json:"state,omitempty"`
	ActiveAt  string            `json:"activeAt,omitempty"`
	NumActive int               `json:"num_active,omitempty"`
}

func parsePromRules(body []byte, filter string) ([]PromRule, error) {
	var env struct {
		Status string `json:"status"`
		Data   struct {
			Groups []struct {
				Name  string `json:"name"`
				File  string `json:"file"`
				Rules []struct {
					Type     string            `json:"type"`
					Name     string            `json:"name"`
					Query    string            `json:"query"`
					Duration float64           `json:"duration"`
					Labels   map[string]string `json:"labels"`
					State    string            `json:"state"`
					Alerts   []struct {
						State    string `json:"state"`
						ActiveAt string `json:"activeAt"`
					} `json:"alerts"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	if env.Status != "" && env.Status != "success" {
		return nil, fmt.Errorf("API status=%s", env.Status)
	}
	var out []PromRule
	for _, g := range env.Data.Groups {
		for _, r := range g.Rules {
			// Normalise type label.
			typ := r.Type
			switch typ {
			case "alerting":
				typ = "alert"
			case "recording":
				typ = "record"
			}
			if filter == "alert" && typ != "alert" {
				continue
			}
			if filter == "record" && typ != "record" {
				continue
			}
			pr := PromRule{
				Group:     g.Name,
				File:      g.File,
				Type:      typ,
				Name:      r.Name,
				Query:     r.Query,
				Duration:  r.Duration,
				Labels:    r.Labels,
				State:     r.State,
				NumActive: len(r.Alerts),
			}
			if len(r.Alerts) > 0 {
				pr.ActiveAt = r.Alerts[0].ActiveAt
			}
			out = append(out, pr)
		}
	}
	// Sort: firing > pending > inactive > recording, then by name.
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := ruleStateRank(out[i].State), ruleStateRank(out[j].State)
		if ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func ruleStateRank(s string) int {
	switch s {
	case "firing":
		return 0
	case "pending":
		return 1
	case "inactive":
		return 2
	default:
		return 3
	}
}

func tablePromRules(rules []PromRule, limit int) *render.Table {
	tbl := &render.Table{Headers: []string{"TYPE", "NAME", "STATE", "SEVERITY", "GROUP", "ACTIVE"}}
	for i, r := range rules {
		if i >= limit {
			break
		}
		tbl.Rows = append(tbl.Rows, []string{
			r.Type,
			r.Name,
			r.State,
			r.Labels["severity"],
			r.Group,
			fmt.Sprintf("%d", r.NumActive),
		})
	}
	return tbl
}

func formatPromRules(rules []PromRule, limit int) string {
	if len(rules) == 0 {
		return "(no rules)"
	}
	shown := len(rules)
	if shown > limit {
		shown = limit
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d rules (showing %d)\n", len(rules), shown)
	for i := 0; i < shown; i++ {
		r := rules[i]
		query := r.Query
		// Trim multi-line PromQL to keep the inline summary readable; the
		// Table cell carries the full version is left to Raw for downstream.
		if idx := strings.IndexByte(query, '\n'); idx >= 0 {
			query = query[:idx] + " …"
		}
		if len(query) > 120 {
			query = query[:117] + "..."
		}
		state := r.State
		if state == "" {
			state = "-"
		}
		fmt.Fprintf(&b, "  [%s/%s] %s sev=%s group=%s active=%d :: %s\n",
			r.Type, state, r.Name, r.Labels["severity"], r.Group, r.NumActive, query)
	}
	return b.String()
}
