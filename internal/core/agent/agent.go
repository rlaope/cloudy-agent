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
	"time"

	"github.com/rlaope/cloudy/internal/core/llm"
	"github.com/rlaope/cloudy/internal/core/skills"
	"github.com/rlaope/cloudy/internal/core/tools"
	"github.com/rlaope/cloudy/internal/permission"
	"github.com/rlaope/cloudy/internal/render"
)

const (
	defaultMaxSteps      = 12
	defaultMaxToolTokens = 8000

	// basePreamble is prepended to every system prompt. It teaches the
	// LLM both how to act (tool-use rules) and what it is (cloudy's
	// surface area), so meta-questions like "what is /setup?" or "what
	// skills do you have?" can be answered from in-band context rather
	// than triggering "I don't know that term" hallucinations.
	basePreamble = "" +
		// --- Identity ---
		"You are cloudy, a read-only multi-cluster SRE monitoring CLI agent " +
		"written in Go (github.com/rlaope/cloudy). The user is talking to you " +
		"through cloudy's terminal UI (a bubbletea TUI). Every tool that touches " +
		"the infrastructure you monitor is read-only by construction — clusters, " +
		"databases, logs, and traces are never mutated. The single exception is " +
		"your own memory: `memory.record` writes a durable fact to cloudy's local " +
		"memory file so you remember it across sessions; it touches no monitored " +
		"infrastructure.\n\n" +
		// --- Slash commands the operator can type ---
		"## cloudy slash commands\n" +
		"The operator may reference these by name; answer questions about " +
		"them from this list rather than saying you do not know.\n" +
		"- `/setup`   — interactive discovery wizard: scans every kubeconfig " +
		"context, auto-detects Prometheus / Loki / Elasticsearch / Tempo / " +
		"Jaeger / Postgres / MySQL / Redis endpoints, lets the operator pick " +
		"which to enable, then writes `~/.cloudy/config.yaml` plus a " +
		"`profile.yaml` scan snapshot. Hot-swaps the tool registry — no restart.\n" +
		"- `/login`   — pick an LLM provider (Anthropic / OpenAI / Google / " +
		"Moonshot / OpenAI-compatible), paste an API key, choose a model. " +
		"Saves to `~/.cloudy/secrets` (mode 0600).\n" +
		"- `/model`   — swap the active LLM model mid-session.\n" +
		"- `/skill`   — switch the active skill playbook (filters tools to " +
		"the skill's whitelist and prepends the skill's system prompt).\n" +
		"- `/scope`   — restrict the agent to a namespace or context " +
		"(`/scope ns=payments` or `/scope ctx=prod-east`); `/scope reset` clears.\n" +
		"- `/use`     — switch the active kubeconfig context (e.g. `/use prod-east`).\n" +
		"- `/tools`   — list the tool groups currently wired plus the reason " +
		"any skipped group was skipped.\n" +
		"- `/clear`   — wipe the visible screen only; the conversation history " +
		"is kept (Ctrl+L is the shortcut). Use `/new` to also reset memory.\n" +
		"- `/compact` — summarize the older turns into one note and drop them, " +
		"freeing context window while keeping the recent turns verbatim. Manual; " +
		"the footer shows a `ctx N%` gauge and warns past 75%.\n" +
		"- `/autocompact` — toggle automatic compaction: when on, cloudy runs " +
		"/compact for you once a finished turn leaves context usage past 90%. " +
		"Off by default.\n" +
		"- `/new`     — reset the conversation history and start a fresh session log.\n" +
		"- `/plan`    — toggle plan-first investigation: the agent opens a " +
		"multi-step question with a brief hypothesis plan (symptom → candidate " +
		"causes → the probe for each) before calling tools. On by default.\n" +
		"- `/resume <id>` — reload a past conversation (by session id) back into context.\n" +
		"- `/replay <id>` — replay a previous session log.\n" +
		"- `/update`  — upgrade the cloudy binary in place from the latest " +
		"GitHub release (`cloudy update` is the equivalent CLI subcommand).\n" +
		"- `/help`, `/version`, `/exit` / `/quit` — self-explanatory.\n\n" +
		// --- Skills concept ---
		"## Skills\n" +
		"A skill is a curated SRE playbook (YAML frontmatter + markdown " +
		"system-prompt body) that filters the available tools to a whitelist " +
		"and primes the agent with domain-specific reasoning. Built-in skills " +
		"live embedded in the binary; user skills live in `~/.cloudy/skills/` " +
		"(user wins on name conflicts). The full skill list appears below " +
		"under \"## Available skills\" when a skill registry is provided.\n\n" +
		// --- Cross-session memory ---
		"## Cross-session memory\n" +
		"You have a durable memory that survives across sessions. Facts you " +
		"already recorded appear below under \"## Environment memory\" when " +
		"present — treat them as trusted background. When you learn a STABLE " +
		"fact about this environment (a context→environment mapping, a naming " +
		"convention, a normal baseline, a confirmed root cause), call " +
		"`memory.record` so future sessions start already knowing it. Never " +
		"record transient readings (a current pod count, a one-off metric).\n\n" +
		// --- State / config layout ---
		"## State layout\n" +
		"cloudy resolves its state directory in this order: `$CLOUDY_HOME` → " +
		"`$XDG_CONFIG_HOME/cloudy` → `$HOME/.cloudy`. Files there: " +
		"`config.yaml`, `profile.yaml`, `secrets`, `profiles/<name>.yaml`, " +
		"`active_profile`, `memory.md`, `skills/*.md`, `logs/*.jsonl`.\n\n" +
		// --- Behaviour contract ---
		"## Tool-use rules\n" +
		"1. Use the registered tools; never invent tools or arguments.\n" +
		"2. Cite specific resource names (namespace, pod, service, …) in " +
		"your final answer — do not generalise away from the data the tools " +
		"returned.\n" +
		"3. If a tool call fails with an approval-denied or " +
		"read-only-violation error, do not retry the same tool — pick a " +
		"lower-risk alternative (list / get / show / inspect / query) and " +
		"proceed.\n" +
		"4. If asked about cloudy itself (slash commands, skills, config " +
		"layout, install instructions), answer from this preamble rather " +
		"than saying you don't know — that information IS your context."

	// planDirective is appended to the system prompt when Options.Plan is set.
	// It makes the agent open multi-step investigations with an explicit
	// hypothesis plan before probing — the structured-investigation behaviour
	// that distinguishes cloudy from an improvised tool-by-tool ReAct loop.
	// The plan is the leading text of the first assistant turn, so it costs no
	// extra round-trip and the model executes against its own stated plan.
	planDirective = "" +
		"\n\n## Investigation planning\n" +
		"For any request that needs more than a single tool call, BEGIN your " +
		"first response with a short plan, then execute it:\n" +
		"- **Symptom** — restate the question/symptom in one line.\n" +
		"- **Hypotheses** — 2–4 likely causes, most probable first.\n" +
		"- **Probes** — for each hypothesis, the specific read-only tool(s) " +
		"that confirm or rule it out.\n" +
		"Keep the plan under 120 words, then carry it out, revising as evidence " +
		"arrives. For a trivial, single-tool, or meta question (e.g. about " +
		"cloudy itself), skip the plan and answer directly."
)

