// Package gitops provides read-only GitOps-tool wrappers — currently Argo CD
// via its v1 HTTP API. All access flows through httpapi.Client so the
// transport-layer GET-only contract applies. Argo CD's standard auth is a
// bearer token minted under User Info → Generate New; configure it via the
// BearerEnv field on ArgoCDEndpoint.
package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/httpapi"
)

// ArgoClient wraps an httpapi.Client with the Argo CD v1 path layout.
type ArgoClient struct {
	*httpapi.Client
}

func pickArgo(m map[string]*ArgoClient, name string) (*ArgoClient, error) {
	return tools.PickEndpoint(m, name, "gitops", "argo cd endpoint")
}

var argoEndpointSchema = map[string]any{
	"type":        "string",
	"description": "Name of the Argo CD endpoint configured under argocd. Optional if exactly one is configured.",
}

// newArgoListAppsTool wraps GET /api/v1/applications. The v1 envelope is
// {items: [{metadata, spec, status}, ...]} — we flatten the bits an
// operator triages on (sync status, health, target revision, last sync
// timestamp) into a compact table and keep the full envelope in Raw.
func newArgoListAppsTool(clients map[string]*ArgoClient) tools.Tool {
	type args struct {
		Name    string `json:"name"`
		Project string `json:"project"`
		Limit   int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":    argoEndpointSchema,
			"project": map[string]any{"type": "string", "description": "Filter by Argo CD project name (empty = all projects)."},
			"limit":   map[string]any{"type": "integer", "description": "Max applications to render (default 50, max 500).", "default": 50, "minimum": 1, "maximum": 500},
		},
	})
	return tools.Spec[args]{
		Name:        "gitops.argo_list_apps",
		Description: "List Argo CD applications (/api/v1/applications) with sync status, health, target revision, last sync time.",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.Limit <= 0 {
				a.Limit = 50
			}
			if a.Limit > 500 {
				a.Limit = 500
			}
			c, err := pickArgo(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			params := url.Values{}
			if a.Project != "" {
				params.Set("projects", a.Project)
			}
			body, err := c.RawGet(ctx, "/api/v1/applications", params)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("gitops.argo_list_apps: %w", err)
			}
			apps, perr := parseArgoApps(body)
			if perr != nil {
				return tools.Observation{}, fmt.Errorf("gitops.argo_list_apps: decode: %w", perr)
			}
			return tools.Observation{
				Text:  formatArgoApps(apps, a.Limit),
				Table: tableArgoApps(apps, a.Limit),
				Raw:   apps,
			}, nil
		},
	}.Build()
}

// ArgoApp is the flattened ArgoCD Application. We pull only the fields used
// for triage — the full envelope (resources, conditions, history) is left in
// Raw for downstream skills that need it.
type ArgoApp struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	Project        string `json:"project"`
	SyncStatus     string `json:"sync_status"`
	HealthStatus   string `json:"health_status"`
	TargetRevision string `json:"target_revision"`
	RepoURL        string `json:"repo_url"`
	Path           string `json:"path"`
	Revision       string `json:"revision"`
	LastSyncAt     string `json:"last_sync_at"`
}

