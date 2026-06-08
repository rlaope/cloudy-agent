# RFC: Knowledge / RAG Layer for cloudy

- **Status**: Draft after v0.6.0 foundation; corpus/indexing deferred
- **Target**: v0.7.0+ (phased)
- **Owner**: TBD
- **Last updated**: 2026-06-08

> TL;DR — Today cloudy is a smart, read-only multi-cluster SRE CLI: it knows
> *how* to investigate a cluster but nothing about *your* cluster — your
> services, your runbooks, your past incidents. This RFC proposes a
> retrieval-augmented generation (RAG) layer rooted at `~/.cloudy/knowledge/`
> that slots into the existing skills pipeline as a new `SkillProvider`
> implementation. No mutation surface is added. The agent stays read-only;
> the corpus is just *another* read-only source the LLM can consult.

---

## 1. Problem statement

cloudy v0.6 ships 34 skill playbooks and 121 tools, all generic. The
`incident-context` skill can already cross-reference firing alerts with
Argo CD syncs and pod restarts, and the incident-memory case-card store can
inject approved prior examples — but cloudy still has no first-class corpus
for *your* runbooks, service ownership, alert ontology, or postmortems.

### Now answerable

| Question                                            | Today                                  | With RAG                                                                                  |
| --------------------------------------------------- | -------------------------------------- | ----------------------------------------------------------------------------------------- |
| "Payments p99 spiked, what should I check?"         | Generic: pods, DB, upstream            | "Matches INC-142 Redis-pool signature. Runbook step 1: check Redis client count."         |
| "Who owns this alert?"                              | "I can't tell from the alert payload." | "payments-team, `#payments-oncall`, pagerduty rota `payments-primary`."                   |
| "Has this happened before?"                         | "No historical context."               | Cites prior postmortem(s) by ID.                                                          |
| "What does `SLOErrorBudgetBurnFast` mean here?"     | Definition from the name only.         | Alert ontology: typical causes, first response, severity convention.                      |

### Unchanged (do not oversell)

- **Live cluster state.** Real-time pod status, logs, traces still
  flow through the existing tools. RAG is static org context, not a
  substitute for `k8s.list_pods`.
- **Brand-new clusters.** Empty corpus = exactly v0.6 behaviour;
  opt-in by file presence.
- **Knowledge not written down.** RAG retrieves what is in the corpus,
  period.

---

## 2. Corpus shape (`~/.cloudy/knowledge/`)

The corpus is a plain directory of files the operator hand-curates (or
syncs from a wiki). No DB, no service. cloudy never writes to it from
inside the agent loop (see §9).

The v1 incident-memory layer is a smaller precursor, not the full RAG corpus:
approved HITL case cards live under
`~/.cloudy/incident-memory/cards.jsonl` and may be exported or adapted into
this corpus later. Candidate and rejected cards are not part of the default
RAG input. The flat `memory.md` file remains separate environment memory
written by the agent-callable `memory.record` tool.

```
~/.cloudy/knowledge/
├── runbooks/
│   ├── payments-high-5xx.md
│   ├── search-latency.md
│   └── ...
├── postmortems/
│   ├── INC-142-redis-pool-exhaustion.md
│   ├── INC-157-kafka-rebalance-storm.md
│   └── ...
├── services/
│   ├── payments.yaml
│   ├── search.yaml
│   └── ...
├── alerts/
│   ├── PaymentsHigh5xx.yaml
│   ├── SLOErrorBudgetBurnFast.yaml
│   └── ...
└── index/
    ├── index.json            # chunk → file + offset + embedding handle
    ├── vectors.bin           # packed float32 embeddings (1 row per chunk)
    └── meta.json             # corpus hash, model, dimension, built_at
```

### File formats

**`runbooks/*.md`** — prose with short YAML frontmatter
(`name`, `service`, `alerts`, `tags`, `updated`), mirroring the
existing skill-file shape.

**`postmortems/*.md`** — standard incident writeup. Required
frontmatter: `id`, `date`, `services`, `severity`, `summary`.

