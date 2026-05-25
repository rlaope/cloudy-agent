package trace_test

import (
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/trace"
)

func TestBuildClients_NoEndpoints(t *testing.T) {
	t.Parallel()
	cs, skips := trace.BuildClients(nil)
	if !cs.Empty() || len(skips) != 0 {
		t.Errorf("expected empty, got cs=%+v skips=%v", cs, skips)
	}
}

func TestBuildClients_UnknownKindRecordsSkip(t *testing.T) {
	t.Parallel()
	_, skips := trace.BuildClients([]config.HTTPEndpoint{
		{Name: "weird", Kind: "zipkin", URL: "http://localhost:9999"},
	})
	if len(skips) == 0 || !strings.Contains(skips[0], "unknown kind") {
		t.Errorf("expected unknown-kind skip, got %v", skips)
	}
}

func TestRegisterAll_EmptyMarksGroupSkipped(t *testing.T) {
	t.Parallel()
	reg := tools.New()
	trace.RegisterAll(reg, trace.Clients{}, nil)
	r, ok := reg.Skipped()["trace"]
	if !ok {
		t.Fatalf("expected group trace skipped")
	}
	if !strings.Contains(r, "no tracing endpoints configured") {
		t.Errorf("expected default reason, got %q", r)
	}
}

func TestRegisterAll_TempoAndJaeger(t *testing.T) {
	t.Parallel()
	cs, _ := trace.BuildClients([]config.HTTPEndpoint{
		{Name: "tempo-prod", Kind: "tempo", URL: "http://tempo:3200"},
		{Name: "jaeger-prod", Kind: "jaeger", URL: "http://jaeger:16686"},
	})
	reg := tools.New()
	trace.RegisterAll(reg, cs, nil)

	wantTools := []string{
		"trace.tempo_get_trace",
		"trace.tempo_search",
		"trace.service_graph",
		"trace.route_red",
		"trace.jaeger_services",
		"trace.jaeger_operations",
		"trace.jaeger_search_traces",
	}
	for _, n := range wantTools {
		if _, ok := reg.Get(n); !ok {
			t.Errorf("expected %q registered", n)
		}
	}
}