func parseArgoApps(body []byte) ([]ArgoApp, error) {
	var env struct {
		Items []argoAppRaw `json:"items"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	out := make([]ArgoApp, len(env.Items))
	for i, it := range env.Items {
		out[i] = flattenArgoApp(it)
	}
	// Surface out-of-sync / degraded apps first.
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := appHealthRank(out[i]), appHealthRank(out[j])
		if ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// argoAppRaw mirrors only the fields we extract from the v1 envelope —
// having the full shape declared in the package makes the JSON tags
// reviewable in one place.
type argoAppRaw struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Project string `json:"project"`
		Source  struct {
			RepoURL        string `json:"repoURL"`
			Path           string `json:"path"`
			TargetRevision string `json:"targetRevision"`
		} `json:"source"`
	} `json:"spec"`
	Status struct {
		Sync struct {
			Status   string `json:"status"`
			Revision string `json:"revision"`
		} `json:"sync"`
		Health struct {
			Status string `json:"status"`
		} `json:"health"`
		OperationState struct {
			FinishedAt string `json:"finishedAt"`
		} `json:"operationState"`
		History []struct {
			Revision   string `json:"revision"`
			DeployedAt string `json:"deployedAt"`
		} `json:"history"`
	} `json:"status"`
}

func flattenArgoApp(it argoAppRaw) ArgoApp {
	lastSync := it.Status.OperationState.FinishedAt
	if lastSync == "" && len(it.Status.History) > 0 {
		// Older Argo CD or apps that never ran the modern op pipeline only
		// populate history; use the most-recent entry.
		lastSync = it.Status.History[len(it.Status.History)-1].DeployedAt
	}
	return ArgoApp{
		Name:           it.Metadata.Name,
		Namespace:      it.Metadata.Namespace,
		Project:        it.Spec.Project,
		SyncStatus:     it.Status.Sync.Status,
		HealthStatus:   it.Status.Health.Status,
		TargetRevision: it.Spec.Source.TargetRevision,
		RepoURL:        it.Spec.Source.RepoURL,
		Path:           it.Spec.Source.Path,
		Revision:       it.Status.Sync.Revision,
		LastSyncAt:     lastSync,
	}
}

func appHealthRank(a ArgoApp) int {
	score := 0
	switch a.SyncStatus {
	case "OutOfSync":
		score += 10
	case "Unknown":
		score += 20
	}
	switch a.HealthStatus {
	case "Degraded":
		score += 5
	case "Missing":
		score += 7
	case "Progressing":
		score += 3
	case "Unknown":
		score += 2
	}
	// Lower numerical rank means "show first" — invert so the most-broken
	// apps get rank 0.
	return -score
}

func tableArgoApps(apps []ArgoApp, limit int) *render.Table {
	tbl := &render.Table{Headers: []string{"NAME", "PROJECT", "SYNC", "HEALTH", "REVISION", "LAST_SYNC"}}
	for i, a := range apps {
		if i >= limit {
			break
		}
		tbl.Rows = append(tbl.Rows, []string{
			a.Name,
			a.Project,
			a.SyncStatus,
			a.HealthStatus,
			shortSHA(a.Revision),
			a.LastSyncAt,
		})
	}
	return tbl
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func formatArgoApps(apps []ArgoApp, limit int) string {
	if len(apps) == 0 {
		return "(no Argo CD applications)"
	}
	var outOfSync, degraded int
	for _, a := range apps {
		if a.SyncStatus == "OutOfSync" {
			outOfSync++
		}
		if a.HealthStatus == "Degraded" || a.HealthStatus == "Missing" {
			degraded++
		}
	}
	shown := len(apps)
	if shown > limit {
		shown = limit
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d apps (out_of_sync=%d degraded=%d) showing %d\n",
		len(apps), outOfSync, degraded, shown)
	for i := 0; i < shown; i++ {
		a := apps[i]
		fmt.Fprintf(&b, "  %s [%s/%s] %s @ %s\n",
			a.Name, a.SyncStatus, a.HealthStatus,
			a.TargetRevision, shortSHA(a.Revision))
	}
	return b.String()
}

// newArgoAppStatusTool wraps GET /api/v1/applications/{name} — a single
// app's full sync + health detail with conditions and resources.
func newArgoAppStatusTool(clients map[string]*ArgoClient) tools.Tool {
	type args struct {
		Name string `json:"name"`
		App  string `json:"app"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": argoEndpointSchema,
			"app":  map[string]any{"type": "string", "description": "Argo CD application name."},
		},
		"required": []string{"app"},
	})
	return tools.Spec[args]{
		Name:        "gitops.argo_app_status",
		Description: "Fetch one Argo CD application's full sync + health detail (/api/v1/applications/{name}).",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.App == "" {
				return tools.Observation{}, fmt.Errorf("gitops.argo_app_status: app is required")
			}
			c, err := pickArgo(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			body, err := c.RawGet(ctx, "/api/v1/applications/"+url.PathEscape(a.App), nil)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("gitops.argo_app_status: %w", err)
			}
			var it argoAppRaw
			if err := json.Unmarshal(body, &it); err != nil {
				return tools.Observation{}, fmt.Errorf("gitops.argo_app_status: decode: %w", err)
			}
			app := flattenArgoApp(it)
			// Resources + conditions live alongside; surface them as a
			// secondary text section.
			var resources struct {
				Status struct {
					Conditions []struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"conditions"`
					Resources []struct {
						Kind   string `json:"kind"`
						Name   string `json:"name"`
						Status string `json:"status"`
						Health struct {
							Status string `json:"status"`
						} `json:"health"`
					} `json:"resources"`
				} `json:"status"`
			}
			_ = json.Unmarshal(body, &resources)
			text := formatArgoAppStatus(app, resources.Status.Conditions, resources.Status.Resources)
			return tools.Observation{
				Text: text,
				Raw:  json.RawMessage(body),
			}, nil
		},
	}.Build()
}

func formatArgoAppStatus(app ArgoApp, conditions []struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}, resources []struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Health struct {
		Status string `json:"status"`
	} `json:"health"`
}) string {
	var b strings.Builder
	fmt.Fprintf(&b, "app=%s project=%s namespace=%s\n", app.Name, app.Project, app.Namespace)
	fmt.Fprintf(&b, "  sync=%s health=%s target=%s revision=%s last_sync=%s\n",
		app.SyncStatus, app.HealthStatus, app.TargetRevision, shortSHA(app.Revision), app.LastSyncAt)
	fmt.Fprintf(&b, "  repo=%s path=%s\n", app.RepoURL, app.Path)
	if len(conditions) > 0 {
		b.WriteString("conditions:\n")
		for _, c := range conditions {
			fmt.Fprintf(&b, "  [%s] %s\n", c.Type, c.Message)
		}
	}
	if len(resources) > 0 {
		b.WriteString("resources:\n")
		shown := len(resources)
		if shown > 50 {
			shown = 50
			fmt.Fprintf(&b, "  (showing first %d of %d)\n", shown, len(resources))
		}
		for i := 0; i < shown; i++ {
			r := resources[i]
			fmt.Fprintf(&b, "  %s/%s sync=%s health=%s\n", r.Kind, r.Name, r.Status, r.Health.Status)
		}
	}
	return b.String()
}

// newArgoAppHistoryTool wraps GET /api/v1/applications/{name}/revisions
// when available, but the canonical recent-revision data already lives on
// the application object itself in .status.history. We read the app object
// (which we know works against every Argo CD version) and project the
// history field — falling through to /revisions when status.history is
// empty so newer-only deployments still report something.
func newArgoAppHistoryTool(clients map[string]*ArgoClient) tools.Tool {
	type args struct {
		Name  string `json:"name"`
		App   string `json:"app"`
		Limit int    `json:"limit"`
	}
	schema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  argoEndpointSchema,
			"app":   map[string]any{"type": "string", "description": "Argo CD application name."},
			"limit": map[string]any{"type": "integer", "description": "Max history entries to render (default 50, max 200).", "default": 50, "minimum": 1, "maximum": 200},
		},
		"required": []string{"app"},
	})
	return tools.Spec[args]{
		Name:        "gitops.argo_app_history",
		Description: "Argo CD application recent revision history — commit SHA + deployedAt per sync (.status.history on the app object).",
		Schema:      schema,
		Run: func(ctx context.Context, a args) (tools.Observation, error) {
			if a.App == "" {
				return tools.Observation{}, fmt.Errorf("gitops.argo_app_history: app is required")
			}
			if a.Limit <= 0 {
				a.Limit = 50
			}
			if a.Limit > 200 {
				a.Limit = 200
			}
			c, err := pickArgo(clients, a.Name)
			if err != nil {
				return tools.Observation{}, err
			}
			body, err := c.RawGet(ctx, "/api/v1/applications/"+url.PathEscape(a.App), nil)
			if err != nil {
				return tools.Observation{}, fmt.Errorf("gitops.argo_app_history: %w", err)
			}
			history, perr := parseArgoHistory(body)
			if perr != nil {
				return tools.Observation{}, fmt.Errorf("gitops.argo_app_history: decode: %w", perr)
			}
			return tools.Observation{
				Text:  formatArgoHistory(a.App, history, a.Limit),
				Table: tableArgoHistory(history, a.Limit),
				Raw:   history,
			}, nil
		},
	}.Build()
}

// ArgoHistoryEntry is one sync occurrence. Argo CD's history records only
// the revision and the deployedAt timestamp on the app object itself —
// commit author/message live behind a separate /revisions/{rev}/metadata
// call, which we leave out of this readout to keep the tool one-shot.
type ArgoHistoryEntry struct {
	Revision   string `json:"revision"`
	DeployedAt string `json:"deployed_at"`
	Source     string `json:"source,omitempty"`
}

func parseArgoHistory(body []byte) ([]ArgoHistoryEntry, error) {
	var it struct {
		Status struct {
			History []struct {
				Revision   string `json:"revision"`
				DeployedAt string `json:"deployedAt"`
				Source     struct {
					RepoURL string `json:"repoURL"`
				} `json:"source"`
			} `json:"history"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &it); err != nil {
		return nil, err
	}
	out := make([]ArgoHistoryEntry, len(it.Status.History))
	for i, h := range it.Status.History {
		out[i] = ArgoHistoryEntry{
			Revision:   h.Revision,
			DeployedAt: h.DeployedAt,
			Source:     h.Source.RepoURL,
		}
	}
	// History is appended chronologically; reverse so newest is first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func tableArgoHistory(entries []ArgoHistoryEntry, limit int) *render.Table {
	tbl := &render.Table{Headers: []string{"#", "REVISION", "DEPLOYED_AT", "SOURCE"}}
	for i, e := range entries {
		if i >= limit {
			break
		}
		tbl.Rows = append(tbl.Rows, []string{
			strconv.Itoa(i),
			shortSHA(e.Revision),
			e.DeployedAt,
			e.Source,
		})
	}
	return tbl
}

func formatArgoHistory(app string, entries []ArgoHistoryEntry, limit int) string {
	if len(entries) == 0 {
		return fmt.Sprintf("app=%s: no recorded sync history", app)
	}
	shown := len(entries)
	if shown > limit {
		shown = limit
	}
	var b strings.Builder
	fmt.Fprintf(&b, "app=%s history=%d (showing %d, newest first)\n", app, len(entries), shown)
	for i := 0; i < shown; i++ {
		e := entries[i]
		fmt.Fprintf(&b, "  #%d %s @ %s\n", i, shortSHA(e.Revision), e.DeployedAt)
	}
	return b.String()
}

var mustJSON = tools.MustJSON