**`services/<service>.yaml`**:

```yaml
name: payments
owner_team: payments
slack: "#payments-oncall"
pagerduty_rota: payments-primary
slo: {availability: 99.95, latency_p99_ms: 250}
dependencies: [redis-primary, postgres-payments]
repos: [github.com/acme/payments-api]
```

**`alerts/<alert_name>.yaml`**:

```yaml
name: PaymentsHigh5xx
severity: page
description: 5xx ratio on payments exceeded 1% for 5m.
typical_causes: [Redis client pool exhaustion (INC-142), DB failover, Bad deploy]
first_response: [Check payments-high-5xx step 1, Page payments-primary at 10m]
```

### `index.json` (illustrative)

```json
{
  "version": 1, "model": "all-MiniLM-L6-v2", "dimension": 384,
  "chunks": [{
    "id": "rb-payments-high-5xx-0",
    "source": "runbooks/payments-high-5xx.md",
    "kind": "runbook", "offset": 0, "length": 612,
    "hash": "sha256:…", "title": "Payments high 5xx — Redis pool check"
  }]
}
```

The `source` field is load-bearing — citations resolve back to a file
the operator can open.

---

## 3. Embedding + retrieval mechanics

### Embedding model

**Default: `all-MiniLM-L6-v2` (384-dim, ~80 MB) via sentence-transformers
GGUF.** Offline (cloudy runs on bastions with no outbound), zero per-token
cost, ~80 ms/chunk on one CPU core (500-chunk corpus indexes in under a
minute).

**Opt-in: `text-embedding-3-small` (OpenAI)** for shops that prefer ~3×
higher retrieval quality and have the credentials wired:

```yaml
knowledge:
  backend: offline   # offline | openai | anthropic-voyage
```

Trade-off: adds a network dependency for a non-agent operation and ~$0.02
per 1k chunks.

### Chunking

- **Size**: 512 tokens, overlap 64.
- **Splitter**: markdown headings → paragraphs → hard cap. YAML files
  chunk as one whole-document plus one per top-level key.
- **Dedup**: sha256 of normalised chunk text; identical hashes stored
  once with multiple source pointers.

### Index format

- `index/vectors.bin` — packed float32, row-major, mmap'd on start.
- `index/meta.json` — corpus hash, model, dimension, `built_at`.
  Mismatch on boot → "stale index, run `cloudy knowledge refresh`" warning.

### Query path

```
question → embed → cosine top-K (K=5) → render + cite
         → inject as system-prompt section → existing ReAct loop
```

No LLM-graded reranker in the first implementation. Per the architect invariant, the LLM
eats all K=5 chunks and decides what is useful.

### Re-indexing

Explicit only — `cloudy knowledge refresh`. No filesystem watcher in
the first implementation (silent mid-session context swaps surprise operators).

---

## 4. The `SkillProvider` interface

Architect's review flagged this as a prerequisite. Small, self-contained,
unblocks RAG without behaviour change.

**Today**: `internal/skills/skill.go` exposes a concrete `*skills.Skill`
consumed directly by `agent.Options.Skill`. `buildSystemPrompt` reads
`a.opts.Skill.SystemPrompt` inline.

**Proposed**:

```go
package skills

type RunContext struct {
    UserInput string
    Now       time.Time
}

type Resolved struct {
    SystemPrompt string     // prepended to system prompt
    AllowedTools []string   // tool whitelist; empty = inherit
    Citations    []Citation // surfaced in final answer
}

type Citation struct{ Source, Title string; Score float32 }

type SkillProvider interface {
    Resolve(ctx context.Context, rc RunContext) (Resolved, error)
}
```

**`StaticSkill`** (v0.6.0, zero-behaviour refactor):

```go
type StaticSkill struct{ S *Skill }
func (s StaticSkill) Resolve(_ context.Context, _ RunContext) (Resolved, error) {
    return Resolved{SystemPrompt: s.S.SystemPrompt, AllowedTools: s.S.AllowedTools}, nil
}
```

