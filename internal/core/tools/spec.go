package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// Spec is a generic tool descriptor that removes the boilerplate of writing
// Name / Description / Schema methods plus json.Unmarshal calls. The framework
// builds a Tool that:
//
//   - returns Name and Description verbatim
//   - serves Schema as the JSON Schema
//   - unmarshals the incoming json.RawMessage into a fresh Args before
//     invoking Run
//
// Use Build() to obtain the resulting Tool. Spec is most useful for new
// tools; existing imperative implementations remain valid.
type Spec[Args any] struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Run         func(ctx context.Context, args Args) (Observation, error)
}

// Build returns the Tool implementation backed by s. It panics on missing
// required fields so the mistake surfaces at registration time.
func (s Spec[Args]) Build() Tool {
	if s.Name == "" {
		panic("tools: Spec.Name is required")
	}
	if s.Run == nil {
		panic("tools: Spec.Run is required for " + s.Name)
	}
	return &specTool[Args]{spec: s}
}

type specTool[Args any] struct {
	spec Spec[Args]
}

func (t *specTool[Args]) Name() string            { return t.spec.Name }
func (t *specTool[Args]) Description() string     { return t.spec.Description }
func (t *specTool[Args]) Schema() json.RawMessage { return t.spec.Schema }

func (t *specTool[Args]) Run(ctx context.Context, raw json.RawMessage) (Observation, error) {
	var a Args
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return Observation{}, fmt.Errorf("%s: parse args: %w", t.spec.Name, err)
		}
	}
	return t.spec.Run(ctx, a)
}
