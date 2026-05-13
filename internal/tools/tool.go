// Package tools defines the Tool interface and Observation type used by the
// cloudy ReAct agent.
//
// Read-only enforcement: cloudy enforces read-only at the *transport* layer —
// internal/transport/readonly.go rejects any HTTP method outside GET/HEAD/
// OPTIONS, and internal/transport/k8s.go rejects any kube verb outside
// get/list/watch. The Tool interface intentionally does not carry a
// ReadOnly() method: it would be type-redundant defense behind two hard
// guards already in place. New tools cannot bypass the transport guards.
package tools

import (
	"context"
	"encoding/json"

	"github.com/rlaope/cloudy/internal/render"
)

// Observation is the structured result returned by a Tool after execution.
type Observation struct {
	// Text is the primary human-readable output of the tool.
	Text string
	// Table is an optional tabular representation of the result.
	Table *render.Table
	// Raw holds the unstructured original data (e.g. a decoded JSON object)
	// for downstream processing.
	Raw any
}

// Tool is the interface every registered tool must implement.
//
// Name conventions: dot-separated segments, e.g. "k8s.list_pods".
type Tool interface {
	// Name returns the dot-separated tool identifier, e.g. "k8s.list_pods".
	Name() string

	// Description returns a concise explanation of what the tool does.
	// This text is included in the system prompt shown to the LLM.
	Description() string

	// Schema returns the JSON Schema (as a raw JSON message) for the tool's
	// arguments object. The LLM uses this schema to construct valid calls.
	Schema() json.RawMessage

	// Run executes the tool with the given JSON-encoded argument object and
	// returns an Observation or an error. ctx carries a deadline/cancellation
	// from the agent loop.
	Run(ctx context.Context, args json.RawMessage) (Observation, error)
}
