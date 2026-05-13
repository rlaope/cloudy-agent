package log_test

import (
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/tools"
	tlog "github.com/rlaope/cloudy/internal/tools/log"
)

func TestBuildClients_NoEndpoints(t *testing.T) {
	t.Parallel()
	cs, skips := tlog.BuildClients(nil)
	if !cs.Empty() || len(skips) != 0 {
		t.Errorf("expected empty, got cs=%+v skips=%v", cs, skips)
	}
}

func TestBuildClients_UnknownKindRecordsSkip(t *testing.T) {
	t.Parallel()
	_, skips := tlog.BuildClients([]config.HTTPEndpoint{
		{Name: "weird", Kind: "splunk", URL: "http://localhost:9999"},
	})
	if len(skips) == 0 || !strings.Contains(skips[0], "unknown kind") {
		t.Errorf("expected unknown-kind skip, got %v", skips)
	}
}

func TestBuildClients_MissingFieldsRecordsSkip(t *testing.T) {
	t.Parallel()
	_, skips := tlog.BuildClients([]config.HTTPEndpoint{
		{Name: "", Kind: "loki", URL: "http://localhost:3100"},
	})
	if len(skips) == 0 || !strings.Contains(skips[0], "missing name or url") {
		t.Errorf("expected missing-field skip, got %v", skips)
	}
}

func TestRegisterAll_EmptyMarksGroupSkipped(t *testing.T) {
	t.Parallel()
	reg := tools.New()
	tlog.RegisterAll(reg, tlog.Clients{}, []string{`loki "prod": ping: connection refused`})

	r, ok := reg.Skipped()["log"]
	if !ok {
		t.Fatalf("expected group log skipped")
	}
	if !strings.Contains(r, "no usable log endpoints") {
		t.Errorf("expected composed reason, got %q", r)
	}
}

func TestRegisterAll_LokiOnly(t *testing.T) {
	t.Parallel()
	cs, _ := tlog.BuildClients([]config.HTTPEndpoint{
		{Name: "prod", Kind: "loki", URL: "http://loki:3100"},
	})
	reg := tools.New()
	tlog.RegisterAll(reg, cs, nil)

	wantTools := []string{
		"log.loki_query_range",
		"log.loki_labels",
		"log.loki_label_values",
		"log.loki_series",
	}
	for _, n := range wantTools {
		if _, ok := reg.Get(n); !ok {
			t.Errorf("expected %q registered", n)
		}
	}
	if _, ok := reg.Get("log.es_search"); ok {
		t.Errorf("did not expect log.es_search without ES endpoint")
	}
}