// BasePreamble exposes the compiled self-knowledge preamble. The TUI
// package uses it to drift-guard its slash-command palette against the
// command list documented here: the two are hand-maintained in separate
// packages (palette.builtinItems vs this string), and a command offered by
// the palette but absent here makes the agent claim it does not know a
// command cloudy actually supports.
func BasePreamble() string { return basePreamble }

// ErrMaxSteps is returned when the agent exhausts its step budget without
// reaching a final (tool-call-free) response.
var ErrMaxSteps = errors.New("agent: maximum steps reached without final response")

// ErrConversationTimeout is returned when the agent's wall-clock budget for
// a single Run is exceeded. Distinct from caller-side ctx cancellation so the
// TUI / CLI can surface it with a meaningful explanation instead of the
// generic "context deadline exceeded".
var ErrConversationTimeout = errors.New("agent: conversation wall-clock deadline exceeded")

// Options configures an Agent.
type Options struct {
	// Provider is the LLM backend to use. Required.
	Provider llm.Provider
	// Model is the fully-qualified model identifier. Required.
	Model string
	// Registry holds all available tools. Mutually exclusive with RegistryFn;
	// exactly one must be set.
	Registry *tools.Registry
	// RegistryFn, when non-nil, is called at the start of every Run to fetch
	// the current registry. This lets long-lived agents pick up a registry
	// hot-swapped by /setup. When nil, Options.Registry is used for the entire
	// lifetime of the agent (legacy behaviour).
	RegistryFn func() *tools.Registry
	// Skill, if non-nil, is consulted at the top of every Run via
	// SkillProvider.Resolve: its returned prompt is prepended to the system
	// prompt, and its returned tool whitelist filters the Registry for this
	// run. The concrete *skills.Skill type is wrapped in skills.NewStaticSkill
	// at every call site; future implementations (RAG-backed, runbook-backed)
	// plug in behind the same interface. See docs/RFC-RAG.md §4.
	Skill skills.SkillProvider
	// Skills, if non-nil, is rendered into the system preamble as a
	// catalog of available skill playbooks the agent can suggest to the
	// operator (via "/skill <name>"). Distinct from Skill — Skill is the
	// currently active playbook; Skills is the directory the LLM can
	// browse when answering "what skills do you have?" questions.
	Skills *skills.Registry
	// MaxSteps caps the total number of LLM → tool → LLM round-trips.
	// Zero is replaced by the default (12).
	MaxSteps int
	// MaxToolTokens caps the character length of any single tool observation
	// fed back to the LLM. Zero is replaced by the default (8000).
	MaxToolTokens int
	// MaxTokensPerSession is the cumulative input+output token cap enforced
	// by a default CostGuardHook. Zero disables the check.
	MaxTokensPerSession int
	// MaxUSDPerDay is the rolling-day USD cap enforced by the default
	// CostGuardHook. Zero disables the check.
	MaxUSDPerDay float64
	// MaxConversationSeconds caps the total wall-clock time a single Run may
	// consume. Distinct from MaxSteps because each step can take tens of
	// seconds (e.g. async_profile waits 60s). Zero disables the check.
	MaxConversationSeconds int
	// MaxLogLinesPerCall is the per-tool-call cap on the "limit" argument of
	// log.* tools, enforced by the default LimitGuardHook. Zero disables it.
	MaxLogLinesPerCall int
	// MaxProfileSecondsPerCall is the per-tool-call cap on duration_seconds
	// for profiling tools (jvm.async_profile, perf.*, ebpf.*, py.spy_*).
	// Zero disables it.
	MaxProfileSecondsPerCall int
	// MaxLogResponseBytes is the byte ceiling at which a log.* tool result
	// is rewritten as a head/tail + exception-context summary before being
	// fed to the LLM. Zero disables the summary step — the model still sees
	// the full text up to MaxToolTokens.
	MaxLogResponseBytes int
	// Approver, when non-nil, is consulted by an ApprovalHook before every
	// dispatch of a RiskHigh tool. nil leaves the door open: appropriate for
	// internal tests, never for production entry points. cmd/main.go injects
	// a TUI-backed approver; cli/ask.go injects DenyApprover.
	Approver Approver
	// Profile, when non-nil, drives the default MaskingHook — its
	// Masking.KeyRegex / Masking.ValueRegex patterns are compiled once and
	// applied to every tool observation before the LLM sees it. nil = no
	// masking (the historical behaviour). Closing the M-1 gap from the v0.5
	// security review which found permission.Masker had no production
	// call sites despite being published, tested, and documented.
	Profile *permission.Profile
	// History is the prior conversation context prepended to each run.
	History []llm.Message
	// EnvironmentMemory is cloudy's durable cross-session memory (see
	// internal/memory), injected verbatim into the system prompt under an
	// "## Environment memory" heading at the top of each run so the agent
	// starts already knowing facts it recorded in earlier sessions. Empty =
	// nothing injected (fresh install, or the caller did not load memory).
	EnvironmentMemory string
	// Plan, when true, appends an investigation-planning directive to the
	// system prompt: the agent opens any multi-step request with a brief
	// hypothesis plan (symptom → candidate causes → the read-only probe for
	// each) before calling tools, then executes against it. The plan is the
	// leading text of the first assistant turn — no extra round-trip — so it
	// streams to the operator and seeds the model's own execution. Trivial,
	// single-tool, or meta questions skip the plan. Off by default; the TUI
	// turns it on and exposes a /plan toggle.
	Plan bool
	// Hooks is the cross-cutting policy chain. When nil, defaults include
	// DupCallHook plus (when MaxTokensPerSession or MaxUSDPerDay is set) a
	// CostGuardHook. Pass an explicit empty slice to opt out entirely.
	Hooks []Hook
}

