package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/render"
)

func pickGCP(m map[string]*gcpProject, name string) (*gcpProject, error) {
	return tools.PickEndpoint(m, name, "cloud", "gcp project")
}

// baseArgs returns the per-project flags injected on every gcloud call:
// project, JSON output, and (optionally) the named gcloud configuration.
// The leading flag (--project) keeps CloudExec's allowlist prefix exactly
// "logging read" even though the filter is a trailing positional.
func (p *gcpProject) baseArgs() []string {
	args := []string{"--project", p.projectID, "--format", "json"}
	if p.configuration != "" {
		args = append(args, "--configuration", p.configuration)
	}
	return args
}

var gcpAccountSchema = map[string]any{
	"type":        "string",
	"description": "Name of the GCP project configured under cloud_gcp. Optional if exactly one is configured.",
}

// newGCPLogTools builds the GCP Cloud Logging read-only tools. GCP metric and
// trace reads are intentionally absent: gcloud has no clean read-only
// time-series or trace read command (see docs/RFC-CLOUD-OBSERVABILITY.md).
func newGCPLogTools(projs map[string]*gcpProject) []tools.Tool {
	return []tools.Tool{
		newGCPLoggingReadTool(projs),
	}
}

// newGCPLoggingReadTool wraps `gcloud logging read` — the clean read-only path
// into Cloud Logging. The agent supplies a Logging query-language filter; the
// project, JSON output, and result cap are injected.
func newGCPLoggingReadTool(projs map[string]*gcpProject) tools.Tool {
	type args struct {
		Account   string `json:"account"`
		Filter    string `json:"filter"`
		Limit     int    `json:"limit"`
		Freshness string `json:"freshness"`
		Order     string `json:"order"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account":   gcpAccountSchema,
			"filter":    map[string]any{"type": "string", "description": "Cloud Logging query-language filter, e.g. \"resource.type=gce_instance AND severity>=ERROR\" (optional; empty returns recent entries)."},
			"limit":     map[string]any{"type": "integer", "description": "Max log entries to return (default 50, max 1000).", "default": 50, "minimum": 1, "maximum": 1000},
			"freshness": map[string]any{"type": "string", "description": "Relative time window, e.g. \"1h\", \"1d\" (optional; gcloud defaults to 1d when the filter has no timestamp)."},
			"order":     map[string]any{"type": "string", "description": "Sort order by timestamp: \"asc\" or \"desc\" (optional, default desc)."},
		},
	})
	return tools.Spec[args]{
		Name:        "cloud.gcp_logging_read",
		Description: "Read Cloud Logging entries from a GCP project (read-only `gcloud logging read`). Provide an optional Logging query-language filter and a time window.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			proj, err := pickGCP(projs, a.Account)
			if err != nil {
				return tools.Observation{}, err
			}
			if a.Limit <= 0 || a.Limit > 1000 {
				a.Limit = 50
			}
			cmd := append([]string{"logging", "read"}, proj.baseArgs()...)
			cmd = append(cmd, "--limit", strconv.Itoa(a.Limit))
			if a.Freshness != "" {
				if err := safeArg("freshness", a.Freshness); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, "--freshness", a.Freshness)
			}
			if a.Order != "" {
				if a.Order != "asc" && a.Order != "desc" {
					return tools.Observation{}, fmt.Errorf("cloud.gcp_logging_read: order must be \"asc\" or \"desc\"")
				}
				cmd = append(cmd, "--order", a.Order)
			}
			// The filter is gcloud's trailing positional; it must come LAST and
			// must not be parsed as a flag (safeArg rejects a leading '-'). A
			// Logging filter legitimately contains spaces, '=', '>' and parens —
			// none of which matter to argv-only exec.
			if a.Filter != "" {
				if err := safeArg("filter", a.Filter); err != nil {
					return tools.Observation{}, err
				}
				cmd = append(cmd, a.Filter)
			}
			body, err := CloudExec(ctx, "gcloud", cmd)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.gcp_logging_read: %w", err)
			}
			var entries []struct {
				Timestamp   string         `json:"timestamp"`
				Severity    string         `json:"severity"`
				TextPayload string         `json:"textPayload"`
				JSONPayload map[string]any `json:"jsonPayload"`
				Resource    struct {
					Type string `json:"type"`
				} `json:"resource"`
			}
			if err := json.Unmarshal(body, &entries); err != nil {
				return tools.Observation{}, fmt.Errorf("cloud.gcp_logging_read: decode: %w", err)
			}
			tbl := &render.Table{
				Headers: []string{"TIME", "SEVERITY", "RESOURCE", "MESSAGE"},
				Aligns:  []render.Align{render.AlignLeft, render.AlignLeft, render.AlignLeft, render.AlignLeft},
			}
			for _, e := range entries {
				tbl.Rows = append(tbl.Rows, []string{e.Timestamp, e.Severity, e.Resource.Type, logMessage(e.TextPayload, e.JSONPayload)})
			}
			return tools.Observation{
				Text:  fmt.Sprintf("%d log entr(ies) from project %q.", len(entries), proj.name),
				Table: tbl,
				Raw:   entries,
			}, nil
		},
	}.Build()
}

// logMessage extracts a human-readable message from a Cloud Logging entry,
// preferring the text payload and falling back to a "message" field in a
// structured (jsonPayload) entry.
func logMessage(text string, structured map[string]any) string {
	if text != "" {
		return text
	}
	if structured != nil {
		if m, ok := structured["message"].(string); ok {
			return m
		}
	}
	return ""
}