**`RAGSkill`** (v0.6.2):

```go
type RAGSkill struct {
    Base  *Skill            // underlying playbook
    Index *knowledge.Index
    TopK  int
}
func (r RAGSkill) Resolve(ctx context.Context, rc RunContext) (Resolved, error) {
    hits, err := r.Index.Search(ctx, rc.UserInput, r.TopK)
    if err != nil { return Resolved{}, err }
    return Resolved{
        SystemPrompt: r.Base.SystemPrompt + "\n\n## Retrieved context\n" + renderHits(hits),
        AllowedTools: r.Base.AllowedTools, // RAG never widens the whitelist
        Citations:    toCitations(hits),
    }, nil
}
```

`RAGSkill` *composes* a base skill. The base still constrains the tool
whitelist — **RAG cannot hand the LLM a new tool.** This is the
load-bearing safety property.

---

## 5. Integration with the existing agent loop

Chokepoint: `(*Agent).buildSystemPrompt` in `internal/agent/agent.go`
(~lines 500–526), which today reads `a.opts.Skill.SystemPrompt` directly.

Conceptual diff:

- Add `Options.Provider SkillProvider`; for v0.6.0 keep `Options.Skill`
  and prefer `Provider` when set (deprecate in v0.7).
- `Run` calls `provider.Resolve(ctx, RunContext{UserInput: userInput, Now: time.Now()})`
  once, *before* `buildSystemPrompt`.
- `Resolved.SystemPrompt` is appended under `## Active skill: …`.
- `Resolved.AllowedTools`, when non-empty, drives the existing
  `reg.Filter(...)` in `resolveRegistry`.
- `Resolved.Citations` flow to the final message via a new
  `OnFinalResponse` hook (v0.7).

Skill catalog, hook chain, registry resolution, and ReAct loop body are
untouched. One well-typed seam, not a rewrite.

---

## 6. CLI surface

New top-level `cloudy knowledge`, registered like `cloudy skills`
(`internal/cli/skills.go` is the template):

| Command                            | Purpose                                                                    |
| ---------------------------------- | -------------------------------------------------------------------------- |
| `cloudy knowledge add <file>`      | Copy/symlink a `.md`/`.yaml` into the corpus, incrementally index.         |
| `cloudy knowledge refresh`         | Full re-embed. Bumps `index/meta.json.built_at`.                           |
| `cloudy knowledge list`            | Indexed chunks: `KIND · SOURCE · TITLE · CHUNKS`.                          |
| `cloudy knowledge ask <question>`  | **Dry run** — top-K chunks + scores, no LLM call. Operator debug tool.     |
| `cloudy knowledge stat`            | Corpus size, chunk count, model, last refresh, index path.                 |

`knowledge ask` is the load-bearing affordance: operators audit what
the agent will see before trusting a retrieval-heavy workflow.

---

## 7. Sample interaction

**Query**: "Payments p99 spiked, anything in the runbook?"

### Before (v0.5)

> `payments-api`: 4 pods, restartCount 0. p99 = 0.71s (baseline 0.18s).
> No recent rollouts. Possibly a downstream dependency — try checking
> the database or upstream services.

### After (v0.6.2 with corpus)

> Payments p99 is 0.71s (baseline 0.18s, 4×). Matches **INC-142**
> (March) — Redis pool exhaustion when the connection-recycler stalls.
>
> Runbook **`payments-high-5xx`** step 1: verify Redis client count
> (`redis-cli client list | wc -l`; expected <500, alerted >800).
>
> Owner: **payments-team** via `#payments-oncall`. SLO from
> `services/payments.yaml`: p99 ≤ 250 ms, availability 99.95% — both
> currently violated.
>
> Sources: `postmortems/INC-142-redis-pool-exhaustion.md`,
> `runbooks/payments-high-5xx.md`, `services/payments.yaml`.

The difference is not a different LLM. It is the corpus.

---

## 8. Phased rollout