// Agent executes the ReAct loop for a single user query. An Agent is safe
// for sequential reuse but MUST NOT be used concurrently.
type Agent struct {
	opts  Options
	hooks []Hook
}

// resolvedSkill holds the per-Run result of SkillProvider.Resolve. The fields
// are stable for the whole Run (we resolve once at the top) so we can pass
// them around without re-invoking the provider per step.
type resolvedSkill struct {
	name    string
	prompt  string
	allowed []string
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
	if opts.Registry == nil && opts.RegistryFn == nil {
		return nil, errors.New("agent: Registry or RegistryFn is required")
	}
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = defaultMaxSteps
	}
	if opts.MaxToolTokens <= 0 {
		opts.MaxToolTokens = defaultMaxToolTokens
	}

	hooks := opts.Hooks
	if hooks == nil {
		hooks = []Hook{NewDupCallHook()}
		if opts.MaxTokensPerSession > 0 || opts.MaxUSDPerDay > 0 {
			hooks = append(hooks, NewCostGuardHook(opts.MaxTokensPerSession, opts.MaxUSDPerDay))
		}
		if opts.MaxLogLinesPerCall > 0 || opts.MaxProfileSecondsPerCall > 0 {
			hooks = append(hooks, NewLimitGuardHook(opts.MaxLogLinesPerCall, opts.MaxProfileSecondsPerCall))
		}
		if opts.MaxLogResponseBytes > 0 {
			hooks = append(hooks, NewLogSummaryHook(opts.MaxLogResponseBytes))
		}
		// MaskingHook sits after LogSummaryHook on purpose: LogSummary
		// clips the observation first (head/tail + exception extract),
		// then masking redacts whatever survived the clip. Running them
		// in the reverse order would waste regex work on the bytes the
		// summary was about to drop.
		if opts.Profile != nil {
			masker, err := permission.NewMasker(opts.Profile)
			if err != nil {
				return nil, fmt.Errorf("agent: compile masker: %w", err)
			}
			// NewMaskingHook handles a nil masker gracefully — no
			// profile-configured patterns is the common case and stays
			// a no-op without a branch here.
			hooks = append(hooks, NewMaskingHook(masker))
		}
		if opts.Approver != nil {
			var regGetter func() *tools.Registry
			if opts.RegistryFn != nil {
				regGetter = opts.RegistryFn
			} else {
				reg := opts.Registry
				regGetter = func() *tools.Registry { return reg }
			}
			hooks = append(hooks, NewApprovalHook(regGetter, opts.Approver))
		}
	}

	return &Agent{opts: opts, hooks: hooks}, nil
}

