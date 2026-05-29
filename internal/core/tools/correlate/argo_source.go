// Package correlate joins the read-only change signals cloudy can already
// observe (Kubernetes + Docker rollouts) with one external GitOps source —
// Argo CD sync history — into a single newest-first evidence chain, and names
// the most recent state-altering event as the candidate cause behind a
// symptom. Nothing here mutates cluster, host, or Argo state: every source is
// built from list/inspect/get reads, in line with cloudy's read-only contract.
package correlate

import (
	"context"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/core/tools/change"
	"github.com/rlaope/cloudy/internal/core/tools/gitops"
)

// argoHistory is the slice of *gitops.ArgoClient that argoSource depends on.
// Declaring it locally keeps the change package decoupled from gitops and
// makes the source mockable in tests.
type argoHistory interface {
	AppHistory(ctx context.Context, app string) ([]gitops.ArgoHistoryEntry, error)
}

// argoSource adapts an Argo CD endpoint to change.ChangeSource. The query's
// Workload is treated as an Argo Application name. correlate has no per-source
// endpoint argument (q.Context is the Kubernetes context, not an Argo endpoint
// name), so the endpoint is resolved by default: the single configured client,
// or a deterministic first-by-name default when several are wired.
type argoSource struct {
	clients map[string]argoHistory
}

// newArgoSource builds an argoSource over the configured Argo clients. It
// returns nil when no client is wired so callers can omit the source.
func newArgoSource(clients map[string]*gitops.ArgoClient) *argoSource {
	if len(clients) == 0 {
		return nil
	}
	m := make(map[string]argoHistory, len(clients))
	for name, c := range clients {
		m[name] = c
	}
	return &argoSource{clients: m}
}

func (s *argoSource) Name() string { return "argo" }

// RecentChanges fetches q.Workload's sync history from the default Argo
// endpoint and converts each entry into a "sync" ChangeEvent, applying the
// q.Since window. The endpoint is resolved by default (single configured
// client, or deterministic first-by-name when several) — q.Context is the
// Kubernetes context and never selects the Argo endpoint here.
func (s *argoSource) RecentChanges(ctx context.Context, q change.ChangeQuery) ([]change.ChangeEvent, error) {
	_, client, err := tools.PickDefaultEndpoint(s.clients, "correlate", "argo cd endpoint")
	if err != nil {
		return nil, err
	}
	entries, err := client.AppHistory(ctx, q.Workload)
	if err != nil {
		return nil, err
	}
	cutoff := time.Time{}
	if q.Since > 0 {
		cutoff = time.Now().Add(-q.Since)
	}
	return historyToEvents(q.Workload, entries, cutoff), nil
}

// historyToEvents converts Argo sync history into ChangeEvents. Entries whose
// DeployedAt does not parse as RFC3339 are skipped (they carry no usable time
// to align against other signals); entries older than cutoff are dropped when
// cutoff is non-zero. Output preserves the input order (Argo history is
// already newest-first); MergeSorted re-orders across sources.
func historyToEvents(app string, entries []gitops.ArgoHistoryEntry, cutoff time.Time) []change.ChangeEvent {
	var out []change.ChangeEvent
	for _, e := range entries {
		t, err := time.Parse(time.RFC3339, e.DeployedAt)
		if err != nil {
			continue
		}
		if !cutoff.IsZero() && t.Before(cutoff) {
			continue
		}
		summary := "argo sync"
		if e.Source != "" {
			summary = "argo sync from " + e.Source
		}
		out = append(out, change.ChangeEvent{
			Time:    t,
			Kind:    "sync",
			Target:  app,
			Summary: summary,
			After:   e.Revision,
			Source:  "argo",
		})
	}
	return out
}
