package setup

import (
	"context"
	"testing"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/discovery"
)

// TestStepOrderingProgression exercises the canonical 7-step flow by directly
// stepping the model through each state. It does not drive the bubbletea
// Program; instead it invokes the transition handlers to verify ordering.
func TestStepOrderingProgression(t *testing.T) {
	m := NewWizardModel(context.Background(), WizardOptions{})

	// Synthetic state so step-3 / step-4 / step-5 init functions have something
	// to chew on. The transitions below run the real init routines.
	m.findings = []discovery.Finding{
		{Group: discovery.GroupProm, Kind: "prometheus", EndpointURL: "http://p"},
		{Group: discovery.GroupDB, Kind: "postgres", Source: discovery.Source{Namespace: "apps", ServiceName: "orders"}, AuthHint: discovery.AuthHint{Kind: discovery.AuthPassword}},
	}

	want := []wizardStep{
		stepKubeconfig,
		stepScan,
		stepDiscovered,
		stepCredentials,
		stepHints,
		stepFillIn,
		stepSkills,
		stepSave,
		stepDone,
	}

	transitions := []func(){
		func() {}, // already at stepKubeconfig
		func() { m.step = stepScan },
		func() {
			m.initDiscovered()
			m.step = stepDiscovered
		},
		func() {
			// Move from discovered -> credentials. commitSelectedFindings flattens
			// groups; initCredentials enqueues anything with an auth hint.
			m.commitSelectedFindings()
			m.initCredentials()
			m.step = stepCredentials
		},
		func() {
			m.initHints()
			m.step = stepHints
		},
		func() {
			m.initFillIn()
			m.step = stepFillIn
		},
		func() {
			m.initSkills()
			m.step = stepSkills
		},
		func() { m.step = stepSave },
		func() { m.step = stepDone },
	}

	for i, fn := range transitions {
		fn()
		if m.step != want[i] {
			t.Fatalf("transition %d: step = %d, want %d", i, m.step, want[i])
		}
	}

	if !m.Done() {
		t.Fatalf("Done() = false at terminal step")
	}
	if m.Aborted() {
		t.Fatalf("Aborted() = true at terminal step")
	}
}

// TestConvertFindings checks the Finding → typed-config conversion in
// isolation. The helper is the testable seam between the (TUI-driven)
// selection state and config persistence.
func TestConvertFindings(t *testing.T) {
	findings := []discovery.Finding{
		{Group: discovery.GroupProm, Kind: "prometheus", EndpointURL: "http://prom:9090"},
		{Group: discovery.GroupLog, Kind: "loki", EndpointURL: "http://loki:3100"},
		{Group: discovery.GroupLog, Kind: "elasticsearch", EndpointURL: "http://es:9200", AuthHint: discovery.AuthHint{Kind: discovery.AuthBearer}},
		{Group: discovery.GroupTrace, Kind: "tempo", EndpointURL: "http://tempo:3200"},
		{Group: discovery.GroupTrace, Kind: "jaeger", EndpointURL: "http://jaeger:16686"},
		{Group: discovery.GroupPerf, Kind: "pprof", EndpointURL: "http://svc:6060"},
		{Group: discovery.GroupPerf, Kind: "v8", EndpointURL: "http://node:9229"},
		{Group: discovery.GroupDB, Kind: "postgres", Source: discovery.Source{Namespace: "apps", ServiceName: "orders-db"}, AuthHint: discovery.AuthHint{Kind: discovery.AuthPassword}},
		{Group: discovery.GroupDB, Kind: "mysql", Source: discovery.Source{Namespace: "apps", ServiceName: "billing-db"}},
		{Group: discovery.GroupDB, Kind: "redis", Source: discovery.Source{Namespace: "infra", ServiceName: "cache"}},
	}

	envByIdx := map[int]string{
		2: "ES_BEARER", // elasticsearch bearer
		7: "PG_PWD",    // postgres password
	}

	logs, traces, proms, pprofEps, nodeEps, dbs := convertFindings(findings, envByIdx)

	// --- prometheus ---
	if len(proms) != 1 {
		t.Fatalf("proms len = %d, want 1", len(proms))
	}
	if got := proms[0]; got.URL != "http://prom:9090" || got.Name != "prom-1" {
		t.Errorf("prom[0] = %+v", got)
	}

	// --- logs (loki + elasticsearch) ---
	if len(logs) != 2 {
		t.Fatalf("logs len = %d, want 2", len(logs))
	}
	if logs[0].Kind != "loki" || logs[1].Kind != "elasticsearch" {
		t.Errorf("log kinds = %q,%q", logs[0].Kind, logs[1].Kind)
	}
	if logs[1].BearerEnv != "ES_BEARER" {
		t.Errorf("elasticsearch bearer env = %q, want ES_BEARER", logs[1].BearerEnv)
	}

	// --- traces ---
	if len(traces) != 2 {
		t.Fatalf("traces len = %d, want 2", len(traces))
	}
	if traces[0].Kind != "tempo" || traces[1].Kind != "jaeger" {
		t.Errorf("trace kinds = %q,%q", traces[0].Kind, traces[1].Kind)
	}

	// --- pprof + node ---
	if len(pprofEps) != 1 || pprofEps[0].Kind != "pprof" {
		t.Errorf("pprof eps = %+v", pprofEps)
	}
	if len(nodeEps) != 1 || nodeEps[0].Kind != "v8" {
		t.Errorf("node eps = %+v", nodeEps)
	}

	// --- databases ---
	if len(dbs) != 3 {
		t.Fatalf("dbs len = %d, want 3", len(dbs))
	}
	wantKinds := []string{"postgres", "mysql", "redis"}
	for i, d := range dbs {
		if d.Kind != wantKinds[i] {
			t.Errorf("db[%d].Kind = %q, want %q", i, d.Kind, wantKinds[i])
		}
		if d.DSN == "" || d.DSN[:6] != "k8s://" {
			t.Errorf("db[%d].DSN = %q, want k8s:// prefix", i, d.DSN)
		}
	}
	if dbs[0].PasswordEnv != "PG_PWD" {
		t.Errorf("postgres PasswordEnv = %q, want PG_PWD", dbs[0].PasswordEnv)
	}
}

