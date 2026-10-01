# Agentic Fetch Model Override Implementation Plan

> **Status:** DRAFT
> **Branch:** `feat/agentic-fetch-model` (worktree), base commit `b50540498`.

## Specification

**Problem:** The `agentic_fetch` sub-agent always runs on the global small
model (`models.small`). `internal/agent/agentic_fetch_tool.go:152` calls
`buildAgentModels` with a bare `config.Agent{ID: "agentic_fetch"}`, throws the
large result away, and then uses the small model for both the sub-agent's
large and small slots (`:184-185`). The provider config, system prompt prefix
and prompt template target are all taken from the small model's provider
(`:157`, `:162-165`). A user who wants a stronger (or cheaper, or different
provider's) model for web research must change `models.small`, which also
changes session titles, summarisation, and every other small-model consumer.

**Goal:** A user can set `tools.agentic_fetch.model` in `anvil.json` to a
`provider/model` string, and optionally `tools.agentic_fetch.reasoning_effort`.
When set, every `agentic_fetch` call runs its sub-agent on that model, using
that model's provider config and system prompt prefix. When unset, behaviour
is identical to today (global small model). When the override fails to
*resolve* (bad format, unknown or disabled provider, unknown model), Anvil
logs a warning and falls back to the global small model, so a typo never
breaks the tool. Provider construction or authentication failures for a
valid override still surface as errors, as they do for the small model today.
`anvil_info` shows which model `agentic_fetch` resolves to.

```json
{
  "tools": {
    "agentic_fetch": {
      "model": "anthropic/claude-haiku-4-5",
      "reasoning_effort": "low"
    }
  }
}
```

**Scope:**

In scope:

- New `config.ToolAgenticFetch` struct under `config.Tools`.
- `config.ResolveAgenticFetchModel`, built by extracting the override logic
  already in `ResolveAgentModel` into a shared helper that takes a base model.
- Coordinator wiring in `agentic_fetch_tool.go` plus a
  `buildAgenticFetchModel` helper in `coordinator.go`.
- `anvil_info` `[model]` section line for `agentic_fetch`.
- `schema.json` regeneration and the builtin `anvil-config` skill docs.

Out of scope:

- A new `SelectedModelType` (`models.fetch`), TUI model picker entry, recent
  models, or `UpdatePreferredModel` support. Rationale: `models.*` is the
  user-switchable, persisted, UI-driven surface; a static per-tool override
  matches how named agents already pin models (`Agent.Model`) at a fraction of
  the surface area. Promoting it later is additive.
- `think` and `variant` overrides for the fetch model. The shared helper
  supports them, so adding the JSON fields later is trivial.
- Changing what `agentic_fetch` does, its tools, prompt, or permissions.
- Load-time validation of the model string. Named agents validate at call
  time with a warn-and-fallback; this follows the same pattern.

**Design decisions:**

1. **Base model is the global small model, not large.** `ResolveAgentModel`
   layers an override over `models.large`, inheriting sampling parameters
   (temperature, top_p, etc.) and dropping provider options across providers.
   For `agentic_fetch` the thing being replaced is the small model, so the
   override is layered over `models.small` with identical inheritance rules.
   Unset override therefore returns `models.small` byte-for-byte, which is
   what guarantees zero behaviour change for existing users.
2. **The fetch model fills the sub-agent's large slot; the small slot stays
   the global small model.** The large slot is the one that runs every turn
   and is used for summarisation (`internal/agent/agent.go:820-826`).
   `runSubAgent` already re-derives the small slot from global small on
   credential refresh (`coordinator.go:1589-1603` via
   `refreshSubAgentModels`, `:1642-1653`) while keeping the large slot's
   `ModelCfg`. Matching that shape means `buildResolvedAgentModels(fetch,
   small, true)` returns exactly the pair we want, with no unused model
   construction, and refresh cannot silently change the model mid-run. In
   the unset case both slots are still the small model, so today's
   behaviour is unchanged.
3. **Provider config follows the fetch model.** `ProviderConfig`,
   `SystemPromptPrefix` and `promptTemplate.Build` must use the fetch model's
   provider. Leaving them on the small model's provider would send (say) an
   OpenAI prefix to an Anthropic model. The existing OAuth regression test
   already asserts the prefix comes from the model's own provider; the new
   test extends that to the override path.
4. **Invalid override falls back to small with a warning,** mirroring
   `buildAgentModels` (`coordinator.go:1167-1182`), which falls back to large
   for named agents. Falling back to the cheaper model is the safe direction.
5. **Resolution happens per call against the in-memory config.** The fetch
   sub-agent is built on every `agentic_fetch` invocation from
   `c.cfg.Config()` (`internal/config/store.go:141-151`). Edits to
   `anvil.json` take effect on the first call after Anvil reloads its config
   (or restarts); no extra refresh plumbing is added.
6. **Disabled providers are rejected for the fetch override only.**
   Disabled builtin providers remain in `Providers` with `Disable: true`
   (`internal/config/load_test.go:889-920`), and `ResolveAgentModel` does
   not check the flag. `ResolveAgenticFetchModel` rejects a disabled
   override provider (triggering the small-model fallback). The same latent
   gap in `ResolveAgentModel` for named agents is noted but not changed
   here, to avoid altering named-agent behaviour in this PR.
7. **Reasoning effort precedence** follows `ResolveAgentModel` exactly:
   with a `model` set, an unsupported `reasoning_effort` is dropped with a
   warning and the target model default is used; with `model` empty,
   `reasoning_effort` is applied to the small model verbatim and validated
   at call time like any other `SelectedModel`
   (`coordinator.go:455-469`). Explicit `provider_options` on the base
   model can still supersede effort at request build time
   (`coordinator.go:527-570`); this plan does not change that ordering.

**Success Criteria:**

- [ ] With `tools.agentic_fetch` unset, the fetch sub-agent requests use the
      `models.small` model ID and small provider prefix (existing
      `TestAgenticFetchAnthropicOAuth` passes unchanged).
- [ ] With `tools.agentic_fetch.model` set to a valid `provider/model`, the
      provider receives requests with that model ID, that provider's
      `SystemPromptPrefix`, and not the small provider's prefix.
- [ ] With an override that fails to resolve (bad format, unknown provider,
      disabled provider, unknown model), the tool succeeds using the small
      model and logs a warning.
- [ ] The fetch override survives credential refresh: after
      `refreshSubAgentModels` the sub-agent's large slot still uses the
      fetch model.
- [ ] `tools.agentic_fetch` merges across config files like other `tools.*`
      keys (global model + project reasoning effort both apply).
- [ ] `tools.agentic_fetch.reasoning_effort` overrides the resolved model's
      reasoning effort, and an unsupported value falls back to the model
      default (existing `ResolveAgentModel` semantics).
- [ ] All existing `TestResolveAgentModel_*` tests pass unchanged after the
      refactor.
- [ ] `anvil_info` prints `agentic_fetch = <model> (<provider>)`.
- [ ] `schema.json` contains `tools.agentic_fetch.model` and
      `reasoning_effort`.
- [ ] `go test ./...` and `task lint` pass.

## Context Loading

_Run before starting (paths relative to the worktree root):_

```bash
read internal/config/config.go          # lines 560-605 (Agent), 646-680 (Tools), 1072-1158 (ResolveAgentModel)
read internal/config/agent_model_test.go
read internal/agent/agentic_fetch_tool.go
read internal/agent/agentic_fetch_tool_test.go
read internal/agent/coordinator.go      # lines 1165-1260 (buildAgentModels, buildResolvedAgentModels), 1642-1660 (refreshSubAgentModels)
read internal/agent/tools/anvil_info.go # lines 96-110 (writeModels)
read internal/agent/tools/anvil_info_test.go
read internal/skills/builtin/anvil-config/SKILL.md
```

## Config Tasks

### Task 1: Add `tools.agentic_fetch` config and resolver

**Context:** `internal/config/config.go`, `internal/config/agent_model_test.go`

**Files:**

- Modify: `internal/config/config.go`
- Test: `internal/config/agent_model_test.go`

**Steps:**

1. [ ] Add the config struct next to `ToolGlob` and wire it into `Tools`:

   ```go
   type Tools struct {
   	Ls           ToolLs           `json:"ls,omitzero"`
   	Grep         ToolGrep         `json:"grep,omitzero"`
   	Glob         ToolGlob         `json:"glob,omitzero"`
   	AgenticFetch ToolAgenticFetch `json:"agentic_fetch,omitzero"`
   }

   // ToolAgenticFetch configures the sub-agent behind the agentic_fetch tool.
   type ToolAgenticFetch struct {
   	// Model is the provider/model the sub-agent runs on. Empty uses the
   	// global small model.
   	Model string `json:"model,omitempty" jsonschema:"description=Model for the agentic_fetch sub-agent in provider/model format. Empty uses the global small model,example=anthropic/claude-haiku-4-5"`
   	// ReasoningEffort overrides the reasoning effort of the resolved model.
   	ReasoningEffort string `json:"reasoning_effort,omitempty" jsonschema:"description=Reasoning effort for the agentic_fetch model. Unsupported values fall back to the model default,example=low"`
   }
   ```

2. [ ] Refactor `ResolveAgentModel` without changing its behaviour. Move
   everything after the base lookup into an unexported helper that takes the
   base explicitly:

   ```go
   func ResolveAgentModel(agent Agent, cfg *Config) (SelectedModel, error) {
   	base, ok := cfg.Models[SelectedModelTypeLarge]
   	if !ok {
   		return SelectedModel{}, fmt.Errorf("agent %q: no large model configured", agent.ID)
   	}
   	return resolveModelOverride(agent, base, cfg)
   }

   // resolveModelOverride layers agent's model, variant, reasoning effort and
   // think settings over base. See ResolveAgentModel for the inheritance
   // rules.
   func resolveModelOverride(agent Agent, base SelectedModel, cfg *Config) (SelectedModel, error) {
   	result := base
   	// ...existing body from `if agent.Model != "" {` to `return result, nil`,
   	// unchanged...
   }
   ```

   Keep the existing doc comment on `ResolveAgentModel`.

3. [ ] Add the fetch resolver directly below `ResolveAgentModel`:

   ```go
   // AgenticFetchAgentID identifies the agentic_fetch sub-agent in model
   // resolution errors and logs.
   const AgenticFetchAgentID = "agentic_fetch"

   // ResolveAgenticFetchModel resolves the SelectedModel for the agentic_fetch
   // sub-agent. The global small model is the base; tools.agentic_fetch is
   // layered over it using the same rules as ResolveAgentModel.
   func ResolveAgenticFetchModel(cfg *Config) (SelectedModel, error) {
   	base, ok := cfg.Models[SelectedModelTypeSmall]
   	if !ok {
   		return SelectedModel{}, fmt.Errorf("agent %q: no small model configured", AgenticFetchAgentID)
   	}
   	fetch := cfg.Tools.AgenticFetch
   	return resolveModelOverride(Agent{
   		ID:              AgenticFetchAgentID,
   		Model:           fetch.Model,
   		ReasoningEffort: fetch.ReasoningEffort,
   	}, base, cfg)
   }
   ```

   Before delegating, reject disabled override providers so they take the
   fallback path:

   ```go
   	if fetch.Model != "" {
   		if slash := strings.IndexByte(fetch.Model, '/'); slash > 0 {
   			if p, ok := cfg.Providers.Get(fetch.Model[:slash]); ok && p.Disable {
   				return SelectedModel{}, fmt.Errorf("agent %q: provider %q is disabled", AgenticFetchAgentID, fetch.Model[:slash])
   			}
   		}
   	}
   ```

   Check `config.go:894` (the `"agentic_fetch"` tool name literal in the
   tool list); leave it as is, the new constant is only for resolution.

4. [ ] Add tests to `internal/config/agent_model_test.go`, reusing
   `resolveAgentModelConfig` and adding the small model to its `Models` map
   inside each test (`cfg.Models[SelectedModelTypeSmall] = ...`). All tests
   `t.Parallel()`, `require` only:
   - `TestResolveAgenticFetchModel_UnsetReturnsGlobalSmall`: small is
     `{Provider: "anthropic", Model: "tiny", Temperature: ptr(0.3)}`, no
     override; result equals small exactly.
   - `TestResolveAgenticFetchModel_MissingSmallModelErrors`: only large set;
     error mentions `no small model configured`.
   - `TestResolveAgenticFetchModel_OverrideLayersOverSmall`: small is
     `anthropic/tiny` with `Temperature: ptr(0.3)`, override
     `anthropic/big`; result has `Model: "big"`, `MaxTokens: 128000`,
     `ReasoningEffort: "high"`, `Temperature` 0.3 (inherited from small, not
     large — set large's temperature to a different value to prove it).
   - `TestResolveAgenticFetchModel_CrossProviderDropsOptions`: small has
     `ProviderOptions` set, override `openai/gpt`; `ProviderOptions` is nil.
   - `TestResolveAgenticFetchModel_ReasoningEffortOverride`: override
     `anthropic/big` with `ReasoningEffort: "low"` gives `"low"`; with
     `"extreme"` gives the model default `"high"`.
   - `TestResolveAgenticFetchModel_ReasoningOnlyAppliesToSmall`: no
     `model`, `ReasoningEffort: "low"`; result is small with
     `ReasoningEffort: "low"`.
   - `TestResolveAgenticFetchModel_Errors`: table of `"no-slash"`,
     `"missing/big"`, `"anthropic/missing"`, and `"openai/gpt"` with the
     `openai` provider set to `Disable: true`; each errors and the message
     contains `agent "agentic_fetch"`.

5. [ ] Add `TestLoadFromBytes_AgenticFetchMerge` to
   `internal/config/load_test.go`: call `loadFromBytes` with a global
   `{"tools":{"agentic_fetch":{"model":"anthropic/big"},"grep":{"timeout":"10s"}}}`
   and a project `{"tools":{"agentic_fetch":{"reasoning_effort":"low"}}}`.
   Assert `Tools.AgenticFetch.Model == "anthropic/big"`,
   `Tools.AgenticFetch.ReasoningEffort == "low"`, and the grep timeout
   survived. Add a third input `{"tools":{"agentic_fetch":{"model":""}}}`
   and assert the model is reset to empty (documents how a project can opt
   back into the small model).

**Verify:**

```bash
go test ./internal/config/ -run 'TestResolveAgentModel|TestResolveAgenticFetchModel|TestApplyOverrides' -v 2>&1 | tail -30
# Expected: all PASS, including every pre-existing TestResolveAgentModel_* test.
gofumpt -l internal/config/
# Expected: no output.
```

## Agent Wiring Tasks

_Depends on Task 1._

### Task 2: Run the fetch sub-agent on the resolved model

**Context:** `internal/agent/agentic_fetch_tool.go`,
`internal/agent/coordinator.go`, `internal/agent/agentic_fetch_tool_test.go`

**Files:**

- Modify: `internal/agent/coordinator.go` (new helper after
  `buildAgentModels`)
- Modify: `internal/agent/agentic_fetch_tool.go`
- Test: `internal/agent/agentic_fetch_tool_test.go`

**Steps:**

1. [ ] Add to `coordinator.go`, directly after `buildAgentModels`:

   ```go
   // buildAgenticFetchModel builds the (fetch, small) model pair for the
   // agentic_fetch sub-agent. tools.agentic_fetch.model wins when it
   // resolves; otherwise the global small model is used, so a bad override
   // degrades instead of failing.
   func (c *coordinator) buildAgenticFetchModel(ctx context.Context) (Model, Model, error) {
   	cfg := c.cfg.Config()
   	smallModelCfg, ok := cfg.Models[config.SelectedModelTypeSmall]
   	if !ok {
   		return Model{}, Model{}, errSmallModelNotSelected
   	}
   	fetchModelCfg, err := config.ResolveAgenticFetchModel(cfg)
   	if err != nil {
   		slog.Warn("Failed to resolve agentic_fetch model; falling back to the global small model",
   			"configured_model", cfg.Tools.AgenticFetch.Model,
   			"error", err,
   		)
   		fetchModelCfg = smallModelCfg
   	}
   	return c.buildResolvedAgentModels(ctx, fetchModelCfg, smallModelCfg, true)
   }
   ```

   `isSubAgent` is `true`, matching
   how the small model is built today (`coordinator.go:1208`), so
   OAuth/provider construction is unchanged for the unset case.
   `buildResolvedAgentModels` already applies OpenRouter `:exacto` and
   `FlatRate` to both slots, so no extra handling is needed.

2. [ ] In `agentic_fetch_tool.go`, replace lines 151-165 and the
   `NewSessionAgent` model/provider fields so everything keys off the fetch
   model:

   ```go
   fetchModel, smallModel, err := c.buildAgenticFetchModel(ctx)
   if err != nil {
   	return fantasy.ToolResponse{}, fmt.Errorf("error building models: %w", err)
   }

   systemPrompt, err := promptTemplate.Build(ctx, fetchModel.Model.Provider(), fetchModel.Model.Model(), c.cfg)
   if err != nil {
   	return fantasy.ToolResponse{}, fmt.Errorf("error building system prompt: %w", err)
   }

   fetchProviderCfg, ok := c.cfg.Config().Providers.Get(fetchModel.ModelCfg.Provider)
   if !ok {
   	return fantasy.ToolResponse{}, errors.New("agentic_fetch model provider not configured")
   }
   ```

   and in `SessionAgentOptions`:

   ```go
   LargeModel:         fetchModel,
   SmallModel:         smallModel,
   ProviderConfig:     fetchProviderCfg,
   SystemPromptPrefix: fetchProviderCfg.SystemPromptPrefix,
   ```

   Drop the now-stale `// Use small model for both...` and
   `// Use a sub-agent config (depth < 3)...` comments. Grep the file
   afterwards for `small` to confirm no stale references remain.

3. [ ] Add `TestAgenticFetchModelSelection` to
   `agentic_fetch_tool_test.go`. Table-driven and **not parallel**: mirror
   the OAuth test's environment isolation at the top of each subtest
   (`t.Setenv("ANTHROPIC_API_KEY", "dummy-env-key")`,
   `t.Setenv("ANTHROPIC_AUTH_TOKEN", "")`) so ambient credentials cannot
   leak into provider construction. Each case builds its own
   `httptest.Server` that:
   - accepts `POST /v1/messages`, decodes `{"model","system":[{"text"}]}`,
     and sends the decoded body on a buffered channel;
   - replies with a single streamed text turn ending `end_turn` (copy the
     `emit` sequence from `TestAgenticFetchAnthropicOAuth`, the `n != 1`
     branch only), text `"Fetch model selection complete."`.

   Providers (all `catwalk.TypeAnthropic`, `APIKey: "dummy-api-key"`,
   `BaseURL: server.URL`):
   - `small-anthropic`: model `claude-haiku-4-5`, prefix
     `"Small provider prefix sentinel."`
   - `fetch-anthropic`: model `claude-sonnet-4-5`, prefix
     `"Fetch provider prefix sentinel."`

   `Models`: large `small-anthropic/claude-haiku-4-5` (large is irrelevant;
   reuse to avoid a third provider), small `small-anthropic/claude-haiku-4-5`.

   Cases (`Tools.AgenticFetch.Model` → expected request model, expected
   prefix, forbidden prefix):
   - `""` → `claude-haiku-4-5`, small prefix, fetch prefix.
   - `"fetch-anthropic/claude-sonnet-4-5"` → `claude-sonnet-4-5`, fetch
     prefix, small prefix.
   - `"fetch-anthropic/does-not-exist"` → `claude-haiku-4-5`, small prefix,
     fetch prefix.
   - `"no-slash"` → `claude-haiku-4-5`, small prefix, fetch prefix.
   - `"fetch-anthropic/claude-sonnet-4-5"` with `fetch-anthropic` set to
     `Disable: true` → `claude-haiku-4-5`, small prefix, fetch prefix.
   - `"fetch-anthropic/claude-sonnet-4-5"` with `fetch-anthropic` using
     OAuth (`APIKey: "Bearer dummy-oauth-token"`, `OAuthToken` as in the
     OAuth test) → `claude-sonnet-4-5`, and the server asserts
     `Authorization: Bearer dummy-oauth-token`. Run this case under both
     `SystemModeA` and `SystemModeB` (`t.Setenv(SystemModeEnvVar, mode)`)
     and check the fetch prefix appears exactly once across system **and**
     user text combined, since mode B moves the prefix into the user turn
     (see the OAuth test's lines 175-203).

   For the fallback cases, capture logs by swapping `slog.Default()` for a
   handler writing to a `bytes.Buffer` for the subtest (restore with
   `t.Cleanup`) and assert the buffer contains
   `Failed to resolve agentic_fetch model`. This is another reason the test
   must not be parallel.

   Build the coordinator, sessions, messages, parent session and context
   values exactly as `TestAgenticFetchAnthropicOAuth` does (lines 108-152),
   run the tool with `{"prompt":"Find the model selection sentinel."}`, then
   assert: no error, `!result.IsError`, content equals the final text,
   exactly one request, `body.Model` matches, joined system text contains
   the expected prefix exactly once and does not contain the forbidden one.

   If the setup duplication with the OAuth test exceeds ~40 lines, extract a
   `newAgenticFetchTestCoordinator(t, cfg *config.Config) (*coordinator, context.Context)`
   helper and use it from the new test only. Do not rewrite the OAuth test.

4. [ ] Add `TestRefreshSubAgentModelsKeepsAgenticFetchModel` in
   `internal/agent/coordinator_test.go` (or the fetch test file if that is
   where the helper lives): build the fetch/small pair with
   `buildAgenticFetchModel` for a config with the override set, create a
   `SessionAgent` with `LargeModel: fetch, SmallModel: small`, call
   `c.refreshSubAgentModels(ctx, agent)`, and assert
   `agent.Model().ModelCfg.Model` is still the fetch model ID. No provider
   requests are made, so no server is needed; any valid dummy API key
   works because `LanguageModel` construction does not dial.

**Verify:**

```bash
go test ./internal/agent/ -run 'TestAgenticFetch' -v 2>&1 | tail -30
# Expected: TestAgenticFetchAnthropicOAuth (4 subtests) and all
# TestAgenticFetchModelSelection subtests PASS.
go test ./internal/agent/ -run 'TestRefreshSubAgentModelsKeepsAgenticFetchModel' -v 2>&1 | tail -5
# Expected: PASS.
go test ./internal/agent/ 2>&1 | tail -5
# Expected: ok. Cassette tests are unaffected: no prompt or tool schema text changes.
gofumpt -l internal/agent/
# Expected: no output.
```

### Task 3: Surface the fetch model and document it

**Context:** `internal/agent/tools/anvil_info.go`,
`internal/agent/tools/anvil_info_test.go`,
`internal/skills/builtin/anvil-config/SKILL.md`, `schema.json`

**Files:**

- Modify: `internal/agent/tools/anvil_info.go` (`writeModels`)
- Test: `internal/agent/tools/anvil_info_test.go`
- Modify: `internal/skills/builtin/anvil-config/SKILL.md`
- Regenerate: `schema.json`

**Steps:**

1. [ ] In `writeModels`, after the large/small loop and before the trailing
   blank line, print the fetch model. Only when a small model exists (the
   resolver's base):

   ```go
   if _, ok := c.Models[config.SelectedModelTypeSmall]; ok {
   	if m, err := config.ResolveAgenticFetchModel(c); err == nil {
   		fmt.Fprintf(b, "agentic_fetch = %s (%s)\n", m.Model, m.Provider)
   	} else {
   		small := c.Models[config.SelectedModelTypeSmall]
   		fmt.Fprintf(b, "agentic_fetch = %s (%s) [invalid override %q, using small]\n", small.Model, small.Provider, c.Tools.AgenticFetch.Model)
   	}
   }
   ```

2. [ ] Extend `TestAnvilInfo_Models` to assert
   `agentic_fetch = claude-haiku-3-20250307 (anthropic)` (unset case). Add
   `TestAnvilInfo_AgenticFetchModelOverride` with an `anthropic` provider
   whose `Models` contains `{ID: "claude-opus-4"}`, override
   `anthropic/claude-opus-4`, asserting `agentic_fetch = claude-opus-4 (anthropic)`;
   and a case with override `"bogus"` asserting the output contains
   `[invalid override "bogus", using small]`.

3. [ ] In `internal/skills/builtin/anvil-config/SKILL.md`, after the
   `models` section (around line 108, the "`large` is the primary coding
   model; `small` is for summarization." line), add:

   ````markdown
   The `agentic_fetch` tool runs a web-research sub-agent on the `small`
   model by default. To give it its own model, set `tools.agentic_fetch`:

   ```json
   {
     "tools": {
       "agentic_fetch": {
         "model": "anthropic/claude-haiku-4-5",
         "reasoning_effort": "low"
       }
     }
   }
   ```

   `model` uses `provider/model` format. An invalid value logs a warning and
   falls back to `small`. Check the resolved model with `anvil_info`.
   ````

4. [ ] Regenerate the schema: `task schema`. Confirm the diff is limited
   to the new `ToolAgenticFetch` definition and its reference under
   `Tools`.

**Verify:**

```bash
go test ./internal/agent/tools/ -run 'TestAnvilInfo' -v 2>&1 | tail -15
# Expected: all PASS.
task schema && git diff --stat schema.json && rg -n '"agentic_fetch"|ToolAgenticFetch' schema.json
# Expected: schema.json changed; new property and definition present.
go test ./internal/skills/... 2>&1 | tail -3
# Expected: ok (builtin skill embedding still valid).
```

## Final Verification

```bash
gofumpt -w . && task lint
go test ./... 2>&1 | grep -Ev '^ok|no test files'
# Expected: no output (baseline on b50540498 was 50 packages ok, 0 failures).
```

Manual check (optional): add `tools.agentic_fetch.model` to a local
`anvil.json`, run `go run .`, ask for `anvil_info`, confirm the
`agentic_fetch = ...` line, then trigger an `agentic_fetch` and confirm the
sub-session in the TUI drill-in used the configured model.

<!-- Review notes (devils-advocate, 2026-09-30):
- Caught that disabled builtin providers stay in `Providers` with
  `Disable: true`, so the resolver would have accepted them. Added a
  fetch-only disabled check plus tests; the same gap in `ResolveAgentModel`
  for named agents is recorded as out of scope.
- Caught that `refreshSubAgentModels` resets the small slot to global small
  on credential refresh. Changed the design so the fetch model owns the
  large slot and global small keeps the small slot, and added a refresh
  regression test.
- Corrected the claim that config edits apply on the next call (they apply
  after config reload), narrowed "invalid falls back" to resolution errors,
  documented reasoning-effort precedence, and switched new error wrapping
  to `%w`.
- Added config-merge, reasoning-only, OAuth-in-both-prompt-modes and
  warning-log assertions; made the new tool test non-parallel for env and
  slog isolation.
- Confirmed single-file (not phased) is right: one cohesive feature. No
  cassette or golden updates expected.
-->
