// Package agent implements the cloudy ReAct (Reason+Act) loop.
//
// The agent drives a conversation with an LLM, dispatches tool calls to a
// Registry, emits incremental output to a render.Sink, and terminates when the model
// produces a final text-only response or a safety limit is hit.
//
// Cross-cutting policies (duplicate-call detection, cost guard, audit log,
// masking) are expressed as Hooks (see hook.go) rather than baked into the
// loop body, so adding new policy does not require changing Run.
package agent

import (
	"context"
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
	// Hooks is the cross-cutting policy chain. When nil, a fresh
	// DupCallHook is registered. Pass an explicit empty slice to opt out.
	Hooks []Hook
}

// Agent executes the ReAct loop for a single user query. An Agent is safe
// for sequential reuse but MUST NOT be used concurrently.
type Agent struct {
	opts     Options
	registry *tools.Registry // possibly skill-filtered
	hooks    []Hook
}

// New constructs an Agent from opts, applying defaults and validating
// required fields.
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

	hooks := opts.Hooks
	if hooks == nil {
		hooks = []Hook{NewDupCallHook()}
	}

	return &Agent{opts: opts, registry: reg, hooks: hooks}, nil
}

// Run executes the ReAct loop for userInput, streaming tokens and tool-call
// blocks to sink. It returns the updated conversation history (including
// the new user turn and all assistant/tool turns) or a typed error.
func (a *Agent) Run(ctx context.Context, userInput string, sink render.Sink) ([]llm.Message, error) {
	sysPrompt := a.buildSystemPrompt()

	msgs := make([]llm.Message, 0, len(a.opts.History)+2)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: sysPrompt})
	msgs = append(msgs, a.opts.History...)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: userInput})

	llmTools := a.registry.ToolsFor(a.opts.Provider.Name())

	var finalErr error
	defer func() { a.fireOnStop(ctx, finalErr) }()

	for step := 0; step < a.opts.MaxSteps; step++ {
		assistant, err := a.streamAssistantTurn(ctx, msgs, llmTools, sink)
		if err != nil {
			finalErr = err
			return msgs, err
		}
		msgs = append(msgs, assistant)
		a.fireOnAssistantTurn(ctx, assistant)

		// No tool calls → final response.
		if len(assistant.ToolCalls) == 0 {
			return msgs, nil
		}

		for _, tc := range assistant.ToolCalls {
			toolMsg, err := a.dispatchTool(ctx, tc, sink)
			if err != nil {
				finalErr = err
				msgs = append(msgs, toolMsg)
				return msgs, err
			}
			msgs = append(msgs, toolMsg)
		}
	}
	finalErr = ErrMaxSteps
	return msgs, ErrMaxSteps
}

// streamAssistantTurn drains a single LLM streaming response, accumulating
// text and tool calls.
func (a *Agent) streamAssistantTurn(ctx context.Context, msgs []llm.Message, llmTools []llm.Tool, sink render.Sink) (llm.Message, error) {
	req := llm.Request{
		Model:    a.opts.Model,
		Messages: msgs,
		Tools:    llmTools,
		Stream:   true,
	}
	ch, err := a.opts.Provider.Stream(ctx, req)
	if err != nil {
		return llm.Message{}, fmt.Errorf("agent: provider stream error: %w", err)
	}

	var textBuf strings.Builder
	var toolCalls []llm.ToolCall
	var currentTC *llm.ToolCall

	for chunk := range ch {
		if chunk.Err != nil {
			return llm.Message{}, fmt.Errorf("agent: stream chunk error: %w", chunk.Err)
		}
		if chunk.Done {
			break
		}
		if chunk.DeltaText != "" {
			textBuf.WriteString(chunk.DeltaText)
			if sink != nil {
				sink.WriteToken(chunk.DeltaText)
			}
		}
		if chunk.Usage != nil && sink != nil {
			sink.RecordUsage(*chunk.Usage)
		}
		if chunk.ToolCall != nil {
			tc := chunk.ToolCall
			if currentTC == nil || currentTC.ID != tc.ID {
				if currentTC != nil {
					toolCalls = append(toolCalls, *currentTC)
				}
				currentTC = &llm.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
			} else {
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

	return llm.Message{
		Role:      llm.RoleAssistant,
		Content:   textBuf.String(),
		ToolCalls: toolCalls,
	}, nil
}

// dispatchTool runs one tool call through the hook chain and returns the
// tool-result message destined for the LLM.
func (a *Agent) dispatchTool(ctx context.Context, tc llm.ToolCall, sink render.Sink) (llm.Message, error) {
	for _, h := range a.hooks {
		if err := h.BeforeToolCall(ctx, tc); err != nil {
			return llm.Message{
				Role:       llm.RoleTool,
				Content:    fmt.Sprintf("error: %v", err),
				ToolCallID: tc.ID,
			}, err
		}
	}

	obs, runErr := a.runTool(ctx, tc, sink)

	for _, h := range a.hooks {
		var hookErr error
		obs, hookErr = h.AfterToolCall(ctx, tc, obs, runErr)
		if hookErr != nil {
			return llm.Message{
				Role:       llm.RoleTool,
				Content:    fmt.Sprintf("error: %v", hookErr),
				ToolCallID: tc.ID,
			}, hookErr
		}
	}

	return llm.Message{
		Role:       llm.RoleTool,
		Content:    a.formatObservation(obs, runErr),
		ToolCallID: tc.ID,
	}, nil
}

// runTool looks up and executes a single tool call, emitting to sink. If
// the tool is unknown it returns a descriptive error observation rather
// than aborting.
func (a *Agent) runTool(ctx context.Context, tc llm.ToolCall, sink render.Sink) (tools.Observation, error) {
	if sink != nil {
		sink.BeginToolCall(tc.Name, string(tc.Arguments))
	}
	tool, ok := a.registry.Get(tc.Name)
	if !ok {
		err := fmt.Errorf("tool %q is not available", tc.Name)
		if sink != nil {
			sink.EndToolCall("", err)
		}
		return tools.Observation{Text: err.Error()}, err
	}
	obs, err := tool.Run(ctx, tc.Arguments)
	if sink != nil {
		if err != nil {
			sink.EndToolCall("", err)
		} else {
			sink.EndToolCall(obs.Text, nil)
		}
	}
	return obs, err
}

// formatObservation converts an Observation and optional error into the
// text fed back to the LLM as the tool result.
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

// buildSystemPrompt assembles base preamble + skill prompt + tool catalogue.
func (a *Agent) buildSystemPrompt() string {
	var sb strings.Builder
	sb.WriteString(basePreamble)
	if a.opts.Skill != nil && a.opts.Skill.SystemPrompt != "" {
		sb.WriteString("\n\n")
		sb.WriteString(a.opts.Skill.SystemPrompt)
	}
	tools := a.registry.List()
	if len(tools) > 0 {
		sb.WriteString("\n\n## Available Tools\n")
		for _, t := range tools {
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", t.Name(), t.Description()))
		}
	}
	return sb.String()
}

func (a *Agent) fireOnAssistantTurn(ctx context.Context, msg llm.Message) {
	for _, h := range a.hooks {
		h.OnAssistantTurn(ctx, msg)
	}
}

func (a *Agent) fireOnStop(ctx context.Context, finalErr error) {
	for _, h := range a.hooks {
		h.OnStop(ctx, finalErr)
	}
}
