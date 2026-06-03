package incidentmemory

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rlaope/cloudy/internal/permission"
)

func TestValidateCard_RequiresCoreFields(t *testing.T) {
	err := Validate(Card{})
	if err == nil {
		t.Fatal("Validate(empty) got nil")
	}
	for _, want := range []string{"symptoms required", "affected_service required", "source required", "confidence must be between 0 and 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate error = %q, want %q", err, want)
		}
	}
}

func TestValidateCard_CauseStatus(t *testing.T) {
	c := validCard()
	c.CauseStatus = "proven"
	err := Validate(c)
	if err == nil || !strings.Contains(err.Error(), "cause_status must be suspected or confirmed") {
		t.Fatalf("Validate cause_status err = %v", err)
	}
}

func TestStore_CreateCandidate(t *testing.T) {
	store := testStore(t)
	card, err := store.CreateCandidate(validCard())
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}
	if card.Status != StatusCandidate {
		t.Fatalf("status = %q, want candidate", card.Status)
	}
	if card.ID == "" || card.CreatedAt.IsZero() || card.UpdatedAt.IsZero() {
		t.Fatalf("candidate missing generated fields: %+v", card)
	}
	got, err := store.Get(card.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AffectedService != "payments-api" {
		t.Fatalf("persisted card = %+v", got)
	}
}

func TestStore_ApproveReject(t *testing.T) {
	store := testStore(t)
	card, err := store.CreateCandidate(validCard())
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}
	approved, err := store.Approve(card.ID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.Status != StatusApproved {
		t.Fatalf("approved status = %q", approved.Status)
	}
	rejected, err := store.Reject(card.ID)
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.Status != StatusRejected {
		t.Fatalf("rejected status = %q", rejected.Status)
	}
	if rejected.AffectedService != card.AffectedService {
		t.Fatalf("status transition changed fields: %+v", rejected)
	}
}

func TestStore_CachesSnapshotWithoutAliasingCallers(t *testing.T) {
	store := testStore(t)
	card, err := store.CreateCandidate(validCard())
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}
	first, err := store.List()
	if err != nil {
		t.Fatalf("List first: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first len = %d, want 1", len(first))
	}
	first[0].Symptoms[0] = "mutated by caller"
	second, err := store.List()
	if err != nil {
		t.Fatalf("List second: %v", err)
	}
	if second[0].Symptoms[0] != "p99 latency spike" {
		t.Fatalf("cached card aliased caller mutation: %+v", second[0])
	}
	if _, err := store.Approve(card.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	third, err := store.List()
	if err != nil {
		t.Fatalf("List third: %v", err)
	}
	if third[0].Status != StatusApproved {
		t.Fatalf("cache was not refreshed after rewrite: %+v", third[0])
	}
}

func TestStore_ConcurrentStatusTransitionsKeepValidJSONL(t *testing.T) {
	store := testStore(t)
	var ids []string
	for i := 0; i < 8; i++ {
		c := validCard()
		c.Source.ID = "INC-" + string(rune('A'+i))
		card, err := store.CreateCandidate(c)
		if err != nil {
			t.Fatalf("CreateCandidate %d: %v", i, err)
		}
		ids = append(ids, card.ID)
	}
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = store.Approve(id)
			} else {
				_, _ = store.Reject(id)
			}
		}(i, id)
	}
	wg.Wait()
	cards, err := store.List()
	if err != nil {
		t.Fatalf("List after concurrent transitions: %v", err)
	}
	if len(cards) != len(ids) {
		t.Fatalf("cards len = %d, want %d", len(cards), len(ids))
	}
}

func TestStore_ApproveDoesNotMutateMemoryMD(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLOUDY_HOME", home)
	store := NewDefaultStore()
	card, err := store.CreateCandidate(validCard())
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}
	if _, err := store.Approve(card.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "memory.md")); !os.IsNotExist(err) {
		t.Fatalf("memory.md should not be created by incident approval, err=%v", err)
	}
}

func TestRetrieve_EmptyStore(t *testing.T) {
	store := testStore(t)
	got, err := store.Retrieve(QueryFromText("payments latency"), RetrieveOptions{})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty store returned cases: %+v", got)
	}
}

func TestStore_Permissions(t *testing.T) {
	store := testStore(t)
	if _, err := store.CreateCandidate(validCard()); err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}
	dirInfo, err := os.Stat(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("dir mode = %o, want 0700", got)
	}
	fileInfo, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 0600", got)
	}
}

func TestStore_RedactsBeforePersist(t *testing.T) {
	store := testStore(t)
	c := validCard()
	c.Signals = []string{"token AKIAIOSFODNN7EXAMPLE appeared in logs"}
	card, err := store.CreateCandidate(c)
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("secret persisted to disk: %s", data)
	}
	if strings.Contains(strings.Join(card.Signals, " "), "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("secret returned in card: %+v", card.Signals)
	}
}

func TestStore_RedactsJSONKeyValuesBeforePersist(t *testing.T) {
	store := testStore(t)
	c := validCard()
	c.Signals = []string{`{"password":"plain-secret","status":"failing"}`}
	c.Cause = `{"api_key":"secret-value","cause":"pool exhaustion"}`
	if _, err := store.CreateCandidate(c); err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, secret := range []string{"plain-secret", "secret-value"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("JSON key secret persisted to disk: %s", data)
		}
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("redacted marker missing from persisted card: %s", data)
	}
}