| Version    | Scope                                                                                                                            | User-visible behaviour change |
| ---------- | -------------------------------------------------------------------------------------------------------------------------------- | ----------------------------- |
| **v0.6.0** | Shipped `SkillProvider` interface + `StaticSkill` refactor only. Corpus CLI, indexing, and RAG integration deferred. | None — pure refactor. |
| **v0.7.0** | Corpus directory shape plus `cloudy knowledge {add,list,stat}` (no embeddings). | New subcommands; agent loop unchanged. |
| **v0.7.1** | Embedding pipeline: chunker, sentence-transformers backend, `cloudy knowledge refresh`, `cloudy knowledge ask` dry-run. | Operator-visible retrieval debug surface. |
| **v0.7.2** | `RAGSkill` provider; agent loop integration (system-prompt injection via `Resolve`). | Agent answers using corpus context when present. |
| **v0.8.0** | Citation rendering in agent output (`Sources: …` block); `text-embedding-3-small` opt-in backend; `knowledge` subcommand in TUI. | First-class citations + cloud embedding option. |

Each milestone is independently shippable. v0.6.0 already carved the provider
seam; the next milestone should make the corpus inspectable before retrieval
quality is debated.

---

## 9. Non-goals

- **Agent-driven mutation of the corpus.** No `knowledge.add` tool, no
  LLM-authored runbooks. Operators edit files out-of-band and re-run
  `cloudy knowledge refresh`. Preserves the read-only-by-construction
  invariant.
- **Multi-user shared corpus.** Single operator workstation. Teams that
  want a shared corpus can `git pull` into `~/.cloudy/knowledge/`.
- **LLM-graded reranking.** Eat all K=5; revisit if recall feels weak.
- **Cross-corpus federation** (Notion / Confluence / GitHub at query
  time). Local files only.
- **Secrets in the corpus.** Treat as loggable plaintext. Masking
  applies to tool observations, not retrieved context. Documented loudly.

---

## 10. Open questions

1. **Default embedding backend: offline vs cloud?** Offline
   (`all-MiniLM-L6-v2`) is bastion-friendly but adds a ~80 MB blob and
   a CPU step. Cloud removes the blob but couples a non-agent operation
   to an LLM provider. **The single most important question to settle
   before any implementation starts.**
2. **Citation rendering: inline (`[INC-142]`) vs trailing block?**
   Trailing is unambiguous; inline reads better. Likely trailing for
   v0.7, inline as a follow-up.
3. **YAML catalog files: chunk per top-level key, or whole document?**
   Per-key gives sharper retrieval; whole gives coherence. Lean
   per-key, with parent filename co-embedded as a hint.
4. **RAG-only mode?** Should `RAGSkill` allow no base skill (corpus =
   entire prompt)? Probably no — losing the base loses the tool
   whitelist.
5. **Index versioning across cloudy upgrades.** When the default model
   changes between minor versions: loud warning on `meta.json.model`
   mismatch + opt-out flag to defer re-embedding.

---

## Appendix A — files touched (anticipated)

| Path                                 | Purpose                                                                |
| ------------------------------------ | ---------------------------------------------------------------------- |
| `internal/skills/provider.go` (new)  | `SkillProvider`, `Resolved`, `RunContext`, `StaticSkill`               |
| `internal/skills/skill.go`           | Unchanged (keep `Skill` as the data type)                              |
| `internal/agent/agent.go`            | `Options.Provider`, `Run` calls `Resolve`, `buildSystemPrompt` consumes `Resolved` |
| `internal/knowledge/` (new package)  | `Index`, `Chunker`, `Embedder` interface + offline/openai implementations |
| `internal/cli/knowledge.go` (new)    | `cloudy knowledge {add,refresh,list,ask,stat}`                         |
| `internal/wiring/`                   | Build a `SkillProvider` from config + corpus presence                  |
| `docs/RFC-RAG.md`                    | This document                                                          |

No tool packages change. No `internal/tools/*` is touched.
