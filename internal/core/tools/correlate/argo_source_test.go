package correlate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/core/tools/change"
	"github.com/rlaope/cloudy/internal/core/tools/gitops"
)

func TestHistoryToEvents(t *testing.T) {
	t.Run("parses timestamp and maps fields", func(t *testing.T) {
		entries := []gitops.ArgoHistoryEntry{
			{Revision: "abc123", DeployedAt: "2026-05-28T12:00:00Z", Source: "git@repo"},
		}
		got := historyToEvents("app", entries, time.Time{})
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		e := got[0]
		if !e.Time.Equal(time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)) {
			t.Errorf("Time = %v, want 2026-05-28T12:00:00Z", e.Time)
		}
		if e.Kind != "sync" {
			t.Errorf("Kind = %q, want sync", e.Kind)
		}
		if e.Target != "app" {
			t.Errorf("Target = %q, want app", e.Target)
		}
		if e.After != "abc123" {
			t.Errorf("After = %q, want abc123", e.After)
		}
		if e.Source != "argo" {
			t.Errorf("Source = %q, want argo", e.Source)
		}
		if e.Summary != "argo sync from git@repo" {
			t.Errorf("Summary = %q, want 'argo sync from git@repo'", e.Summary)
		}
	})

	t.Run("drops unparseable timestamps", func(t *testing.T) {
		entries := []gitops.ArgoHistoryEntry{
			{Revision: "good", DeployedAt: "2026-05-28T12:00:00Z"},
			{Revision: "bad", DeployedAt: "not-a-time"},
		}
		got := historyToEvents("app", entries, time.Time{})
		if len(got) != 1 || got[0].After != "good" {
			t.Fatalf("expected only the parseable entry, got %+v", got)
		}
	})

	t.Run("applies since cutoff", func(t *testing.T) {
		entries := []gitops.ArgoHistoryEntry{
			{Revision: "recent", DeployedAt: "2026-05-28T12:00:00Z"},
			{Revision: "old", DeployedAt: "2026-05-20T12:00:00Z"},
		}
		cutoff := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
		got := historyToEvents("app", entries, cutoff)
		if len(got) != 1 || got[0].After != "recent" {
			t.Fatalf("expected only the entry after the cutoff, got %+v", got)
		}
	})

	t.Run("zero cutoff keeps everything", func(t *testing.T) {
		entries := []gitops.ArgoHistoryEntry{
			{Revision: "a", DeployedAt: "2020-01-01T00:00:00Z"},
			{Revision: "b", DeployedAt: "2026-05-28T12:00:00Z"},
		}
		if got := historyToEvents("app", entries, time.Time{}); len(got) != 2 {
			t.Fatalf("zero cutoff should keep all entries, got %d", len(got))
		}
	})
}

// mockArgoHistory is an argoHistory returning canned entries or an error.
type mockArgoHistory struct {
	entries []gitops.ArgoHistoryEntry
	err     error
	gotApp  string
}

func (m *mockArgoHistory) AppHistory(_ context.Context, app string) ([]gitops.ArgoHistoryEntry, error) {
	m.gotApp = app
	return m.entries, m.err
}

func TestArgoSource_RecentChanges(t *testing.T) {
	t.Run("converts history via the selected endpoint", func(t *testing.T) {
		mock := &mockArgoHistory{entries: []gitops.ArgoHistoryEntry{
			{Revision: "rev1", DeployedAt: "2026-05-28T12:00:00Z"},
		}}
		src := &argoSource{clients: map[string]argoHistory{"prod": mock}}
		got, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "myapp"})
		if err != nil {
			t.Fatalf("RecentChanges: %v", err)
		}
		if mock.gotApp != "myapp" {
			t.Errorf("AppHistory called with %q, want myapp", mock.gotApp)
		}
		if len(got) != 1 || got[0].After != "rev1" || got[0].Source != "argo" {
			t.Fatalf("unexpected events: %+v", got)
		}
	})

	t.Run("propagates client error", func(t *testing.T) {
		src := &argoSource{clients: map[string]argoHistory{"prod": &mockArgoHistory{err: errors.New("boom")}}}
		if _, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "app"}); err == nil {
			t.Fatal("expected error from client")
		}
	})

	t.Run("ambiguous endpoint errors when context empty", func(t *testing.T) {
		src := &argoSource{clients: map[string]argoHistory{
			"a": &mockArgoHistory{}, "b": &mockArgoHistory{},
		}}
		if _, err := src.RecentChanges(context.Background(), change.ChangeQuery{Workload: "app"}); err == nil {
			t.Fatal("expected error selecting among multiple endpoints with empty context")
		}
	})
}

func TestNewArgoSource(t *testing.T) {
	if newArgoSource(nil) != nil {
		t.Error("newArgoSource(nil) should be nil")
	}
	if newArgoSource(map[string]*gitops.ArgoClient{}) != nil {
		t.Error("newArgoSource(empty) should be nil")
	}
	if newArgoSource(map[string]*gitops.ArgoClient{"p": {}}) == nil {
		t.Error("newArgoSource with a client should be non-nil")
	}
}
