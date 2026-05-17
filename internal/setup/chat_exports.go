package setup

import (
	"context"
	"time"

	"github.com/rlaope/cloudy/internal/config"
	"github.com/rlaope/cloudy/internal/discovery"
)

// ScanResultsForContexts is an exported wrapper around the private
// scanContextsConcurrent so the TUI's stream-inline /setup flow can
// reuse the same per-context probe the full-screen wizard uses.
// Returns one ContextResult per input context name in the same order.
func ScanResultsForContexts(ctx context.Context, kubeconfigPath string, contexts []string, perCtxTimeout time.Duration) []ContextResult {
	return scanContextsConcurrent(ctx, kubeconfigPath, contexts, perCtxTimeout)
}

// ConvertFindingsForChat is an exported wrapper around the wizard's
// findings-to-typed-config projection. It mirrors the (logs, traces,
// proms, pprof, nodeInspectors, dbs) tuple shape used by the wizard's
// step-7 save logic; the stream-inline /setup flow appends these into
// the same cloudy.yaml slices.
//
// Authentication info is intentionally not threaded through (the
// inline flow does not collect per-finding credentials; the operator
// uses /login or hand-edits cloudy.yaml for that).
func ConvertFindingsForChat(findings []discovery.Finding) (
	logs []config.HTTPEndpoint,
	traces []config.HTTPEndpoint,
	proms []config.PrometheusEndpoint,
	pprofEps []config.HTTPEndpoint,
	nodeEps []config.HTTPEndpoint,
	dbs []config.DatabaseEndpoint,
) {
	return convertFindings(findings, nil)
}