// resolveSkill invokes the SkillProvider once for this Run. When Options.Skill
// is nil (the historical "no active skill" path) it returns a zero-value
// resolvedSkill, which downstream code (resolveRegistry, buildSystemPrompt)
// treats as "no prompt, no filter".
func (a *Agent) resolveSkill(ctx context.Context) (resolvedSkill, error) {
	if a.opts.Skill == nil {
		return resolvedSkill{}, nil
	}
	prompt, allowed, err := a.opts.Skill.Resolve(ctx)
	if err != nil {
		return resolvedSkill{}, fmt.Errorf("agent: resolve skill: %w", err)
	}
	return resolvedSkill{
		name:    a.opts.Skill.Name(),
		prompt:  prompt,
		allowed: allowed,
	}, nil
}

// resolveRegistry returns the current Registry for this Run, applying the
// skill filter when the resolved skill carries an AllowedTools whitelist.
// When RegistryFn is set it is called on every Run so hot-swapped registries
// are picked up automatically; otherwise the static Registry is used.
func (a *Agent) resolveRegistry(skill resolvedSkill) *tools.Registry {
	var reg *tools.Registry
	if a.opts.RegistryFn != nil {
		reg = a.opts.RegistryFn()
		if reg == nil {
			return nil
		}
	} else {
		reg = a.opts.Registry
	}
	if len(skill.allowed) > 0 {
		return reg.Filter(skill.allowed)
	}
	return reg
}

