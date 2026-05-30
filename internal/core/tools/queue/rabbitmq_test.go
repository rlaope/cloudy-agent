package queue

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/clients/httpapi"
)

func fptr(f float64) *float64 { return &f }

// TestRenderRabbitQueues_RanksAndFlags pins the two on-call failure modes: a
// backlogged queue with no consumer is flagged NO CONSUMER, a queue whose
// consumers are behind (unacked + low utilisation) is flagged FALLING BEHIND,
// and the highest backlog sorts first.
func TestRenderRabbitQueues_RanksAndFlags(t *testing.T) {
	queues := []rabbitQueue{
		{Name: "idle", Vhost: "/", Messages: 0, Consumers: 2, ConsumerUtil: fptr(1.0)},
		{Name: "stuck", Vhost: "/", Messages: 40000, Ready: 40000, Unacked: 0, Consumers: 0},
		{Name: "behind", Vhost: "payments", Messages: 5000, Ready: 4000, Unacked: 1000, Consumers: 3, ConsumerUtil: fptr(0.2)},
	}
	out := renderRabbitQueues("rmq", queues, 0, 20)

	// Highest backlog (stuck, 40k) must appear before the 5k queue.
	if strings.Index(out, "stuck") > strings.Index(out, "/behind") {
		t.Errorf("queues should rank by backlog desc; got:\n%s", out)
	}
	if !strings.Contains(out, "NO CONSUMER") {
		t.Errorf("a backlogged queue with 0 consumers must be flagged NO CONSUMER; got:\n%s", out)
	}
	if !strings.Contains(out, "FALLING BEHIND") {
		t.Errorf("a queue with unacked + low utilisation must be flagged FALLING BEHIND; got:\n%s", out)
	}
	if !strings.Contains(out, "1 queue(s) backlogged with no consumer; 1 queue(s) with consumers falling behind") {
		t.Errorf("summary should count both failure modes; got:\n%s", out)
	}
}

// TestRenderRabbitQueues_MinMessagesAndLimit covers the two view controls:
// min_messages drops quiet queues, limit caps the rows and reports the
// remainder instead of silently truncating.
func TestRenderRabbitQueues_MinMessagesAndLimit(t *testing.T) {
	queues := []rabbitQueue{
		{Name: "a", Vhost: "/", Messages: 100, Consumers: 1, ConsumerUtil: fptr(1)},
		{Name: "b", Vhost: "/", Messages: 50, Consumers: 1, ConsumerUtil: fptr(1)},
		{Name: "c", Vhost: "/", Messages: 5, Consumers: 1, ConsumerUtil: fptr(1)},
	}
	out := renderRabbitQueues("rmq", queues, 50, 1)
	if strings.Contains(out, "/c ") {
		t.Errorf("min_messages=50 should drop the 5-message queue; got:\n%s", out)
	}
	if !strings.Contains(out, "…and 1 more") {
		t.Errorf("limit=1 over 2 surviving queues should report 1 more; got:\n%s", out)
	}
}

// TestRenderRabbitQueues_NilUtilisation pins that a queue whose
// consumer_utilisation is absent (the API omits it until the queue has had a
// consumer) renders util=— and, when it has a backlog with consumers attached,
// is still flagged FALLING BEHIND rather than slipping through unflagged.
func TestRenderRabbitQueues_NilUtilisation(t *testing.T) {
	out := renderRabbitQueues("rmq", []rabbitQueue{
		{Name: "noutil", Vhost: "/", Messages: 300, Ready: 300, Unacked: 0, Consumers: 2, ConsumerUtil: nil},
	}, 0, 20)
	if !strings.Contains(out, "util=—") {
		t.Errorf("nil consumer_utilisation should render as util=—; got:\n%s", out)
	}
	if !strings.Contains(out, "FALLING BEHIND") {
		t.Errorf("a backlogged queue with consumers but unknown utilisation should flag FALLING BEHIND; got:\n%s", out)
	}
}

// TestRabbitMQQueuesTool_EndToEnd drives the tool through a fake management API
// to confirm the path, the column projection, JSON decode, and render wire
// together.
func TestRabbitMQQueuesTool_EndToEnd(t *testing.T) {
	body := mustMarshal(t, []rabbitQueue{
		{Name: "orders", Vhost: "/", Messages: 1200, Ready: 1200, Unacked: 0, Consumers: 0},
	})
	var gotPath, gotColumns string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotColumns = r.URL.Query().Get("columns")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	cl, err := httpapi.NewClient("rmq", srv.URL, httpapi.Auth{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	tool := newRabbitMQQueuesTool(map[string]*httpapi.Client{"rmq": cl})

	obs, err := tool.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotPath != "/api/queues" {
		t.Errorf("default request path = %q, want /api/queues", gotPath)
	}
	if !strings.Contains(gotColumns, "messages_ready") {
		t.Errorf("request should project columns including messages_ready; got %q", gotColumns)
	}
	if !strings.Contains(obs.Text, "orders") || !strings.Contains(obs.Text, "NO CONSUMER") {
		t.Errorf("observation should describe the stuck orders queue; got:\n%s", obs.Text)
	}
}

// TestRabbitMQQueuesTool_ErrorPropagates pins that a non-2xx from the
// management API (auth failure, broker down) surfaces as a tool error rather
// than an empty queue list — the operator must see the backend failed during
// the incident this tool is meant to catch.
func TestRabbitMQQueuesTool_ErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable"))
	}))
	defer srv.Close()

	cl, err := httpapi.NewClient("rmq", srv.URL, httpapi.Auth{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	tool := newRabbitMQQueuesTool(map[string]*httpapi.Client{"rmq": cl})

	if _, err := tool.Run(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("a 5xx from the management API must propagate as an error")
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