func TestStore_DoesNotPersistCallerIDOrSourceTypeSecret(t *testing.T) {
	store := testStore(t)
	c := validCard()
	c.ID = `{"password":"id-secret"}`
	c.Source.Type = `{"password":"type-secret"}`
	card, err := store.CreateCandidate(c)
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}
	if strings.Contains(card.ID, "id-secret") {
		t.Fatalf("caller-provided id was persisted: %s", card.ID)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, secret := range []string{"id-secret", "type-secret"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("secret persisted to disk: %s", data)
		}
	}
}

func TestStore_LoadSkipsMalformedAndInvalidImportedCards(t *testing.T) {
	store := testStore(t)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := strings.Join([]string{
		`not-json`,
		strings.Repeat("x", 1024*1024+10),
		`{"id":"{\"password\":\"id-secret\"}","created_at":"2026-06-03T00:00:00Z","updated_at":"2026-06-03T00:00:00Z","status":"approved","symptoms":["latency"],"affected_service":"payments-api","signals":["5xx"],"cause_status":"suspected","source":{"type":"postmortem","id":"INC-raw"},"confidence":0.7}`,
		`{"id":"case-20260603T000001Z-001122334455","created_at":"2026-06-03T00:00:00Z","updated_at":"2026-06-03T00:00:00Z","status":"{\"password\":\"status-secret\"}","symptoms":["latency"],"affected_service":"payments-api","cause_status":"suspected","source":{"type":"postmortem","id":"INC-bad-status"},"confidence":0.7}`,
		`{"id":"case-20260603T000002Z-001122334455","created_at":"2026-06-03T00:00:00Z","updated_at":"2026-06-03T00:00:00Z","status":"approved","symptoms":["latency"],"affected_service":"payments-api","cause_status":"{\"password\":\"cause-secret\"}","source":{"type":"postmortem","id":"INC-bad-cause"},"confidence":0.7}`,
		"",
	}, "\n")
	if err := os.WriteFile(store.Path(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cards, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards len = %d, want 1: %+v", len(cards), cards)
	}
	if cards[0].ID != "imported-line-3" {
		t.Fatalf("loaded id = %q, want opaque imported id", cards[0].ID)
	}
	for _, secret := range []string{"id-secret", "status-secret", "cause-secret"} {
		if strings.Contains(cards[0].ID, secret) || strings.Contains(cards[0].Status, secret) || strings.Contains(cards[0].CauseStatus, secret) {
			t.Fatalf("loaded card leaked metadata secret %q: %+v", secret, cards[0])
		}
	}
	got, err := store.Retrieve(QueryFromText("payments-api latency"), RetrieveOptions{})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 1 || got[0].Card.ID != "imported-line-3" {
		t.Fatalf("Retrieve = %+v, want surviving imported card", got)
	}
}

func TestRetrieve_ApprovedOnlyByDefault(t *testing.T) {
	store := testStore(t)
	approved, rejected := seedRetrievalCards(t, store)
	if _, err := store.Approve(approved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reject(rejected.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.Retrieve(Query{AffectedService: "payments-api", Symptoms: []string{"p99 latency spike"}}, RetrieveOptions{})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 1 || got[0].Card.ID != approved.ID {
		t.Fatalf("Retrieve default = %+v, want approved only", got)
	}
}

func TestRetrieve_BroadSimilarityAndDifferenceNotes(t *testing.T) {
	store := testStore(t)
	card, err := store.CreateCandidate(validCard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Approve(card.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.Retrieve(Query{Symptoms: []string{"latency spike"}, Signals: []string{"redis errors"}}, RetrieveOptions{})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Retrieve len = %d, want 1", len(got))
	}
	if got[0].DifferenceNotes == "" || len(got[0].Reasons) == 0 {
		t.Fatalf("Retrieve missing difference/reasons: %+v", got[0])
	}
}

func TestRetrieve_FreeTextMatchesAffectedService(t *testing.T) {
	store := testStore(t)
	card, err := store.CreateCandidate(validCard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Approve(card.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.Retrieve(QueryFromText("payments-api is slow"), RetrieveOptions{})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) != 1 || got[0].Card.ID != card.ID {
		t.Fatalf("free-text service match = %+v, want %s", got, card.ID)
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	m, err := permission.NewMasker(&permission.Profile{Masking: permission.DefaultMaskingPatterns()})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(t.TempDir(), "incident-memory", "cards.jsonl"), m)
	now := time.Date(2026, 6, 3, 1, 2, 3, 0, time.UTC)
	store.now = func() time.Time { return now }
	return store
}

func validCard() Card {
	return Card{
		Symptoms:         []string{"p99 latency spike", "checkout timeout"},
		AffectedService:  "payments-api",
		Signals:          []string{"redis errors", "5xx burn rate"},
		CauseStatus:      CauseConfirmed,
		Cause:            "redis client pool exhaustion",
		FixOrMitigation:  "raised pool limit and rolled payments",
		WhatWasDifferent: "current incident also had a deploy in progress",
		Source:           Source{Type: "postmortem", ID: "INC-142", Ref: "docs/postmortems/INC-142.md"},
		Confidence:       0.82,
		Tags:             []string{"redis", "latency"},
	}
}

func seedRetrievalCards(t *testing.T, store *Store) (Card, Card) {
	t.Helper()
	a, err := store.CreateCandidate(validCard())
	if err != nil {
		t.Fatal(err)
	}
	bc := validCard()
	bc.Source.ID = "INC-143"
	bc.Cause = "different cause"
	b, err := store.CreateCandidate(bc)
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}