// Run executes the ReAct loop for userInput, streaming tokens and tool-call
// blocks to sink. It returns the updated conversation history (including
// the new user turn and all assistant/tool turns) or a typed error.
//
// When Options.MaxConversationSeconds is set, the incoming ctx is wrapped
// with a deadline. Hitting the deadline surfaces as ErrConversationTimeout
// (not the bare context.DeadlineExceeded) so callers can distinguish it from
// upstream cancellation.
func (a *Agent) Run(ctx context.Context, userInput string, sink render.Sink) ([]llm.Message, error) {
	// Resolve the active skill exactly once per Run. The returned prompt /
	// allowed-tools are stable for the whole loop, so we pass them as values
	// through resolveRegistry / buildSystemPrompt rather than re-invoking the
	// provider per step.
	skill, err := a.resolveSkill(ctx)
	if err != nil {
		return nil, err
	}

	reg := a.resolveRegistry(skill)
	if reg == nil {
		return nil, fmt.Errorf("agent: no registry available")
	}

	var deadline time.Time
	if a.opts.MaxConversationSeconds > 0 {
		deadline = time.Now().Add(time.Duration(a.opts.MaxConversationSeconds) * time.Second)
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}

	sysPrompt := a.buildSystemPrompt(reg, skill)

	msgs := make([]llm.Message, 0, len(a.opts.History)+2)
	msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: sysPrompt})
	// Never replay a system message from prior turns. The system prompt is
	// rebuilt every turn from current state (environment memory, plan, skill,
	// tool catalog), and some provider adapters resolve the system block
	// last-wins over the message list — so an accumulated older copy would
	// silently override the fresh one (e.g. a fact recorded this session would
	// not take effect until the next). Skipping RoleSystem here also keeps the
	// returned history free of system messages, so it never accumulates.
	for _, m := range a.opts.History {
		if m.Role == llm.RoleSystem {
			continue
		}
		msgs = append(msgs, m)
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: userInput})

	llmTools := reg.ToolsFor(a.opts.Provider.Name())

	var finalErr error
	defer func() { a.fireOnStop(ctx, finalErr) }()

	for step := 0; step < a.opts.MaxSteps; step++ {
		// Translate the wrapped-context deadline into a typed error so the
		// TUI can show "wall-clock limit reached" instead of a generic
		// context-cancelled message. Caller cancellations propagate as-is.
		if !deadline.IsZero() && time.Now().After(deadline) {
			finalErr = ErrConversationTimeout
			return msgs, ErrConversationTimeout
		}
		assistant, err := a.streamAssistantTurn(ctx, msgs, llmTools, sink)
		if err != nil {
			if !deadline.IsZero() && errors.Is(err, context.DeadlineExceeded) {
				finalErr = ErrConversationTimeout
				return msgs, ErrConversationTimeout
			}
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
			toolMsg, err := a.dispatchTool(ctx, tc, reg, sink)
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
		if chunk.Usage != nil {
			if sink != nil {
				sink.RecordUsage(*chunk.Usage)
			}
			if err := a.fireOnUsage(ctx, *chunk.Usage); err != nil {
				return llm.Message{}, err
			}
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
func (a *Agent) dispatchTool(ctx context.Context, tc llm.ToolCall, reg *tools.Registry, sink render.Sink) (llm.Message, error) {
	for _, h := range a.hooks {
		if err := h.BeforeToolCall(ctx, tc); err != nil {
			return llm.Message{
				Role:       llm.RoleTool,
				Content:    fmt.Sprintf("error: %v", err),
				ToolCallID: tc.ID,
			}, err
		}
	}

	obs, runErr := a.runTool(ctx, tc, reg, sink)

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
func (a *Agent) runTool(ctx context.Context, tc llm.ToolCall, reg *tools.Registry, sink render.Sink) (tools.Observation, error) {
	if sink != nil {
		sink.BeginToolCall(tc.Name, string(tc.Arguments))
	}
	tool, ok := reg.Get(tc.Name)
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
			// Mirror the table-aware composition formatObservation uses
			// so the operator sees the same data the LLM does, not just
			// the bare summary string.
			sink.EndToolCall(observationText(obs), nil)
		}
	}
	return obs, err
}

// formatObservation converts an Observation and optional error into the
// text fed back to the LLM as the tool result. When the observation
// carries a Table (every k8s.list_* tool does), the rows are rendered as
// a GitHub-flavored markdown table appended to Text — without this the
// LLM only saw the summary string ("3 node(s)") and had no row data to
// reason from. The same composed text is what runTool emits to the TUI
// sink so the operator sees what the model sees.
func (a *Agent) formatObservation(obs tools.Observation, err error) string {
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return truncateMiddle(observationText(obs), a.opts.MaxToolTokens)
}

// observationText composes obs.Text with a rendered Table (when
// present) into the single string surfaced to both the LLM and the TUI.
// Rendering is GitHub-flavored markdown so glamour can pretty-print
// the result in the TUI and the LLM sees a structure it can reason about.
func observationText(obs tools.Observation) string {
	text := obs.Text
	if obs.Table == nil || len(obs.Table.Rows) == 0 {
		return text
	}
	tbl := renderMarkdownTable(obs.Table)
	if text == "" {
		return tbl
	}
	// Trim trailing newlines on text so the join doesn't produce a triple
	// newline (text "3 node(s)\n" + "\n\n" + tbl → blank paragraph above
	// the table in glamour output and a misleading structural gap in the
	// LLM-visible payload).
	return strings.TrimRight(text, "\n") + "\n\n" + tbl
}

// renderMarkdownTable produces a GitHub-flavored markdown table from a
// render.Table. Cells with pipes or newlines are escaped so the table
// stays parseable.
func renderMarkdownTable(t *render.Table) string {
	if t == nil || len(t.Headers) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteByte('|')
	for _, h := range t.Headers {
		sb.WriteByte(' ')
		sb.WriteString(escapeMarkdownCell(h))
		sb.WriteString(" |")
	}
	sb.WriteByte('\n')
	sb.WriteByte('|')
	for i := range t.Headers {
		sep := " --- |"
		if i < len(t.Aligns) {
			switch t.Aligns[i] {
			case render.AlignRight:
				sep = " ---: |"
			case render.AlignCenter:
				sep = " :---: |"
			}
		}
		sb.WriteString(sep)
	}
	sb.WriteByte('\n')
	for _, row := range t.Rows {
		sb.WriteByte('|')
		for i := 0; i < len(t.Headers); i++ {
			// Pad short rows with empty cells so column alignment under
			// the separator rule stays consistent. A row with fewer
			// cells than headers (or zero cells) otherwise emits a
			// malformed markdown row that downstream renderers split or
			// align incorrectly.
			var cell string
			if i < len(row) {
				cell = row[i]
			}
			sb.WriteByte(' ')
			sb.WriteString(escapeMarkdownCell(cell))
			sb.WriteString(" |")
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// escapeMarkdownCell replaces characters that would break a markdown
// table row (the cell separator and newlines). Backslashes are escaped
// first so a pre-existing `\|` in cell content doesn't double-escape
// into `\\|` (which a markdown parser would read as literal-backslash
// followed by a cell terminator, splitting the cell in two).
func escapeMarkdownCell(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}

// truncateMiddle preserves both the head and tail of s when its length exceeds
// max, replacing the middle with a marker. The split favors the tail (2/3)
// because SRE diagnostics (stack traces, error lines, slowest rows) typically
// land at the bottom of the buffer — a single trailing cut loses them.
func truncateMiddle(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	marker := fmt.Sprintf("\n…[truncated %d chars]…\n", len(s)-max)
	if max <= len(marker) {
		return s[:max]
	}
	keep := max - len(marker)
	head := keep / 3
	tail := keep - head
	return s[:head] + marker + s[len(s)-tail:]
}

// buildSystemPrompt assembles base preamble + skill catalog + active skill
// prompt + tool catalogue from the given registry snapshot. skill carries the
// already-resolved SkillProvider output for this Run.
func (a *Agent) buildSystemPrompt(reg *tools.Registry, skill resolvedSkill) string {
	var sb strings.Builder
	sb.WriteString(basePreamble)
	if a.opts.Plan {
		sb.WriteString(planDirective)
	}
	// Durable cross-session memory is injected high in the prompt (right after
	// the preamble, before the per-run skill/tool catalogs) so the model treats
	// recorded facts as standing background rather than transient noise.
	if a.opts.EnvironmentMemory != "" {
		sb.WriteString("\n\n## Environment memory\n")
		sb.WriteString("Durable facts you recorded about this operator's environment in earlier " +
			"sessions. Treat them as trusted background, but re-verify with tools when a fact may " +
			"be stale:\n")
		sb.WriteString(a.opts.EnvironmentMemory)
	}
	// Skill catalog (all skills by name + description) is listed ONLY when no
	// skill is active. Once the operator has switched into a skill — its full
	// body is injected just below — the catalog of the OTHER skills is noise
	// that only inflates the per-request token floor, which matters on small
	// models. With no active skill the catalog lets the model answer "what
	// skills do you have?".
	if a.opts.Skills != nil && skill.prompt == "" {
		all := a.opts.Skills.List()
		if len(all) > 0 {
			sb.WriteString("\n\n## Available skills\n")
			for _, s := range all {
				sb.WriteString(fmt.Sprintf("- **%s** — %s\n", s.Name, s.Description))
			}
		}
	}
	if skill.prompt != "" {
		sb.WriteString("\n\n## Active skill: ")
		sb.WriteString(skill.name)
		sb.WriteString("\n")
		sb.WriteString(skill.prompt)
	}
	tools := reg.List()
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

// fireOnUsage broadcasts usage to every hook. The first non-nil error wins
// and aborts the loop — used by CostGuardHook to enforce budget caps.
func (a *Agent) fireOnUsage(ctx context.Context, u llm.Usage) error {
	for _, h := range a.hooks {
		if err := h.OnUsage(ctx, u); err != nil {
			return err
		}
	}
	return nil
}
