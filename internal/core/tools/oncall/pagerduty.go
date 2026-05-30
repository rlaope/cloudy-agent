// Package oncall provides read-only access to incident-response / paging
// backends — currently PagerDuty via its REST API v2. All access flows through
// httpapi.Client so the transport-layer GET-only contract applies. PagerDuty
// authenticates with a classic API token sent as "Authorization: Token
// token=<key>"; configure it via the TokenEnv field on PagerDutyEndpoint.
//
// The client surface is shaped as a per-backend map so a future Opsgenie /
// VictorOps backend can join without reshaping the registration pipeline.
package oncall

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

// PagerDutyClient wraps an httpapi.Client with the PagerDuty REST v2 layout.
type PagerDutyClient struct {
	*httpapi.Client
}

func pickPD(m map[string]*PagerDutyClient, name string) (*PagerDutyClient, error) {
	return tools.PickEndpoint(m, name, "oncall", "pagerduty account")
}

var pdEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the PagerDuty account configured under pagerduty. Optional if exactly one is configured.",
}

var mustJSON = tools.MustJSON

// newListIncidentsTool wraps GET /incidents. By default it returns the open
// incidents (triggered + acknowledged), newest first, which is what "what is
// on fire right now" asks for. Resolved incidents are included only when
// statuses="all" is passed.
func newListIncidentsTool(clients map[string]*PagerDutyClient) tools.Tool {
	type args struct {
		Name     string `json:"name"`
		Statuses string `json:"statuses"`
		Limit    int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":     pdEndpointSchema,
			"statuses": map[string]any{"type": "string", "description": "Which incidents to return: \"open\" (triggered+acknowledged, default), \"triggered\", \"acknowledged\", or \"all\" (includes resolved)."},
			"limit":    map[string]any{"type": "integer", "description": "Max incidents to render (default 25, max 100).", "default": 25, "minimum": 1, "maximum": 100},
		},
	})
	return tools.Spec[args]{
		Name:        "oncall.list_incidents",
		Description: "List PagerDuty incidents (open by default — triggered + acknowledged), newest first, with title, status, urgency, service, and assignee. The fastest way to answer \"what is paging right now\". Read-only.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Limit <= 0 {
				a.Limit = 25
			}
			if a.Limit > 100 {
				a.Limit = 100
			}
			c, err := pickPD(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			params := url.Values{}
			params.Set("limit", strconv.Itoa(a.Limit))
			params.Set("sort_by", "created_at:desc")
			for _, st := range incidentStatuses(a.Statuses) {
				params.Add("statuses[]", st)
			}
			body, err := c.RawGet(ctx, "/incidents", params)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("oncall.list_incidents: %w", err)
			}
			incs, perr := parseIncidents(body)
			if perr != nil {
				return tools.Observation{}, fmt.Errorf("oncall.list_incidents: decode: %w", perr)
			}
			return tools.Observation{
				Text:  formatIncidents(incs),
				Table: tableIncidents(incs),
				Raw:   incs,
			}, nil
		},
	}.Build()
}

// incidentStatuses maps the friendly statuses arg onto PagerDuty's status
// values. "all" returns nil so no status filter is sent (PagerDuty then
// includes resolved).
func incidentStatuses(s string) []string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "open":
		return []string{"triggered", "acknowledged"}
	case "triggered":
		return []string{"triggered"}
	case "acknowledged", "acked":
		return []string{"acknowledged"}
	case "all":
		return nil
	default:
		return []string{"triggered", "acknowledged"}
	}
}

// Incident is the flattened PagerDuty incident — only the triage fields.
type Incident struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Urgency   string `json:"urgency"`
	Service   string `json:"service"`
	Assignee  string `json:"assignee"`
	CreatedAt string `json:"created_at"`
	HTMLURL   string `json:"html_url"`
}