// TestParseAndStoreHint checks that the step-5 mini-parser routes lines into
// the correct slice and assigns auto-incrementing names.
func TestParseAndStoreHint(t *testing.T) {
	m := NewWizardModel(context.Background(), WizardOptions{})

	cases := []struct {
		line       string
		wantErr    bool
		wantHTTPN  int
		wantDBN    int
		lastKind   string
		lastBearer string
		lastDSN    string
	}{
		{line: "prom http://p:9090", wantHTTPN: 1, wantDBN: 0, lastKind: "prom"},
		{line: "loki http://l:3100 LOKI_TOKEN", wantHTTPN: 2, wantDBN: 0, lastKind: "loki", lastBearer: "LOKI_TOKEN"},
		{line: "postgres postgres://u@h:5432/db", wantHTTPN: 2, wantDBN: 1, lastDSN: "postgres://u@h:5432/db"},
		{line: "garbage", wantErr: true, wantHTTPN: 2, wantDBN: 1},
		{line: "redis 127.0.0.1:6379", wantHTTPN: 2, wantDBN: 2, lastDSN: "127.0.0.1:6379"},
	}

	for _, tc := range cases {
		err := m.parseAndStoreHint(tc.line)
		if tc.wantErr && err == nil {
			t.Fatalf("%q: expected error", tc.line)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%q: unexpected error: %v", tc.line, err)
		}
		if got := len(m.httpHints); got != tc.wantHTTPN {
			t.Fatalf("%q: httpHints len = %d, want %d", tc.line, got, tc.wantHTTPN)
		}
		if got := len(m.dbHints); got != tc.wantDBN {
			t.Fatalf("%q: dbHints len = %d, want %d", tc.line, got, tc.wantDBN)
		}
		if tc.lastKind != "" {
			if last := m.httpHints[len(m.httpHints)-1]; last.Kind != tc.lastKind {
				t.Fatalf("%q: last http kind = %q, want %q", tc.line, last.Kind, tc.lastKind)
			}
		}
		if tc.lastBearer != "" {
			if last := m.httpHints[len(m.httpHints)-1]; last.BearerEnv != tc.lastBearer {
				t.Fatalf("%q: last http bearer = %q, want %q", tc.line, last.BearerEnv, tc.lastBearer)
			}
		}
		if tc.lastDSN != "" {
			if last := m.dbHints[len(m.dbHints)-1]; last.DSN != tc.lastDSN {
				t.Fatalf("%q: last db dsn = %q, want %q", tc.line, last.DSN, tc.lastDSN)
			}
		}
	}
}

// TestGeneratedEnvVarName verifies the CLOUDY_<KIND>_<NAME>_PWD convention.
func TestGeneratedEnvVarName(t *testing.T) {
	cases := []struct {
		f    discovery.Finding
		want string
	}{
		{
			f:    discovery.Finding{Kind: "postgres", Source: discovery.Source{ServiceName: "orders-db"}},
			want: "CLOUDY_POSTGRES_ORDERS_DB_PWD",
		},
		{
			f:    discovery.Finding{Kind: "redis", Source: discovery.Source{ServiceName: "cache-1"}},
			want: "CLOUDY_REDIS_CACHE_1_PWD",
		},
		{
			f:    discovery.Finding{Kind: "loki", Source: discovery.Source{External: true, ExternalURL: "http://loki.example.com"}},
			want: "CLOUDY_LOKI_HTTP___LOKI_EXAMPLE_COM_PWD",
		},
	}
	for _, tc := range cases {
		if got := generatedEnvVarName(tc.f); got != tc.want {
			t.Errorf("generatedEnvVarName(%+v) = %q, want %q", tc.f, got, tc.want)
		}
	}
}

// TestConvertFindings_EmptySelection covers the zero-finding edge case.
func TestConvertFindings_EmptySelection(t *testing.T) {
	logs, traces, proms, pprofEps, nodeEps, dbs := convertFindings(nil, nil)
	if len(logs)+len(traces)+len(proms)+len(pprofEps)+len(nodeEps)+len(dbs) != 0 {
		t.Fatalf("expected empty result, got %d/%d/%d/%d/%d/%d",
			len(logs), len(traces), len(proms), len(pprofEps), len(nodeEps), len(dbs))
	}
}

// Compile-time assertion: ensure the wizard emits the right config types.
var _ = []config.HTTPEndpoint(nil)
var _ = []config.DatabaseEndpoint(nil)
var _ = []config.PrometheusEndpoint(nil)
