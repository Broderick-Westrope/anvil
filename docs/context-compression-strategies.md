# Context Compression Strategies for Agent Harnesses

A research-backed guide to managing LLM context degradation in long-running
agent sessions. Covers three compression techniques and a token-saving
tool-loading pattern.

---

## Table of Contents

- [The Problem](#the-problem)
- [1. Observation Masking](#1-observation-masking)
- [2. Verbatim Compaction](#2-verbatim-compaction)
- [3. Anchored Iterative Summarisation](#3-anchored-iterative-summarisation)
- [4. Lazy Tool Loading](#4-lazy-tool-loading)
- [Layered Strategy](#layered-strategy)
- [Sources](#sources)

---

## The Problem

LLM performance degrades as context grows. Research shows accuracy can drop
from 87% to 54% under context overload, with noticeable degradation starting
at ~40% context utilisation. The primary causes:

1. **Lost in the Middle** — LLMs attend strongly to the start and end of
   context but largely ignore the middle (U-shaped attention curve), causing
   up to 30% performance loss on mid-positioned information.
2. **Tool Definition Tax** — Connecting a few MCP servers can consume 50k+
   tokens on tool schemas alone before any real work begins.
3. **Conversation Bloat** — Tool outputs (file reads, test logs, grep results)
   accumulate fast and carry diminishing value over time.

The goal is not to avoid context, but to use it intentionally. The following
strategies attack different parts of the problem.

---

## 1. Observation Masking

**Source**: JetBrains Research — "The Complexity Trap" (NeurIPS '25 DL4C
workshop). [Paper](https://arxiv.org/abs/2508.21433),
[Code](https://github.com/JetBrains-Research/the-complexity-trap).

### What It Is

A rolling-window approach that replaces old tool/environment outputs with
placeholders while preserving the full reasoning and action trace. The insight
is that for coding agents, *what you decided to do* matters more than *the raw
output you read*.

### How It Works

Given a trajectory of turns `T₁, ..., Tₙ` where each turn `Tᵢ = (reasoning,
action, observation)`, the masking function replaces observations outside a
window of size `M`:

```
observation'ᵢ = placeholder   if i < n - M
observation'ᵢ = observationᵢ  if i ≥ n - M
```

The placeholder is a short message like:
`"Old environment output: (247 lines omitted)"`

All reasoning and action entries remain untouched regardless of age.

### Implementation

SWE-agent's `LastNObservations` history processor is the reference
implementation:

```python
class LastNObservations(BaseModel):
    n: int           # Number of recent observations to keep.
    polling: int = 1 # Steps between pruning passes.

    def _get_omit_indices(self, history: History) -> list[int]:
        observation_indices = [
            idx for idx, entry in enumerate(history)
            if entry["message_type"] == "observation"
            and not entry.get("is_demo", False)
        ]
        last_removed_idx = max(
            0,
            (len(observation_indices) // self.polling) * self.polling - self.n,
        )
        # Never remove the first observation (instance/system template).
        return observation_indices[1:last_removed_idx]

    def __call__(self, history: History) -> History:
        new_history = []
        omit_indices = self._get_omit_indices(history)

        for idx, entry in enumerate(history):
            if idx not in omit_indices:
                new_history.append(entry)
            else:
                data = entry.copy()
                text = _get_content_text(data)
                _set_content_text(
                    data,
                    f"Old environment output: "
                    f"({len(text.splitlines())} lines omitted)",
                )
                new_history.append(data)
        return new_history
```

Key details:
- The `polling` parameter controls how frequently pruning recalculates,
  enabling better prompt-cache reuse (fewer cache invalidations).
- Tag-based exceptions (`always_keep_output_for_tags`) let you protect
  specific tool outputs from masking.

### Tuning

Window size is agent-specific:
- **SWE-agent**: `M=10` was optimal (SWE-agent skips failed retry turns).
- **OpenHands**: Requires a larger window (includes all turns including
  retries).
- Rule of thumb: start at 10 and increase if the agent starts re-reading
  files it already processed.

### Results (SWE-bench Verified, 500 instances)

| Metric | vs Raw Agent | vs LLM Summarisation |
|--------|-------------|---------------------|
| Cost | **-52%** | Comparable |
| Solve rate (Qwen3-Coder 480B) | **+2.6%** | Outperformed in 4/5 configs |

### Hybrid Extension

JetBrains also tested a hybrid approach:
- **Primary**: Observation masking handles all turns up to a threshold
  (`N=43`).
- **Fallback**: LLM summarisation triggers as a last resort for very long
  trajectories, using the unmasked context as input.
- **Result**: 7% cheaper than pure masking, 11% cheaper than pure
  summarisation.

### Strengths and Weaknesses

| Strengths | Weaknesses |
|-----------|-----------|
| Simple to implement | Context can still grow unbounded (slowed, not capped) |
| No additional API calls | Window size needs per-agent tuning |
| Preserves full decision trace | Doesn't help if the bloat is in reasoning, not observations |
| Zero hallucination risk | |

---

## 2. Verbatim Compaction

**Source**: Morph — "Compaction vs Summarization".
[Post](https://www.morphllm.com/compaction-vs-summarization).

### What It Is

Instead of rewriting or summarising context, verbatim compaction **deletes
low-signal tokens entirely**. Every surviving line is character-for-character
identical to the original input — no paraphrasing, no hallucination risk.

### How It Works

1. A model reads the full context and **scores each line by relevance** to the
   current task.
2. Lines below a relevance threshold are removed.
3. Structure and formatting of surviving content are preserved.

What typically gets removed vs kept:

| Removed | Kept |
|---------|------|
| Irrelevant file reads (`[reads src/middleware/cors.ts]`) | All file paths and exact references |
| Tangential grep results | Error messages verbatim |
| Verbose test output (200+ line logs) | Line numbers |
| Redundant context from earlier exploration | Multi-step reasoning about the fix |
| | Summary conclusions |

### Performance

| Metric | Value |
|--------|-------|
| Compression ratio | 50–70% |
| Verbatim accuracy | 98% |
| Hallucination risk | 0% |
| Speed | 3,300+ tokens/sec |

The compression ratio is lower than summarisation-based approaches (which
achieve 80–99%), but the fidelity tradeoff matters enormously for coding
agents where a paraphrased file path or approximated line number is worse than
no information at all.

### Implementation Considerations

Morph's production system uses custom inference engines purpose-built for the
compaction workload to achieve sub-3-second latency. A naive implementation
using standard LLM APIs takes 8–15 seconds per compaction pass, which is too
slow for inline use.

For a custom harness, the practical approach is:
- Use a fast, small model (not your primary reasoning model) for relevance
  scoring.
- Score at the line or block level, not token level.
- Run compaction asynchronously when context pressure crosses a threshold,
  not inline on every turn.

### Strengths and Weaknesses

| Strengths | Weaknesses |
|-----------|-----------|
| Zero hallucination — surviving text is identical to original | Lower compression ratio than summarisation |
| Preserves exact technical details (paths, errors, line numbers) | Requires a fast scoring model or custom infra |
| Fully inspectable — you can audit what was kept/dropped | Naive implementation is too slow for inline use |
| Ideal for coding agents where precision matters | Relevance scoring quality depends on the scoring model |

---

## 3. Anchored Iterative Summarisation

**Source**: Factory.ai — "Evaluating Context Compression for AI Agents".
[Post](https://factory.ai/news/evaluating-compression). Evaluated across
36,611 messages from production software engineering sessions.

### What It Is

A structured, incremental summarisation approach that maintains a persistent
"anchor" document with explicit sections. Unlike naive rolling summarisation
(which regenerates the full summary each cycle and drifts over time), this
approach only summarises newly-evicted content and merges it into the existing
anchor.

### The Anchor State Structure

The anchor is a structured document with four mandatory sections:

| Section | Purpose |
|---------|---------|
| **Session Intent** | What the user wants to accomplish |
| **File Modifications** | Which files have been touched and what changed |
| **Decisions Made** | Reasoning behind past choices |
| **Next Steps** | What needs to be done next |

The structure acts as a checklist: the summariser must populate each section or
explicitly leave it empty. This prevents the silent information drift that
kills freeform summaries.

### How Merging Works

```
1. Context pressure crosses threshold (~70%)
2. Identify the newly-evictable span (messages being dropped)
3. Summarise ONLY that span (not the full history)
4. Merge the new span summary into the existing anchor:
   - Append new file modifications
   - Update decisions if new ones were made
   - Revise next steps based on progress
   - Preserve session intent (rarely changes)
5. Replace evicted messages with the updated anchor
6. Append recent messages in full after the anchor
```

The critical difference from full-reconstruction summarisation: you never
re-summarise content that's already in the anchor. Each piece of information
passes through the summarisation step exactly once, which limits compounding
drift.

### Evaluation Framework

Factory uses a probe-based evaluation with four probe types:

| Probe Type | Tests | Example |
|------------|-------|---------|
| **Recall** | Factual retention | "What was the original error message?" |
| **Artifact** | File tracking | "Which files have we modified? Describe changes." |
| **Continuation** | Task planning | "What should we do next?" |
| **Decision** | Reasoning chains | "What did we decide and why?" |

Responses are graded by an LLM judge across six dimensions: accuracy, context
awareness, artifact trail, completeness, continuity, and instruction
following. Each scored 0–5 with explicit rubrics.

### Results

| Method | Overall Score | Accuracy | Compression |
|--------|-------------|----------|-------------|
| Factory (Anchored) | **3.70/5** | **4.04/5** | 98.6% |
| Anthropic (Compaction) | 3.44/5 | 3.44/5 | ~98% |
| OpenAI (Opaque) | 3.35/5 | 3.43/5 | 99.3% |

Factory's 0.7% lower compression ratio compared to OpenAI yielded a 0.35
quality point advantage overall and a 0.61 point advantage on accuracy
(preserving file paths, function names, error messages).

### Strengths and Weaknesses

| Strengths | Weaknesses |
|-----------|-----------|
| Highest accuracy for technical detail preservation | More complex to implement than masking |
| Structured anchor prevents drift | Still uses LLM summarisation (cost + latency per cycle) |
| Incremental merging limits compounding errors | Anchor structure needs to be designed per domain |
| Each fact summarised exactly once | Quality depends on the summarisation model |

---

## 4. Lazy Tool Loading

### What It Is

Instead of injecting all tool schemas into context upfront (which can consume
50k+ tokens for a handful of MCP servers), lazy tool loading uses a two-phase
approach: show a lightweight catalogue first, then inject full schemas only
for the tools the model actually selects.

### Two-Phase Pattern

**Phase 1 — Lightweight Catalogue**

Include only tool names and one-line descriptions in the system prompt or
tool list:

```json
[
  {"name": "bash", "description": "Execute shell commands"},
  {"name": "edit", "description": "Find-and-replace in a file"},
  {"name": "grep", "description": "Search file contents by regex"},
  {"name": "view", "description": "Read a file with line numbers"},
  {"name": "glob", "description": "Find files by name pattern"}
]
```

This costs ~200 tokens instead of ~10,000 for full schemas.

**Phase 2 — On-Demand Schema Injection**

When the model declares intent to use a tool (or a `tool_search` meta-tool),
inject the complete schema for only the selected tools into the next turn.

### Implementation Approaches

#### A. Tool Search Meta-Tool (Claude's Pattern)

Provide a single `tool_search` tool. The model calls it to discover available
tools, and the harness injects matching full schemas:

```
Turn 1: Model receives system prompt + tool_search tool only
Turn 2: Model calls tool_search("file editing")
Turn 3: Harness injects full schemas for edit, multiedit, write
Turn 4: Model calls edit with full parameter knowledge
```

Tradeoff: adds one round-trip per new tool category, but dramatically reduces
baseline context.

#### B. Middleware-Based Filtering (LangChain Pattern)

Filter the tool list at call time based on conversation state, user
permissions, or task phase:

```python
def select_tools(request, all_tools):
    if request.state.get("phase") == "discovery":
        return [t for t in all_tools if t.name in {"grep", "glob", "ls"}]
    elif request.state.get("phase") == "editing":
        return [t for t in all_tools if t.name in {"edit", "multiedit", "view"}]
    return all_tools
```

Tradeoff: requires the harness to track task phase, but avoids extra
round-trips.

#### C. Semantic Tool Matching

Embed all tool descriptions into a vector store. On each turn, retrieve the
top-k most relevant tools based on the user's message:

```python
relevant_tools = tool_index.search(user_message, top_k=5)
full_schemas = [tool_registry[t.name] for t in relevant_tools]
```

Tradeoff: works well for large toolsets (50+), but adds embedding lookup
latency and may miss tools the model would have creatively applied.

#### D. Hierarchical Grouping

Cluster tools into categories. The model picks a category first, then gets
the full schemas for that group:

```
Categories: [file_ops, search, execution, version_control]
  └─ file_ops: [edit, multiedit, write, view, download]
  └─ search: [grep, glob, sourcegraph, lsp_references]
  └─ execution: [bash, job_output, job_kill]
  └─ version_control: [bash(git), gh]
```

### Token Savings

| Approach | Typical Baseline | With Lazy Loading | Savings |
|----------|-----------------|-------------------|---------|
| 5 MCP servers (~50 tools) | ~50,000 tokens | ~500–2,000 tokens | **96–99%** |
| 15 built-in tools | ~10,000 tokens | ~200–1,500 tokens | **85–98%** |

The savings scale with the number of tools. For small toolsets (<10), the
overhead of the meta-tool round-trip may not be worth it. For 20+ tools,
lazy loading is almost always net positive.

### Anvil's Implementation

Anvil implements lazy tool loading for MCP servers via the `lazy_description`
config field. The approach combines elements of the Tool Search Meta-Tool
and Middleware-Based Filtering patterns above:

1. **Configuration**: Set `lazy_description` on an MCP server in
   `anvil.json`. The presence of this field makes the server lazy; its value
   is the one-line description shown to the agent.
2. **Eager connect, lazy context**: All servers connect at startup. Only the
   tool descriptions are withheld from the LLM context window.
3. **`enable_mcp` tool**: A built-in tool whose description lists available
   lazy servers. The agent calls it when it needs a server's capabilities.
   This adds one round-trip, then all tools from that server appear on the
   next turn.
4. **Human toggle**: The MCP palette dialog (Ctrl+P → "MCP Servers") lets
   users enable/disable servers and toggle lazy MCPs via keyboard shortcuts.
5. **Branch-scoped state**: Enabled state is derived from message history
   (both `enable_mcp` tool calls and `MCPToggleContent` metadata messages).
   It persists across restarts and survives compaction.
6. **Per-turn filtering**: `PrepareStep` filters the tool list every turn
   based on the current branch's enabled set.

```json
{
  "mcp": {
    "datadog": {
      "type": "http",
      "url": "https://mcp.datadoghq.com/...",
      "lazy_description": "Datadog monitoring, observability, and APM."
    }
  }
}
```

Key files: `internal/agent/lazy_mcp.go` (state derivation and filtering),
`internal/agent/tools/enable_mcp.go` (agent-side tool),
`internal/ui/dialog/mcp_palette.go` (human-side dialog).

### Pairing with Prompt Caching

Lazy loading composes well with prompt caching:
- Cache the lightweight catalogue (stable across turns).
- Cache full schemas on first injection (reused if the same tool is needed
  again).
- Net effect: first use of a tool costs one extra round-trip; subsequent uses
  hit cache.

---

## Layered Strategy

These techniques aren't mutually exclusive. A practical layered approach,
triggered at ~70% context utilisation:

```
Layer 1: Observation Masking (always on)
  └─ Replace tool outputs older than M turns with placeholders.
  └─ Zero cost, zero risk, biggest single impact.

Layer 2: Verbatim Compaction (at 70% pressure)
  └─ Score remaining context by relevance, delete low-signal lines.
  └─ Preserves exact technical details. No hallucination.

Layer 3: Anchored Iterative Summarisation (at 85% pressure)
  └─ Summarise the oldest masked section into the structured anchor.
  └─ Most aggressive compression, highest information loss.

Layer 0 (always on): Lazy Tool Loading
  └─ Orthogonal to the above. Reduces baseline context before
     conversation even begins.
```

The principle: **never rewrite what you can delete, never summarise what you
can mask**. Each layer introduces more information loss, so escalate only when
cheaper options are exhausted.

### Monitoring

Track these metrics to know when compression is working (or failing):

| Metric | What it tells you |
|--------|-------------------|
| Context utilisation % per turn | Whether you're approaching the danger zone |
| Compression cycles triggered | How often each layer fires |
| Agent re-reads (same file read twice) | Compression dropped something important |
| Trajectory length trend | Summarisation causing the agent to run longer (drift signal) |
| Task completion rate | The only metric that ultimately matters |

---

## Sources

- JetBrains Research — "The Complexity Trap": [Paper](https://arxiv.org/abs/2508.21433), [Code](https://github.com/JetBrains-Research/the-complexity-trap)
- Factory.ai — "Evaluating Context Compression for AI Agents": [Post](https://factory.ai/news/evaluating-compression)
- Morph — "Compaction vs Summarization": [Post](https://www.morphllm.com/compaction-vs-summarization)
- Anthropic — "Effective Context Engineering for AI Agents": [Docs](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- LangChain — Context Engineering: [Docs](https://docs.langchain.com/oss/python/langchain/context-engineering)
- LangMem — Summarization Guide: [Docs](https://langchain-ai.github.io/langmem/guides/summarization/)
