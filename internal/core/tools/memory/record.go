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
	"github.com/rlaope/cloudy/internal/permission"
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
			fact := strings.TrimSpace(a.Fact)
			if fact == "" {
				return tools.Observation{Text: "Nothing recorded: fact was empty."}, nil
			}
			// Redact before persisting. memory.md is injected verbatim into the
			// system prompt of every future session, and the system prompt never
			// passes the MaskingHook (which only runs on tool observations). So a
			// secret recorded here would otherwise leak both to disk in clear text
			// and into every future prompt. Masking at the write keeps the on-disk
			// store from ever being less redacted than the model-facing path —
			// the persisted-state invariant the v0.5 audit established.
			profile, _ := permission.LoadActive()
			fact = permission.MaskerOrDefault(profile).MaskString(fact)
			if err := memstore.Append(fact); err != nil {
				return tools.Observation{}, fmt.Errorf("memory.record: %w", err)
			}
			return tools.Observation{Text: "Recorded to cross-session memory: " + fact}, nil
		},
	}.Build()
}
