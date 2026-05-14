// Package discovery is the coordinator seam between cloudy's /setup flow and
// per-backend detectors. The coordinator owns a list of self-registered
// Detector implementations and runs them concurrently against a shared Env;
// each Detector returns []Finding which is aggregated and stably sorted.
//
// Architecture rule: this package MUST NOT import anything from
// internal/tools/. Dependency direction is reversed — backend packages under
// internal/tools/<kind>/ import discovery and self-register via init().
// Otherwise we'd create the import cycle tools -> discovery -> tools.
package discovery

// Group is the coarse-grained tool group a Finding contributes to.
type Group string

const (
	GroupProm  Group = "prom"
	GroupLog   Group = "log"
	GroupTrace Group = "trace"
	GroupDB    Group = "db"
	GroupPerf  Group = "perf"
)

// Source describes where a Finding came from so the user can recognise it in
// the /setup checkbox view.
type Source struct {
	Context     string // kubeconfig context
	Namespace   string
	ServiceName string // empty for non-service sources
	PodName     string // empty when not relevant
	External    bool   // true when this finding came from a yaml hint
	ExternalURL string // populated when External=true
}

// AuthKind classifies what credential a backend likely needs.
type AuthKind string

const (
	AuthNone     AuthKind = "none"
	AuthBasic    AuthKind = "basic"
	AuthBearer   AuthKind = "bearer"
	AuthPassword AuthKind = "password" // DB-style user/password
)

// AuthHint guides the credential prompt step in /setup.
type AuthHint struct {
	Kind  AuthKind
	Realm string // optional: "prometheus", "redis", ...
}

// Finding is one candidate backend the user can register in /setup.
type Finding struct {
	Group       Group
	Kind        string // "loki", "tempo", "jaeger", "postgres", "mysql", "redis", "pprof", "v8", "prometheus"
	Source      Source
	EndpointURL string  // ready-to-use URL: apiserver proxy for HTTP, "k8s://ns/pod:port" for TCP, or external URL
	Confidence  float64 // 0..1
	AuthHint    AuthHint
	Labels      map[string]string
}
