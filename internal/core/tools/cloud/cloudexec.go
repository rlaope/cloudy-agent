// Package cloud provides read-only cloud-provider observability tools (AWS,
// GCP, Azure) by shelling out to the operator's already-configured `aws`,
// `gcloud`, and `az` CLIs. cloudy stores no cloud secrets — every credential
// resolves from the CLI's own chain (env, SSO, assume-role, instance/workload
// identity).
//
// Read-only enforcement note: the HTTP transport guard
// (internal/transport/readonly.go) only covers Go http.Client traffic. A
// subprocess does NOT pass through it. The read-only boundary for this group
// is therefore re-established here, in CloudExec, by an explicit allowlist of
// "<service> <verb>" sub-command prefixes plus argv-only execution (no shell,
// no string interpolation). Tool names additionally pass the registry's
// mutator-token guard, and operators are expected to attach least-privilege
// read-only IAM. See docs/RFC-CLOUD-OBSERVABILITY.md.
package cloud

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// maxCloudOutputBytes caps CLI stdout so a pathological response cannot
// exhaust memory. Cloud metric/list payloads are small; 4 MiB is generous.
const maxCloudOutputBytes = 4 << 20

// allowedSubcommands is the read-only allowlist keyed by CLI binary. The value
// set holds the permitted command-path prefixes (the leading non-flag tokens
// of a command). A command whose prefix is absent is refused before exec.
//
// Only read verbs (list/get/describe/show) appear here by construction; this
// map IS the security boundary, so any addition must be a read-only operation.
var allowedSubcommands = map[string]map[string]struct{}{
	"aws": {
		"cloudwatch list-metrics":          {},
		"cloudwatch get-metric-statistics": {},
		"cloudwatch get-metric-data":       {},
		// CloudWatch Logs (read-only): describe + filter, plus Logs Insights
		// start-query/get-query-results. logs:StartQuery is a read permission —
		// it executes an ephemeral query job, it does not mutate any resource.
		"logs describe-log-groups": {},
		"logs filter-log-events":   {},
		"logs start-query":         {},
		"logs get-query-results":   {},
		// X-Ray traces (read-only): trace summaries + full segments + the
		// service-dependency graph. All are GetXxx APIs — no mutation.
		"xray get-trace-summaries": {},
		"xray batch-get-traces":    {},
		"xray get-service-graph":   {},
		// Inventory / managed-service health (read-only Describe/List).
		"rds describe-db-instances": {},
		"lambda list-functions":     {},
		"eks list-clusters":         {},
	},
	"az": {
		"monitor metrics list":             {},
		"monitor metrics list-definitions": {},
		"monitor log-analytics query":      {},
		// Application Insights KQL query (read-only; KQL cannot mutate).
		"monitor app-insights query": {},
		// Inventory / managed-service health (read-only list verbs).
		"sql server list":  {},
		"functionapp list": {},
		"aks list":         {},
	},
	"gcloud": {
		// Inventory / managed-service health (read-only list verbs). Unlike
		// gcloud's absent metric/trace reads, these list commands are first-class
		// read-only and JSON-capable.
		"sql instances list":      {},
		"run services list":       {},
		"container clusters list": {},
		// Cloud Logging read-only. `gcloud logging read` takes the filter as a
		// trailing positional, so cloud tools emit `logging read` immediately
		// followed by flags (--project …) and append the filter LAST — keeping
		// subcommandPrefix == "logging read". GCP metric and trace reads stay
		// deferred: `gcloud monitoring` exposes no time-series read and there is
		// no stable `gcloud trace` read command (see docs/RFC-CLOUD-OBSERVABILITY.md).
		"logging read": {},
	},
}

// cloudExecRunner is the subprocess runner, a package var so tests can stub it
// without invoking real CLIs.
var cloudExecRunner = runCloudExec

// CloudExec runs an allowlisted read-only CLI sub-command and returns stdout.
//
// Security properties:
//   - argv-only: bin + args are passed straight to exec.CommandContext as a
//     vector; there is no shell and no string interpolation, so no LLM- or
//     config-supplied value can inject a second command or a shell metachar.
//   - allowlist: the command-path prefix (leading non-flag tokens) must be
//     present in allowedSubcommands[bin]; otherwise the call is refused before
//     exec with ErrSubcommandNotAllowed.
//   - bounded: the caller's ctx deadline applies, and stdout is truncated at
//     maxCloudOutputBytes.
func CloudExec(ctx context.Context, bin string, args []string) ([]byte, error) {
	allowed, ok := allowedSubcommands[bin]
	if !ok {
		return nil, fmt.Errorf("%w: binary %q is not an allowed cloud CLI", ErrSubcommandNotAllowed, bin)
	}
	prefix := subcommandPrefix(args)
	if _, ok := allowed[prefix]; !ok {
		return nil, fmt.Errorf("%w: %s %q is not a read-only allowlisted sub-command", ErrSubcommandNotAllowed, bin, prefix)
	}
	return cloudExecRunner(ctx, bin, args)
}

// subcommandPrefix joins the leading run of non-flag tokens — the full
// command path before the first flag — e.g.
//
//	["cloudwatch","list-metrics","--namespace","AWS/EC2"] → "cloudwatch list-metrics"
//	["monitor","metrics","list","--resource","x"]         → "monitor metrics list"
//
// This handles variable-length command paths (aws verbs are 2 tokens, az verbs
// are 3) while matching the allowlist keys exactly. cloud tools always build
// argv as <command path> followed by flags, so stopping at the first flag is
// exact; a flag appearing first yields an empty prefix and is refused.
func subcommandPrefix(args []string) string {
	var head []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}
		head = append(head, a)
	}
	return strings.Join(head, " ")
}

// runCloudExec is the real implementation behind cloudExecRunner.
func runCloudExec(ctx context.Context, bin string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		stderr := strings.TrimSpace(errOut.String())
		if stderr != "" {
			return nil, fmt.Errorf("%s: %w: %s", bin, err, stderr)
		}
		return nil, fmt.Errorf("%s: %w", bin, err)
	}
	b := out.Bytes()
	if len(b) > maxCloudOutputBytes {
		b = b[:maxCloudOutputBytes]
	}
	return b, nil
}
