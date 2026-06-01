// Package memory provides the memory.record tool — cloudy's single write
// surface. It does NOT touch any monitored infrastructure: it appends one
// durable fact to cloudy's local memory file (see internal/memory) so the agent
// remembers it across sessions. The recorded memory is injected back into the
// system prompt at the start of every future run.
package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/rlaope/cloudy/internal/core/tools"
	memstore "github.com/rlaope/cloudy/internal/memory"
)

// newRecordTool builds memory.record.
func newRecordTool() tools.Tool {
	type args struct {
		Fact string `json:"fact"`
	}
	schema := tools.MustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"fact": map[string]any{
				"type": "string",
				"description": "One concise, durable fact about THIS operator's environment worth " +
					"remembering across sessions — e.g. a context→environment mapping " +
					"(\"ctx prod-east is production\"), a naming convention, a normal baseline, or " +
					"a confirmed past-incident root cause. NOT transient readings (current pod " +
					"count, a one-off metric value).",
			},
		},
		"required": []string{"fact"},
	})
	return tools.Spec[args]{
		Name: "memory.record",
		Description: "Save one durable fact about the operator's environment to cloudy's local " +
			"cross-session memory (memory.md). This is the ONLY tool that writes anything, and it " +
			"touches no monitored infrastructure — only cloudy's own memory file, which is injected " +
			"into your context at the start of every future session. Use it when you learn a STABLE " +
			"fact (topology, naming conventions, baselines, a confirmed root cause) so you need not " +
			"rediscover it later. Do not record transient observations.",
		Schema: schema,
		Run: func(_ context.Context, a args) (tools.Observation, error) {
			if strings.TrimSpace(a.Fact) == "" {
				return tools.Observation{Text: "Nothing recorded: fact was empty."}, nil
			}
			if err := memstore.Append(a.Fact); err != nil {
				return tools.Observation{}, fmt.Errorf("memory.record: %w", err)
			}
			return tools.Observation{Text: fmt.Sprintf("Recorded to cross-session memory: %s", strings.TrimSpace(a.Fact))}, nil
		},
	}.Build()
}
