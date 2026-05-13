package perf_test

import (
	"strings"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/tools"
	"github.com/rlaope/cloudy/internal/tools/perf"
)

func TestBuildClients_MissingFields(t *testing.T) {
	t.Parallel()
	_, skips := perf.BuildClients(
		[]config.HTTPEndpoint{{Name: "", URL: ""}},
		nil,
	)
	if len(skips) == 0 || !strings.Contains(skips[0], "missing name or url") {
		t.Errorf("expected missing-field skip, got %v", skips)
	}
}

func TestRegisterAll_RbspyAlwaysRegistered(t *testing.T) {
	t.Parallel()
	reg := tools.New()
	perf.RegisterAll(reg, perf.Clients{}, nil)

	if _, ok := reg.Get("perf.rbspy_dump"); !ok {
		t.Errorf("expected perf.rbspy_dump registered even with no HTTP backends")
	}
	skipped := reg.Skipped()
	if _, ok := skipped["perf-pprof"]; !ok {
		t.Errorf("expected perf-pprof skipped reason")
	}
	if _, ok := skipped["perf-v8"]; !ok {
		t.Errorf("expected perf-v8 skipped reason")
	}
}

func TestRegisterAll_PprofRegistersAllFourTools(t *testing.T) {
	t.Parallel()
	cs, _ := perf.BuildClients(
		[]config.HTTPEndpoint{{Name: "api", Kind: "pprof", URL: "http://api:6060"}},
		nil,
	)
	reg := tools.New()
	perf.RegisterAll(reg, cs, nil)

	wantTools := []string{
		"perf.go_pprof_goroutine",
		"perf.go_pprof_heap",
		"perf.go_pprof_allocs",
		"perf.go_pprof_threadcreate",
		"perf.rbspy_dump",
	}
	for _, n := range wantTools {
		if _, ok := reg.Get(n); !ok {
			t.Errorf("expected %q registered", n)
		}
	}
	if _, ok := reg.Get("perf.v8_inspector_targets"); ok {
		t.Errorf("did not expect v8 tool without node_inspectors")
	}
}
