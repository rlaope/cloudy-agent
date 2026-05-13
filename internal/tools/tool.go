// Package tools defines the Tool interface and Observation type used by the
// cloudy ReAct agent. All tools registered with a Registry MUST be read-only;
// any attempt to register a mutating tool causes a panic at load time.
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
// All tools registered with a Registry MUST return true from ReadOnly();
// a false return causes Register to panic immediately.
type Tool interface {
	// Name returns the dot-separated tool identifier, e.g. "k8s.list_pods".
	Name() string

	// Description returns a concise explanation of what the tool does.
	// This text is included in the system prompt shown to the LLM.
	Description() string

	// Schema returns the JSON Schema (as a raw JSON message) for the tool's
	// arguments object. The LLM uses this schema to construct valid calls.
	Schema() json.RawMessage

	// ReadOnly must always return true for tools intended for read-only
	// operation. Register panics if this returns false.
	ReadOnly() bool

	// Run executes the tool with the given JSON-encoded argument object and
	// returns an Observation or an error. ctx carries a deadline/cancellation
	// from the agent loop.
	Run(ctx context.Context, args json.RawMessage) (Observation, error)
}