func parseIncidents(body []byte) ([]Incident, error) {
	var env struct {
		Incidents []struct {
			IncidentNumber int    `json:"incident_number"`
			Title          string `json:"title"`
			Status         string `json:"status"`
			Urgency        string `json:"urgency"`
			CreatedAt      string `json:"created_at"`
			HTMLURL        string `json:"html_url"`
			Service        struct {
				Summary string `json:"summary"`
			} `json:"service"`
			Assignments []struct {
				Assignee struct {
					Summary string `json:"summary"`
				} `json:"assignee"`
			} `json:"assignments"`
		} `json:"incidents"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	out := make([]Incident, len(env.Incidents))
	for i, it := range env.Incidents {
		assignee := ""
		if len(it.Assignments) > 0 {
			assignee = it.Assignments[0].Assignee.Summary
		}
		out[i] = Incident{
			Number:    it.IncidentNumber,
			Title:     it.Title,
			Status:    it.Status,
			Urgency:   it.Urgency,
			Service:   it.Service.Summary,
			Assignee:  assignee,
			CreatedAt: it.CreatedAt,
			HTMLURL:   it.HTMLURL,
		}
	}
	// Triggered before acknowledged, high urgency first, then newest.
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := incidentRank(out[i]), incidentRank(out[j])
		if ri != rj {
			return ri > rj
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

// incidentRank orders the most urgent, least-handled incidents first.
func incidentRank(i Incident) int {
	score := 0
	if i.Status == "triggered" {
		score += 2
	}
	if strings.EqualFold(i.Urgency, "high") {
		score += 1
	}
	return score
}

func tableIncidents(incs []Incident) *render.Table {
	tbl := &render.Table{Headers: []string{"#", "STATUS", "URGENCY", "SERVICE", "ASSIGNEE", "TITLE"}}
	for _, i := range incs {
		tbl.Rows = append(tbl.Rows, []string{
			strconv.Itoa(i.Number),
			i.Status,
			i.Urgency,
			i.Service,
			i.Assignee,
			i.Title,
		})
	}
	return tbl
}

func formatIncidents(incs []Incident) string {
	if len(incs) == 0 {
		return "(no matching PagerDuty incidents)"
	}
	var triggered, acked int
	for _, i := range incs {
		switch i.Status {
		case "triggered":
			triggered++
		case "acknowledged":
			acked++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d incident(s) (triggered=%d acknowledged=%d)\n", len(incs), triggered, acked)
	for _, i := range incs {
		fmt.Fprintf(&b, "  #%d [%s/%s] %s — %s (%s)\n",
			i.Number, i.Status, i.Urgency, i.Service, i.Title, i.Assignee)
	}
	return b.String()
}

// newWhoIsOnCallTool wraps GET /oncalls — the users currently on call, with
// their escalation policy, schedule, and escalation level. Answers "who do I
// page for service X" without leaving the terminal.
func newWhoIsOnCallTool(clients map[string]*PagerDutyClient) tools.Tool {
	type args struct {
		Name     string `json:"name"`
		Policy   string `json:"escalation_policy"`
		MaxLevel int    `json:"max_level"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":              pdEndpointSchema,
			"escalation_policy": map[string]any{"type": "string", "description": "Filter to one escalation policy by its PagerDuty ID (empty = all policies)."},
			"max_level":         map[string]any{"type": "integer", "description": "Only return on-calls at or above this escalation level (1 = first responder). Default 0 = all levels.", "minimum": 0},
		},
	})
	return tools.Spec[args]{
		Name:        "oncall.who_is_oncall",
		Description: "List who is currently on call in PagerDuty, with their escalation policy, schedule, and escalation level. Answers \"who do I page\" for an incident. Read-only.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			c, err := pickPD(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			params := url.Values{}
			params.Set("limit", "100")
			if a.Policy != "" {
				params.Add("escalation_policy_ids[]", a.Policy)
			}
			body, err := c.RawGet(ctx, "/oncalls", params)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("oncall.who_is_oncall: %w", err)
			}
			ocs, perr := parseOnCalls(body, a.MaxLevel)
			if perr != nil {
				return tools.Observation{}, fmt.Errorf("oncall.who_is_oncall: decode: %w", perr)
			}
			return tools.Observation{
				Text:  formatOnCalls(ocs),
				Table: tableOnCalls(ocs),
				Raw:   ocs,
			}, nil
		},
	}.Build()
}

// OnCall is the flattened PagerDuty on-call record.
type OnCall struct {
	User             string `json:"user"`
	EscalationPolicy string `json:"escalation_policy"`
	Schedule         string `json:"schedule"`
	Level            int    `json:"level"`
	Start            string `json:"start"`
	End              string `json:"end"`
}

func parseOnCalls(body []byte, maxLevel int) ([]OnCall, error) {
	var env struct {
		OnCalls []struct {
			EscalationLevel int `json:"escalation_level"`
			User            struct {
				Summary string `json:"summary"`
			} `json:"user"`
			EscalationPolicy struct {
				Summary string `json:"summary"`
			} `json:"escalation_policy"`
			Schedule struct {
				Summary string `json:"summary"`
			} `json:"schedule"`
			Start string `json:"start"`
			End   string `json:"end"`
		} `json:"oncalls"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	var out []OnCall
	for _, it := range env.OnCalls {
		if maxLevel > 0 && it.EscalationLevel < maxLevel {
			continue
		}
		out = append(out, OnCall{
			User:             it.User.Summary,
			EscalationPolicy: it.EscalationPolicy.Summary,
			Schedule:         it.Schedule.Summary,
			Level:            it.EscalationLevel,
			Start:            it.Start,
			End:              it.End,
		})
	}
	// Group by policy, then by escalation level ascending (first responder up).
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].EscalationPolicy != out[j].EscalationPolicy {
			return out[i].EscalationPolicy < out[j].EscalationPolicy
		}
		return out[i].Level < out[j].Level
	})
	return out, nil
}

func tableOnCalls(ocs []OnCall) *render.Table {
	tbl := &render.Table{Headers: []string{"USER", "ESCALATION_POLICY", "LEVEL", "SCHEDULE"}}
	for _, o := range ocs {
		tbl.Rows = append(tbl.Rows, []string{
			o.User,
			o.EscalationPolicy,
			strconv.Itoa(o.Level),
			o.Schedule,
		})
	}
	return tbl
}

func formatOnCalls(ocs []OnCall) string {
	if len(ocs) == 0 {
		return "(no one currently on call for the requested scope)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d on-call assignment(s)\n", len(ocs))
	for _, o := range ocs {
		sched := o.Schedule
		if sched == "" {
			sched = "(direct)"
		}
		fmt.Fprintf(&b, "  L%d %s — %s via %s\n", o.Level, o.User, o.EscalationPolicy, sched)
	}
	return b.String()
}

// pagerDutyBaseURL is PagerDuty's REST API root, used when an endpoint omits URL.
const pagerDutyBaseURL = "https://api.pagerduty.com"

// pagerDutyTokenScheme is PagerDuty's classic Authorization scheme prefix.
const pagerDutyTokenScheme = "Token token="
