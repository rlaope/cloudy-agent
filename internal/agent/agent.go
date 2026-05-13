// Package agent implements the cloudy ReAct (Reason+Act) loop.
//
// The agent drives a conversation with an LLM, dispatching tool calls to a
// Registry, streaming output to a render.Stream, and terminating when the
// model produces a final text-only response or a safety limit is hit.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rlaope/cloudy/internal/llm"
	"github.com/rlaope/cloudy/internal/render"
	"github.com/rlaope/cloudy/internal/skills"
	"github.com/rlaope/cloudy/internal/tools"
)

const (
	defaultMaxSteps      = 12
	defaultMaxToolTokens = 8000

	basePreamble = "You are cloudy, a read-only multi-cluster SRE monitoring agent. " +
		"Use the registered tools; never invent tools or arguments. " +
		"Cite specific resource names in your final answer."
)

// ErrMaxSteps is returned when the agent exhausts its step budget without
// reaching a final (tool-call-free) response.
var ErrMaxSteps = errors.New("agent: maximum steps reached without final response")

// ErrDuplicateCall is returned when the same (tool, args) pair is invoked
// three times in a row, indicating the model is stuck in a loop.
var ErrDuplicateCall = errors.New("agent: duplicate tool call detected (model stuck in loop)")

// Options configures an Agent.
type Options struct {
	// Provider is the LLM backend to use. Required.
	Provider llm.Provider
	// Model is the fully-qualified model identifier. Required.
	Model string
	// Registry holds all available tools. Required.
	Registry *tools.Registry
	// Skill, if non-nil, prepends its SystemPrompt and filters the Registry
	// through skill.AllowedTools before each run.
	Skill *skills.Skill
	// MaxSteps caps the total number of LLM → tool → LLM round-trips.
	// Zero is replaced by the default (12).
	MaxSteps int
	// MaxToolTokens caps the character length of any single tool observation
	// fed back to the LLM. Zero is replaced by the default (8000).
	MaxToolTokens int
	// History is the prior conversation context prepended to each run.
	History []llm.Message
}

// Agent executes the ReAct loop for a single user query.
// An Agent is safe for sequential reuse but MUST NOT be used concurrently.
type Agent struct {
	opts     Options
	registry *tools.Registry // possibly skill-filtered
}

// New constructs an Agent from opts, applying defaults and validating required
// fields. It returns an error if Provider, Model, or Registry is nil/empty.
func New(opts Options) (*Agent, error) {
	if opts.Provider == nil {
		return nil, errors.New("agent: Provider is required")
	}
	if opts.Model == "" {
		return nil, errors.New("agent: Model is required")
	}
	if opts.Registry == nil {
		return nil, errors.New("agent: Registry is required")
	}
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = defaultMaxSteps
	}
	if opts.MaxToolTokens <= 0 {
		opts.MaxToolTokens = defaultMaxToolTokens
	}

	reg := opts.Registry
	if opts.Skill != nil && len(opts.Skill.AllowedTools) > 0 {
		reg = opts.Registry.Filter(opts.Skill.AllowedTools)
	}

	return &Agent{opts: opts, registry: reg}, nil
}

