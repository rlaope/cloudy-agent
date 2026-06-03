package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rlaope/cloudy/internal/incidentmemory"
)

func TestIncidentMemoryPrompt_CaveatAndCap(t *testing.T) {
	cases := []incidentmemory.SimilarCase{{
		Card: incidentmemory.Card{
			ID:               "case-1",
			Status:           incidentmemory.StatusApproved,
			Symptoms:         []string{"p99 latency spike"},
			Signals:          []string{"redis errors"},
			AffectedService:  "payments-api",
			CauseStatus:      incidentmemory.CauseConfirmed,
			Cause:            "redis client pool exhaustion",
			FixOrMitigation:  "raised pool limit",
			WhatWasDifferent: "current deploy state unknown",
			Source:           incidentmemory.Source{Type: "postmortem", ID: "INC-142"},
			Confidence:       0.82,
		},
		Reasons:         []string{"symptom overlap: latency"},
		DifferenceNotes: "current deploy state unknown",
	}}
	rendered := RenderIncidentMemoryPrompt(cases, nil, 1, 1000)
	for _, want := range []string{"Prior similar incident cases", "not proof of the current root cause", "what was different"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered prompt missing %q:\n%s", want, rendered)
		}
	}
	capped := RenderIncidentMemoryPrompt(cases, nil, 1, 120)
	if len(capped) > 160 {
		t.Fatalf("rendered prompt not capped: len=%d", len(capped))
	}
}

func TestIncidentMemoryPrompt_TruncatesUTF8Safely(t *testing.T) {
	cases := []incidentmemory.SimilarCase{{
		Card: incidentmemory.Card{
			ID:              "case-utf8",
			Status:          incidentmemory.StatusApproved,
			Symptoms:        []string{strings.Repeat("장애", 80)},
			AffectedService: "payments-api",
			CauseStatus:     incidentmemory.CauseSuspected,
			Source:          incidentmemory.Source{Type: "postmortem", ID: "INC-UTF8"},
			Confidence:      0.8,
		},
		Score:   1,
		Reasons: []string{"symptom overlap: 장애"},
	}}
	rendered := RenderIncidentMemoryPrompt(cases, nil, 1, 180)
	if !utf8.ValidString(rendered) {
		t.Fatalf("rendered prompt is invalid UTF-8: %q", rendered)
	}
	if !strings.Contains(rendered, "truncated") {
		t.Fatalf("rendered prompt should indicate truncation:\n%s", rendered)
	}
}

func TestIncidentMemoryPrompt_RedactsPreExistingJSONKeySecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDY_HOME", home)
	dir := filepath.Join(home, "incident-memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"id":"case-raw","created_at":"2026-06-03T00:00:00Z","updated_at":"2026-06-03T00:00:00Z","status":"approved","symptoms":["latency"],"affected_service":"payments-api","signals":["{\"password\":\"plain-secret\",\"symptom\":\"latency\"}"],"cause_status":"suspected","source":{"type":"{\"password\":\"type-secret\"}","id":"INC-raw"},"confidence":0.7}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "cards.jsonl"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	rendered, err := BuildIncidentMemoryPrompt("payments-api latency", nil)
	if err != nil {
		t.Fatalf("BuildIncidentMemoryPrompt: %v", err)
	}
	for _, secret := range []string{"plain-secret", "type-secret"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("prompt leaked secret %q:\n%s", secret, rendered)
		}
	}
	if !strings.Contains(rendered, "[REDACTED]") {
		t.Fatalf("prompt missing redacted marker:\n%s", rendered)
	}
}

func TestIncidentMemoryPrompt_ApprovedCardFromStore(t *testing.T) {
	t.Setenv("CLOUDY_HOME", t.TempDir())
	store := incidentmemory.NewDefaultStore()
	card, err := store.CreateCandidate(incidentmemory.Card{
		Symptoms:        []string{"latency spike"},
		AffectedService: "payments-api",
		Signals:         []string{"redis errors"},
		CauseStatus:     incidentmemory.CauseSuspected,
		Source:          incidentmemory.Source{Type: "postmortem", ID: "INC-142"},
		Confidence:      0.7,
		CreatedAt:       time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}
	if _, err := store.Approve(card.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	rendered, err := BuildIncidentMemoryPrompt("payments-api latency", nil)
	if err != nil {
		t.Fatalf("BuildIncidentMemoryPrompt: %v", err)
	}
	if !strings.Contains(rendered, card.ID) {
		t.Fatalf("prompt missing approved card id %s:\n%s", card.ID, rendered)
	}
}