// Run executes the ReAct loop for userInput, streaming tokens and tool-call
// blocks to stream. It returns the updated conversation history (including the
// new user turn and all assistant/tool turns) or a typed error.
func (a *Agent) Run(ctx context.Context, userInput string, stream *render.Stream) ([]llm.Message, error) {
	sysPrompt := a.buildSystemPrompt()

	// Build initial message list: system + history + user.
	msgs := make([]llm.Message, 0, len(a.opts.History)+2)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: sysPrompt})
	msgs = append(msgs, a.opts.History...)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: userInput})

	llmTools := a.registry.ToolsFor(a.opts.Provider.Name())

	// Duplicate-call tracking: hash → consecutive count.
	var lastCallHash string
	var consecutiveDupes int

	for step := 0; step < a.opts.MaxSteps; step++ {
		req := llm.Request{
			Model:    a.opts.Model,
			Messages: msgs,
			Tools:    llmTools,
			Stream:   true,
		}

		ch, err := a.opts.Provider.Stream(ctx, req)
		if err != nil {
			return msgs, fmt.Errorf("agent: provider stream error: %w", err)
		}

		// Accumulate the assistant turn.
		var textBuf strings.Builder
		var toolCalls []llm.ToolCall
		var currentTC *llm.ToolCall

		for chunk := range ch {
			if chunk.Err != nil {
				return msgs, fmt.Errorf("agent: stream chunk error: %w", chunk.Err)
			}
			if chunk.Done {
				break
			}

			if chunk.DeltaText != "" {
				textBuf.WriteString(chunk.DeltaText)
				if stream != nil {
					stream.WriteToken(chunk.DeltaText)
				}
			}

			if chunk.Usage != nil && stream != nil {
				stream.RecordUsage(*chunk.Usage)
			}

			if chunk.ToolCall != nil {
				tc := chunk.ToolCall
				// Providers may stream tool call deltas incrementally.
				// We treat each non-empty ToolCall chunk as a complete call
				// (adapters are expected to assemble deltas before emitting).
				if currentTC == nil || currentTC.ID != tc.ID {
					if currentTC != nil {
						toolCalls = append(toolCalls, *currentTC)
					}
					currentTC = &llm.ToolCall{
						ID:        tc.ID,
						Name:      tc.Name,
						Arguments: tc.Arguments,
					}
				} else {
					// Accumulate argument fragments.
					if len(tc.Arguments) > 0 {
						currentTC.Arguments = append(currentTC.Arguments, tc.Arguments...)
					}
					if tc.Name != "" {
						currentTC.Name = tc.Name
					}
				}
			}
		}
		if currentTC != nil {
			toolCalls = append(toolCalls, *currentTC)
		}

		// Record assistant message.
		assistantMsg := llm.Message{
			Role:      llm.RoleAssistant,
			Content:   textBuf.String(),
			ToolCalls: toolCalls,
		}
		msgs = append(msgs, assistantMsg)

		// No tool calls → final response.
		if len(toolCalls) == 0 {
			return msgs, nil
		}

		// Execute each requested tool call.
		for _, tc := range toolCalls {
			callHash := hashCall(tc.Name, tc.Arguments)

			// Duplicate-call detection.
			if callHash == lastCallHash {
				consecutiveDupes++
				if consecutiveDupes >= 3 {
					return msgs, ErrDuplicateCall
				}
			} else {
				lastCallHash = callHash
				consecutiveDupes = 1
			}

			obs, runErr := a.runTool(ctx, tc, stream)

			// Build the tool result message.
			resultContent := a.formatObservation(obs, runErr)
			toolMsg := llm.Message{
				Role:       llm.RoleTool,
				Content:    resultContent,
				ToolCallID: tc.ID,
			}
			msgs = append(msgs, toolMsg)
		}
	}

	return msgs, ErrMaxSteps
}

// runTool looks up and executes a single tool call, emitting to stream.
// If the tool is unknown or not allowed it returns a descriptive error
// observation without aborting the loop.
func (a *Agent) runTool(ctx context.Context, tc llm.ToolCall, stream *render.Stream) (tools.Observation, error) {
	if stream != nil {
		stream.BeginToolCall(tc.Name, string(tc.Arguments))
	}

	tool, ok := a.registry.Get(tc.Name)
	if !ok {
		err := fmt.Errorf("tool %q is not available", tc.Name)
		if stream != nil {
			stream.EndToolCall("", err)
		}
		return tools.Observation{Text: err.Error()}, err
	}

	obs, err := tool.Run(ctx, tc.Arguments)

	if stream != nil {
		if err != nil {
			stream.EndToolCall("", err)
		} else {
			stream.EndToolCall(obs.Text, nil)
		}
	}
	return obs, err
}

// formatObservation converts an Observation and optional error into the text
// that will be fed back to the LLM as the tool result content.
func (a *Agent) formatObservation(obs tools.Observation, err error) string {
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	text := obs.Text
	if len(text) > a.opts.MaxToolTokens {
		text = text[:a.opts.MaxToolTokens] + " …(truncated)"
	}
	return text
}

// buildSystemPrompt assembles the full system prompt: base preamble + optional
// skill prompt + compact tool catalogue.
func (a *Agent) buildSystemPrompt() string {
	var sb strings.Builder
	sb.WriteString(basePreamble)

	if a.opts.Skill != nil && a.opts.Skill.SystemPrompt != "" {
		sb.WriteString("\n\n")
		sb.WriteString(a.opts.Skill.SystemPrompt)
	}

	// Append a compact tool catalogue (name + description only, no inline schemas).
	tools := a.registry.List()
	if len(tools) > 0 {
		sb.WriteString("\n\n## Available Tools\n")
		for _, t := range tools {
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", t.Name(), t.Description()))
		}
	}

	return sb.String()
}

// hashCall returns a short fingerprint of (name, args) for duplicate detection.
func hashCall(name string, args json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(name))
	h.Write(args)
	return fmt.Sprintf("%x", h.Sum(nil))
}
