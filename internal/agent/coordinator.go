package agent

import (
	"bytes"
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/notify"
	"github.com/Broderick-Westrope/anvil/internal/agent/prompt"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	toolsmcp "github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/discover"
	"github.com/Broderick-Westrope/anvil/internal/filetracker"
	"github.com/Broderick-Westrope/anvil/internal/home"
	"github.com/Broderick-Westrope/anvil/internal/hooks"
	"github.com/Broderick-Westrope/anvil/internal/lsp"
	"github.com/Broderick-Westrope/anvil/internal/message"
	anthropicoauth "github.com/Broderick-Westrope/anvil/internal/oauth/anthropic"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/plugin"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"golang.org/x/sync/errgroup"

	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/azure"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
	"github.com/qjebbs/go-jsons"
)

//go:embed all:templates/agents
var agentMDFS embed.FS

// Coordinator errors.
var (
	errOrchestratorAgentNotConfigured  = errors.New("orchestrator agent not configured")
	errModelProviderNotConfigured      = errors.New("model provider not configured")
	errLargeModelNotSelected           = errors.New("large model not selected")
	errSmallModelNotSelected           = errors.New("small model not selected")
	errLargeModelProviderNotConfigured = errors.New("large model provider not configured")
	errSmallModelProviderNotConfigured = errors.New("small model provider not configured")
	errLargeModelNotFound              = errors.New("large model not found in provider config")
	errSmallModelNotFound              = errors.New("small model not found in provider config")
)

type Coordinator interface {
	// INFO: (kujtim) this is not used yet we will use this when we have multiple agents
	// SetMainAgent(string)
	Run(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error)
	Cancel(sessionID string)
	CancelAll()
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	QueuedPrompts(sessionID string) int
	QueuedPromptsList(sessionID string) []string
	ClearQueue(sessionID string)
	Summarize(context.Context, string) error
	RegenerateTitle(ctx context.Context, sessionID string) error
	Model() Model
	UpdateModels(ctx context.Context) error
	ReloadPlugins(ctx context.Context) error
	SkillStates() []*skills.SkillState
	// ActiveSkillByName returns the active skill with the given name, or nil
	// if not found.
	ActiveSkillByName(name string) *skills.Skill
}

type coordinator struct {
	cfg         *config.ConfigStore
	sessions    session.Service
	messages    message.Service
	permissions permission.Service
	filetracker filetracker.Service
	lspManager  *lsp.Manager
	notify      pubsub.Publisher[notify.Notification]

	// orchestrator is the eagerly-built top-level agent. Protected by orchestratorMu.
	// Do NOT use csync.Value[SessionAgent] — it panics on interface types backed by pointers.
	orchestrator   SessionAgent
	orchestratorMu sync.RWMutex

	// agents is a lazy map of named sub-agents, populated on first delegation.
	agents *csync.Map[string, SessionAgent]

	// agentBuildMu serialises lazy agent construction to prevent duplicate
	// builds when two goroutines race on the same agent name.
	agentBuildMu sync.Mutex

	// agentConfigs holds per-agent config loaded from cfg at init.
	agentConfigs map[string]config.Agent

	// agentMDs holds parsed agent .md description files, keyed by agent name.
	agentMDs map[string]prompt.AgentMD

	// Skills discovery results (session-start snapshot).
	allSkills    []*skills.Skill      // Pre-filter: all discovered after dedup.
	activeSkills []*skills.Skill      // Post-filter: active skills only.
	skillStates  []*skills.SkillState // Combined builtin + user states.
	skillTracker *skills.Tracker

	// plugins holds the discovered plugins so their skills paths can be
	// included in the View tool's auto-approval list. Protected by orchestratorMu.
	plugins []*plugin.Plugin
}

func NewCoordinator(
	ctx context.Context,
	cfg *config.ConfigStore,
	sessions session.Service,
	messages message.Service,
	permissions permission.Service,
	filetracker filetracker.Service,
	lspManager *lsp.Manager,
	notify pubsub.Publisher[notify.Notification],
) (Coordinator, error) {
	// Discover plugins once for both skills and agents.
	plugins := plugin.DiscoverAll(cfg.Config().Plugins)
	// Discover skills once at session start.
	allSkills, activeSkills, skillStates := discoverSkills(cfg, plugins)
	skillTracker := skills.NewTracker(activeSkills)

	c := &coordinator{
		cfg:          cfg,
		sessions:     sessions,
		messages:     messages,
		permissions:  permissions,
		filetracker:  filetracker,
		lspManager:   lspManager,
		notify:       notify,
		allSkills:    allSkills,
		activeSkills: activeSkills,
		skillStates:  skillStates,
		skillTracker: skillTracker,
		plugins:      plugins,
		agents:       csync.NewMap[string, SessionAgent](),
	}

	// Enable MCP OAuth tool-name rename when the Anthropic provider uses
	// OAuth credentials. This must happen once during coordinator
	// initialisation, before any tools are registered.
	for providerCfg := range cfg.Config().Providers.Seq() {
		if providerCfg.ID == string(catwalk.InferenceProviderAnthropic) && providerCfg.OAuthToken != nil {
			toolsmcp.SetOAuthRename(true)
			break
		}
	}

	agentMDs, err := discoverAgentMDs(agentMDFS, plugins)
	if err != nil {
		return nil, fmt.Errorf("loading agent descriptions: %w", err)
	}
	c.agentMDs = agentMDs

	if len(agentMDs) == 0 {
		slog.Warn("No specialist agents discovered; the orchestrator will handle all tasks directly. Configure plugins to provide agent definitions.")
	}

	// Convert AgentMD capability fields to config.Agent defaults and re-setup
	// the agent roster, replacing the hardcoded non-orchestrator defaults set
	// during config loading with values sourced from the .md frontmatter.
	mdDefaults := make(map[string]config.Agent, len(agentMDs))
	for name, md := range agentMDs {
		mdDefaults[name] = agentConfigFromMD(name, md)
	}
	// Load all agent configs from the computed defaults + user overrides.
	c.agentConfigs = cfg.Config().SetupAgentsWithDefaults(mdDefaults)

	// Validate delegates_to references. Warn on disabled refs, error on missing.
	agentMDSlice := make([]prompt.AgentMD, 0, len(agentMDs))
	for _, md := range agentMDs {
		agentMDSlice = append(agentMDSlice, md)
	}
	errs, warnings := prompt.ValidateDelegatesTo(agentMDSlice, cfg.Config().DisabledAgents)
	for _, w := range warnings {
		slog.Warn("Agent delegation warning", "error", w)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("agent delegates_to validation failed: %w", errors.Join(errs...))
	}

	// Build the orchestrator eagerly at depth=3.
	orchestratorCfg, ok := c.agentConfigs[config.AgentOrchestrator]
	if !ok {
		return nil, errOrchestratorAgentNotConfigured
	}
	// Pass waitForMCP=false: coordinator.Run already waits for MCP init and
	// rebuilds the tool list via UpdateModels before the first turn, so
	// blocking here at startup is redundant and adds significant latency.
	orchestrator, err := c.buildAgent(ctx, config.AgentOrchestrator, orchestratorCfg, 3, false)
	if err != nil {
		return nil, err
	}
	c.orchestratorMu.Lock()
	c.orchestrator = orchestrator
	c.orchestratorMu.Unlock()

	return c, nil
}

// agentConfigFromMD converts the capability and model fields of a parsed
// agent .md file into a config.Agent default. Behavioural frontmatter (role,
// delegate_when, routing_hint) is consumed by the prompt builder instead.
func agentConfigFromMD(name string, md prompt.AgentMD) config.Agent {
	return config.Agent{
		ID:              name,
		Name:            agentIDToName(name),
		AllowedTools:    md.Tools,
		AllowedSkills:   md.Skills,
		AllowedMCP:      md.MCPs,
		Model:           md.Model,
		ReasoningEffort: md.ReasoningEffort,
		Think:           md.Think,
	}
}

// agentIDToName converts an agent ID (e.g. "devils-advocate") to a
// human-readable name (e.g. "Devils Advocate") by replacing hyphens with
// spaces and title-casing each word.
func agentIDToName(id string) string {
	words := strings.Fields(strings.ReplaceAll(id, "-", " "))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// loadAgentMDsFromDir reads *.md files from a filesystem directory (non-
// recursive, unlike loadAgentMDs which uses fs.WalkDir) and parses each one
// using prompt.ParseAgentMD. Used for plugin agent discovery. Subdirectories
// are intentionally ignored; plugin agents must be at the top level.
func loadAgentMDsFromDir(dir string) (map[string]prompt.AgentMD, error) {
	result := make(map[string]prompt.AgentMD)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading agent directory %q: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			slog.Warn("Failed to read plugin agent file",
				"path", filepath.Join(dir, entry.Name()), "error", err)
			continue
		}
		md, err := prompt.ParseAgentMD(name, content)
		if err != nil {
			slog.Warn("Failed to parse plugin agent file",
				"path", filepath.Join(dir, entry.Name()), "error", err)
			continue
		}
		result[name] = md
	}
	return result, nil
}

// loadAgentMDs reads all *.md files from the given filesystem and parses
// each one using prompt.ParseAgentMD. The returned map is keyed by agent
// name (filename without extension).
func loadAgentMDs(fsys fs.FS) (map[string]prompt.AgentMD, error) {
	result := make(map[string]prompt.AgentMD)
	err := fs.WalkDir(fsys, "templates/agents", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		content, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}
		// Derive agent name from filename (strip directory and .md suffix).
		base := pathpkg.Base(path)
		name := strings.TrimSuffix(base, ".md")
		md, parseErr := prompt.ParseAgentMD(name, content)
		if parseErr != nil {
			return fmt.Errorf("parsing %s: %w", path, parseErr)
		}
		result[name] = md
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// discoverAgentMDs loads built-in agent definitions from the embedded
// filesystem and overlays plugin-provided definitions in reverse priority
// order (earlier-configured plugins win on name collisions).
func discoverAgentMDs(builtinFS fs.FS, plugins []*plugin.Plugin) (map[string]prompt.AgentMD, error) {
	agentMDs, err := loadAgentMDs(builtinFS)
	if err != nil {
		return nil, err
	}
	for i := len(plugins) - 1; i >= 0; i-- {
		p := plugins[i]
		if p.AgentsPath == "" {
			continue
		}
		pluginAgentMDs, loadErr := loadAgentMDsFromDir(p.AgentsPath)
		if loadErr != nil {
			slog.Warn("Failed to load plugin agents",
				"plugin", p.Name, "path", p.AgentsPath, "error", loadErr)
			continue
		}
		for name, md := range pluginAgentMDs {
			if existing, ok := agentMDs[name]; ok {
				existingSource := existing.Source
				if existingSource == "" {
					existingSource = "builtin"
				}
				slog.Warn("Plugin agent overrides existing agent",
					"plugin", p.Name, "agent", name,
					"existingSource", existingSource)
			}
			md.Source = "plugin:" + p.Name
			agentMDs[name] = md
		}
	}
	return agentMDs, nil
}

// Run implements Coordinator.
func (c *coordinator) Run(ctx context.Context, sessionID string, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	// Wait for MCP initialization to complete before building the tool list.
	// Without this, slow-to-start MCP servers (e.g. stdio Python via uv) may
	// not have registered their tools yet when buildTools reads the registry,
	// so their tools silently never appear in the LLM tool palette — even
	// though anvil_info reports them as connected.
	if err := toolsmcp.WaitForInit(ctx); err != nil {
		return nil, fmt.Errorf("failed to wait for MCP initialization: %w", err)
	}

	// refresh models before each run
	if err := c.UpdateModels(ctx); err != nil {
		return nil, fmt.Errorf("failed to update models: %w", err)
	}

	orch := c.getOrchestrator()
	model := orch.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}

	if !model.CatwalkCfg.SupportsImages && attachments != nil {
		// filter out image attachments
		filteredAttachments := make([]message.Attachment, 0, len(attachments))
		for _, att := range attachments {
			if att.IsText() {
				filteredAttachments = append(filteredAttachments, att)
			}
		}
		attachments = filteredAttachments
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return nil, errModelProviderNotConfigured
	}

	mergedOptions, temp, topP, topK, freqPenalty, presPenalty := mergeCallOptions(model, providerCfg)

	if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
		// NOTE(@andreynering): We don't return here because the event handling to ask the user to reauthenticate
		// depends on the flow below. If refresh fails, proceed with the token we have.
		slog.Error("Failed to refresh OAuth2 token. Proceeding with existing token.", "error", err)
	}

	var messageCreated bool
	run := func() (*fantasy.AgentResult, error) {
		result, err := orch.Run(ctx, SessionAgentCall{
			SessionID:         sessionID,
			Prompt:            prompt,
			Attachments:       attachments,
			MaxOutputTokens:   maxTokens,
			ProviderOptions:   mergedOptions,
			Temperature:       temp,
			TopP:              topP,
			TopK:              topK,
			FrequencyPenalty:  freqPenalty,
			PresencePenalty:   presPenalty,
			skipCreateMessage: messageCreated,
			OnAuthRefresh:     c.makeAuthRefreshCallback(providerCfg),
		})
		// Safe to set unconditionally: isUnauthorized only matches
		// 401 ProviderErrors which occur after createUserMessage.
		messageCreated = true
		return result, err
	}
	// Snapshot skill state under lock to avoid a data race with
	// ReloadPlugins which writes these fields under orchestratorMu.
	c.orchestratorMu.RLock()
	activeSkillsSnap := slices.Clone(c.activeSkills)
	skillTrackerSnap := c.skillTracker
	c.orchestratorMu.RUnlock()

	beforeLoaded := skillTrackerSnap.LoadedNames()
	result, originalErr := run()
	logTurnSkillUsage(sessionID, prompt, activeSkillsSnap, skillTrackerSnap, beforeLoaded)

	if c.isUnauthorized(originalErr) {
		if err := c.retryAfterUnauthorized(ctx, providerCfg); err == nil {
			return run()
		}
	}

	return result, originalErr
}

// getOrchestrator returns the orchestrator session agent, reading under lock.
func (c *coordinator) getOrchestrator() SessionAgent {
	c.orchestratorMu.RLock()
	defer c.orchestratorMu.RUnlock()
	return c.orchestrator
}

// effectiveReasoningEffort returns the reasoning effort to apply for
// provider calls. It prefers the user-selected effort when valid,
// otherwise the model default when valid, and finally falls back to the
// first configured reasoning level.
func effectiveReasoningEffort(model Model) string {
	if !model.CatwalkCfg.CanReason {
		return ""
	}

	if effort := model.ModelCfg.ReasoningEffort; effort != "" && slices.Contains(model.CatwalkCfg.ReasoningLevels, effort) {
		return effort
	}
	if effort := model.CatwalkCfg.DefaultReasoningEffort; effort != "" && slices.Contains(model.CatwalkCfg.ReasoningLevels, effort) {
		return effort
	}
	if len(model.CatwalkCfg.ReasoningLevels) > 0 {
		return model.CatwalkCfg.ReasoningLevels[0]
	}
	return ""
}

func getProviderOptions(model Model, providerCfg config.ProviderConfig) fantasy.ProviderOptions {
	options := fantasy.ProviderOptions{}

	cfgOpts := []byte("{}")
	providerCfgOpts := []byte("{}")
	catwalkOpts := []byte("{}")

	if model.ModelCfg.ProviderOptions != nil {
		data, err := json.Marshal(model.ModelCfg.ProviderOptions)
		if err == nil {
			cfgOpts = data
		}
	}

	if providerCfg.ProviderOptions != nil {
		data, err := json.Marshal(providerCfg.ProviderOptions)
		if err == nil {
			providerCfgOpts = data
		}
	}

	if model.CatwalkCfg.Options.ProviderOptions != nil {
		data, err := json.Marshal(model.CatwalkCfg.Options.ProviderOptions)
		if err == nil {
			catwalkOpts = data
		}
	}

	readers := []io.Reader{
		bytes.NewReader(catwalkOpts),
		bytes.NewReader(providerCfgOpts),
		bytes.NewReader(cfgOpts),
	}

	got, err := jsons.Merge(readers)
	if err != nil {
		slog.Error("Could not merge call config", "err", err)
		return options
	}

	mergedOptions := make(map[string]any)

	err = json.Unmarshal([]byte(got), &mergedOptions)
	if err != nil {
		slog.Error("Could not create config for call", "err", err)
		return options
	}

	reasoningEffort := effectiveReasoningEffort(model)
	shouldSetEffort := model.CatwalkCfg.CanReason &&
		reasoningEffort != "" &&
		slices.Contains(model.CatwalkCfg.ReasoningLevels, reasoningEffort)

	switch providerCfg.Type {
	case openai.Name, azure.Name:
		_, hasReasoningEffort := mergedOptions["reasoning_effort"]
		if !hasReasoningEffort && shouldSetEffort {
			mergedOptions["reasoning_effort"] = reasoningEffort
		}
		if openai.IsResponsesModel(model.CatwalkCfg.ID) {
			if openai.IsResponsesReasoningModel(model.CatwalkCfg.ID) {
				mergedOptions["reasoning_summary"] = "auto"
				mergedOptions["include"] = []openai.IncludeType{openai.IncludeReasoningEncryptedContent}
			}
			parsed, err := openai.ParseResponsesOptions(mergedOptions)
			if err == nil {
				options[openai.Name] = parsed
			}
		} else {
			parsed, err := openai.ParseOptions(mergedOptions)
			if err == nil {
				options[openai.Name] = parsed
			}
		}
	case anthropic.Name, bedrock.Name:
		var (
			_, hasEffort = mergedOptions["effort"]
			_, hasThink  = mergedOptions["thinking"]
			extraBody    = make(map[string]any)
		)

		switch providerCfg.ID {
		case string(catwalk.InferenceProviderAlibabaSingapore), string(catwalk.InferenceProviderAlibabaUS):
			switch {
			case !hasEffort && shouldSetEffort:
				extraBody["reasoning_effort"] = reasoningEffort
			case !hasThink && model.CatwalkCfg.CanReason:
				if model.ModelCfg.Think {
					extraBody["thinking"] = map[string]any{"type": "enabled"}
				} else {
					extraBody["thinking"] = map[string]any{"type": "disabled"}
				}
			}
			mergedOptions["extra_body"] = extraBody

		default:
			switch {
			case !hasEffort && shouldSetEffort:
				mergedOptions["effort"] = reasoningEffort
			case !hasThink && model.ModelCfg.Think:
				mergedOptions["thinking"] = map[string]any{"budget_tokens": 2000}
			}
		}

		parsed, err := anthropic.ParseOptions(mergedOptions)
		if err == nil {
			options[anthropic.Name] = parsed
		}

	case openrouter.Name:
		_, hasReasoning := mergedOptions["reasoning"]
		if !hasReasoning && shouldSetEffort {
			mergedOptions["reasoning"] = map[string]any{
				"enabled": true,
				"effort":  reasoningEffort,
			}
		}
		parsed, err := openrouter.ParseOptions(mergedOptions)
		if err == nil {
			options[openrouter.Name] = parsed
		}
	case vercel.Name:
		_, hasReasoning := mergedOptions["reasoning"]
		if !hasReasoning && shouldSetEffort {
			mergedOptions["reasoning"] = map[string]any{
				"enabled": true,
				"effort":  reasoningEffort,
			}
		}
		parsed, err := vercel.ParseOptions(mergedOptions)
		if err == nil {
			options[vercel.Name] = parsed
		}
	case google.Name:
		_, hasReasoning := mergedOptions["thinking_config"]
		if !hasReasoning {
			if strings.HasPrefix(model.CatwalkCfg.ID, "gemini-2") {
				mergedOptions["thinking_config"] = map[string]any{
					"thinking_budget":  2000,
					"include_thoughts": true,
				}
			} else {
				mergedOptions["thinking_config"] = map[string]any{
					"thinking_level":   reasoningEffort,
					"include_thoughts": true,
				}
			}
		}
		parsed, err := google.ParseOptions(mergedOptions)
		if err == nil {
			options[google.Name] = parsed
		}
	case openaicompat.Name:
		extraBody := make(map[string]any)

		_, hasReasoningEffort := mergedOptions["reasoning_effort"]
		if !hasReasoningEffort && shouldSetEffort {
			switch providerCfg.ID {
			case string(catwalk.InferenceProviderIoNet):
				extraBody["reasoning"] = map[string]string{"effort": reasoningEffort}
			case string(catwalk.InferenceProviderOpenCodeGo), string(catwalk.InferenceProviderOpenCodeZen):
				// MiniMax models use the "thinking" parameter instead of
				// "reasoning_effort". Other models on these providers still
				// use the standard field.
				if !strings.HasPrefix(strings.ToLower(model.CatwalkCfg.ID), "minimax") {
					mergedOptions["reasoning_effort"] = reasoningEffort
				}
			default:
				mergedOptions["reasoning_effort"] = reasoningEffort
			}
		}

		// "reasoning effort" is a standard OpenAI field, but "thinking" is not.
		// Setting it in the right way for each provider.
		// TODO: Abstract this in Fantasy somehow?
		// TODO: Allow custom providers to specify how to set this?
		switch providerCfg.ID {
		case string(catwalk.InferenceProviderIoNet):
			if _, ok := extraBody["reasoning"]; !ok && model.CatwalkCfg.CanReason {
				if model.ModelCfg.Think {
					extraBody["reasoning"] = map[string]string{"effort": "medium"}
				} else {
					extraBody["reasoning"] = map[string]string{"effort": "none"}
				}
			}
		case string(catwalk.InferenceProviderZAI), string(catwalk.InferenceProviderDeepSeek):
			if model.ModelCfg.Think || reasoningEffort != "" {
				extraBody["thinking"] = map[string]any{
					"type": "enabled",
				}
			} else {
				extraBody["thinking"] = map[string]any{
					"type": "disabled",
				}
			}
		case string(catwalk.InferenceProviderFireworks):
			// NOTE: Fireworks break if we set both `reasoning_effort` and `thinking`.
			if model.ModelCfg.ReasoningEffort == "" {
				if model.ModelCfg.Think {
					extraBody["thinking"] = map[string]any{"type": "enabled"}
				} else {
					extraBody["thinking"] = map[string]any{"type": "disabled"}
				}
			}

		case string(catwalk.InferenceProviderBaseten):
			extraBody["chat_template_args"] = map[string]any{
				"enable_thinking": model.ModelCfg.Think || reasoningEffort != "" && reasoningEffort != "none",
			}

		case string(catwalk.InferenceProviderOpenCodeGo), string(catwalk.InferenceProviderOpenCodeZen):
			// MiniMax M3 uses the "thinking" parameter to control reasoning.
			// "reasoning_split" must be true so thinking content is returned
			// in the "reasoning_content" field instead of inline in "content".
			if strings.HasPrefix(strings.ToLower(model.CatwalkCfg.ID), "minimax") {
				if model.CatwalkCfg.CanReason && (model.ModelCfg.Think || reasoningEffort != "") {
					extraBody["thinking"] = map[string]any{"type": "adaptive"}
					extraBody["reasoning_split"] = true
				} else {
					extraBody["thinking"] = map[string]any{"type": "disabled"}
				}
			}

		case string(catwalk.InferenceProviderAlibabaSingapore), string(catwalk.InferenceProviderAlibabaUS):
			if model.CatwalkCfg.CanReason {
				extraBody["enable_thinking"] = model.ModelCfg.Think || reasoningEffort != ""
			}
		}

		mergedOptions["extra_body"] = extraBody

		parsed, err := openaicompat.ParseOptions(mergedOptions)
		if err == nil {
			options[openaicompat.Name] = parsed
		}
	default:
		// Known custom providers (litellm, ollama, omlx) are
		// openai-compat under the hood.
		if discover.IsKnownCustomProvider(string(providerCfg.Type)) {
			parsed, err := openaicompat.ParseOptions(mergedOptions)
			if err == nil {
				options[openaicompat.Name] = parsed
			}
		}
	}

	return options
}

func mergeCallOptions(model Model, cfg config.ProviderConfig) (fantasy.ProviderOptions, *float64, *float64, *int64, *float64, *float64) {
	modelOptions := getProviderOptions(model, cfg)
	temp := cmp.Or(model.ModelCfg.Temperature, model.CatwalkCfg.Options.Temperature)
	topP := cmp.Or(model.ModelCfg.TopP, model.CatwalkCfg.Options.TopP)
	topK := cmp.Or(model.ModelCfg.TopK, model.CatwalkCfg.Options.TopK)
	freqPenalty := cmp.Or(model.ModelCfg.FrequencyPenalty, model.CatwalkCfg.Options.FrequencyPenalty)
	presPenalty := cmp.Or(model.ModelCfg.PresencePenalty, model.CatwalkCfg.Options.PresencePenalty)
	return modelOptions, temp, topP, topK, freqPenalty, presPenalty
}

// buildAgent constructs a SessionAgent for the named agent at the given depth.
// depth=3 is the top-level orchestrator; depth decreases with each delegation
// level. isSubAgent is derived as depth < 3. waitForMCP controls whether the
// tool-building goroutine waits for MCP initialisation to complete before
// building the tool list; pass false for the eager startup build (Run will
// wait and rebuild) and true for lazily-built specialist agents.
func (c *coordinator) buildAgent(ctx context.Context, agentName string, agentCfg config.Agent, depth int, waitForMCP bool) (SessionAgent, error) {
	isSubAgent := depth < 3

	large, small, err := c.buildAgentModels(ctx, agentCfg)
	if err != nil {
		return nil, err
	}

	largeProviderCfg, _ := c.cfg.Config().Providers.Get(large.ModelCfg.Provider)
	result := NewSessionAgent(SessionAgentOptions{
		LargeModel:           large,
		SmallModel:           small,
		SystemPromptPrefix:   largeProviderCfg.SystemPromptPrefix,
		SystemPrompt:         "",
		Depth:                depth,
		IsSubAgent:           isSubAgent,
		DisableAutoSummarize: c.cfg.Config().Options.DisableAutoSummarize,
		IsYolo:               c.permissions.YoloLevel() != config.YoloOff,
		Sessions:             c.sessions,
		Messages:             c.messages,
		Tools:                nil,
		Notify:               c.notify,
		ProviderConfig:       largeProviderCfg,
	})

	// Capture values needed in goroutines.
	largeProvider := large.Model.Provider()
	largeModel := large.Model.Model()

	// Use a local errgroup so the agent is fully initialised (prompt + tools)
	// before it is returned to the caller. This avoids the shared-errgroup
	// reuse hazard and ensures each buildAgent call is self-contained.
	var wg errgroup.Group

	wg.Go(func() error {
		p, buildErr := c.buildPrompt(agentName, agentCfg)
		if buildErr != nil {
			return buildErr
		}
		systemPrompt, buildErr := p.Build(ctx, largeProvider, largeModel, c.cfg)
		if buildErr != nil {
			return buildErr
		}
		result.SetSystemPrompt(systemPrompt)
		return nil
	})

	wg.Go(func() error {
		// The eager startup build (waitForMCP=false) skips this wait:
		// coordinator.Run waits for MCP init and rebuilds the tool list
		// via UpdateModels before the first turn, so blocking TUI
		// startup here is redundant. Lazily-built agents keep the wait
		// so their first tool list is complete regardless of call order.
		if waitForMCP {
			if err := toolsmcp.WaitForInit(ctx); err != nil {
				return err
			}
		}
		agentTools, lazyMap, buildErr := c.buildTools(ctx, agentCfg, depth)
		if buildErr != nil {
			return buildErr
		}
		result.SetTools(agentTools)
		result.SetLazyMCPToolMap(lazyMap)
		return nil
	})

	if err := wg.Wait(); err != nil {
		return nil, err
	}

	return result, nil
}

// buildPrompt constructs the system prompt for the given agent name and config.
// Orchestrator agents use orchestratorPrompt; all others use specialistPrompt.
// It snapshots coordinator fields under RLock to avoid races with ReloadPlugins.
func (c *coordinator) buildPrompt(agentName string, agentCfg config.Agent) (*prompt.Prompt, error) {
	c.orchestratorMu.RLock()
	activeSkills := c.activeSkills
	agentMDs := c.agentMDs
	agentConfigs := c.agentConfigs
	c.orchestratorMu.RUnlock()
	return c.buildPromptWithState(agentName, agentCfg, activeSkills, agentMDs, agentConfigs)
}

func (c *coordinator) buildPromptWithState(
	agentName string,
	agentCfg config.Agent,
	activeSkills []*skills.Skill,
	agentMDs map[string]prompt.AgentMD,
	agentConfigs map[string]config.Agent,
) (*prompt.Prompt, error) {
	opts := []prompt.Option{
		prompt.WithWorkingDir(c.cfg.WorkingDir()),
		prompt.WithAllowedSkills(agentCfg.AllowedSkills),
		prompt.WithAvailableSkills(activeSkills),
	}

	if agentName == config.AgentOrchestrator {
		// Build agents block and delegation workflow from parsed .md files.
		agentsBlock, delegationWorkflow := buildOrchestratorBlocksFromState(agentMDs, agentConfigs)
		opts = append(
			opts,
			prompt.WithAgentsBlock(agentsBlock),
			prompt.WithDelegationWorkflow(delegationWorkflow),
			prompt.WithAppendPrompt(agentCfg.AppendPrompt),
		)
		return orchestratorPrompt(opts...)
	}

	// Specialist: include agent body if available.
	agentBody := ""
	if md, ok := agentMDs[agentName]; ok {
		agentBody = md.Body
	}
	opts = append(
		opts,
		prompt.WithAgentBody(agentBody),
		prompt.WithAppendPrompt(agentCfg.AppendPrompt),
	)
	return specialistPrompt(opts...)
}

// buildOrchestratorBlocks generates the AgentsBlock and DelegationWorkflow
// strings for the orchestrator prompt from the parsed agent .md files,
// excluding agents that are not in the active agentConfigs.
func (c *coordinator) buildOrchestratorBlocks() (agentsBlock, delegationWorkflow string) {
	return buildOrchestratorBlocksFromState(c.agentMDs, c.agentConfigs)
}

func buildOrchestratorBlocksFromState(
	agentMDs map[string]prompt.AgentMD,
	agentConfigs map[string]config.Agent,
) (agentsBlock, delegationWorkflow string) {
	activeAgents := make([]prompt.AgentMD, 0, len(agentMDs))
	for name, md := range agentMDs {
		// Only include agents that are configured and not the orchestrator itself.
		if _, ok := agentConfigs[name]; ok && name != config.AgentOrchestrator {
			activeAgents = append(activeAgents, md)
		}
	}
	// Sort for deterministic prompt output.
	slices.SortFunc(activeAgents, func(a, b prompt.AgentMD) int {
		return strings.Compare(a.Name, b.Name)
	})
	return prompt.BuildAgentsBlock(activeAgents), prompt.BuildDelegationWorkflow(activeAgents)
}

// getOrBuildAgent returns a cached sub-agent by name or lazily builds one.
// A mutex ensures only one goroutine builds the agent even under concurrent
// delegation; a second caller will hit the fast-path re-check after the lock.
// The cache key includes depth because depth controls whether the task
// delegation tool is included (depth > 1). Without depth in the key, an
// agent cached at depth=2 (with task tool) would be incorrectly served to a
// caller at depth=1 (where delegation must be blocked).
// When modelOverride is non-empty, a separate cache entry is used keyed by
// "agentName|depth|modelOverride".
func (c *coordinator) getOrBuildAgent(ctx context.Context, agentName string, depth int, modelOverride string) (SessionAgent, error) {
	cacheKey := fmt.Sprintf("%s|%d", agentName, depth)
	if modelOverride != "" {
		cacheKey = fmt.Sprintf("%s|%d|%s", agentName, depth, modelOverride)
	}

	if existing, ok := c.agents.Get(cacheKey); ok {
		return existing, nil
	}

	c.agentBuildMu.Lock()
	defer c.agentBuildMu.Unlock()

	// Double-check after acquiring the lock.
	if existing, ok := c.agents.Get(cacheKey); ok {
		return existing, nil
	}

	agentCfg, ok := c.agentConfigs[agentName]
	if !ok {
		return nil, fmt.Errorf("agent %q not configured", agentName)
	}

	// Apply model override to a copy of the agent config when requested.
	if modelOverride != "" {
		agentCfg.Model = modelOverride
	}

	// Lazily-built specialist agents pass waitForMCP=true: by the time a
	// specialist is requested (via the task tool during a Run), MCP init has
	// already completed, so the wait is instant in practice, but keeping it
	// explicit makes correctness independent of that call-order assumption.
	built, err := c.buildAgent(ctx, agentName, agentCfg, depth, true)
	if err != nil {
		return nil, err
	}
	c.agents.Set(cacheKey, built)
	return built, nil
}

// buildTools assembles the tool set for an agent at the given delegation depth.
// At depth ≤ 1 the task delegation tool is excluded.
// AllowedTools is applied via ParseFilterList; AllowedMCP is applied per server.
// It snapshots coordinator fields under RLock to avoid races with ReloadPlugins.
func (c *coordinator) buildTools(ctx context.Context, agent config.Agent, depth int) ([]fantasy.AgentTool, map[string]string, error) {
	c.orchestratorMu.RLock()
	allSkills := c.allSkills
	activeSkills := c.activeSkills
	skillTracker := c.skillTracker
	agentMDs := c.agentMDs
	plugins := c.plugins
	c.orchestratorMu.RUnlock()
	return c.buildToolsWithState(ctx, agent, depth, allSkills, activeSkills, skillTracker, agentMDs, plugins)
}

func (c *coordinator) buildToolsWithState(
	ctx context.Context,
	agent config.Agent,
	depth int,
	allSkills []*skills.Skill,
	activeSkills []*skills.Skill,
	skillTracker *skills.Tracker,
	agentMDs map[string]prompt.AgentMD,
	plugins []*plugin.Plugin,
) ([]fantasy.AgentTool, map[string]string, error) {
	isSubAgent := depth < 3

	logFile := filepath.Join(c.cfg.Config().Options.ProjectDirectory, "logs", "anvil.log")

	// Build hook runner if PreToolUse hooks are configured.
	var hookRunner *hooks.Runner
	if preToolHooks := c.cfg.Config().Hooks[hooks.EventPreToolUse]; len(preToolHooks) > 0 {
		hookRunner = hooks.NewRunner(preToolHooks, c.cfg.WorkingDir(), c.cfg.WorkingDir())
	}

	// Assemble the full candidate tool set (before AllowedTools filtering).
	var candidateTools []fantasy.AgentTool

	// Add the task delegation tool if depth allows and the agent has delegates.
	if depth > 1 {
		callerName := agent.ID
		hasDelegates := callerName == config.AgentOrchestrator
		if !hasDelegates {
			if md, ok := agentMDs[callerName]; ok && len(md.DelegatesTo) > 0 {
				hasDelegates = true
			}
		}
		if hasDelegates {
			taskTool, err := c.taskTool(ctx, callerName, depth)
			if err != nil {
				return nil, nil, err
			}
			candidateTools = append(candidateTools, taskTool)
		}
	}

	// Add the agentic_fetch tool to the candidate set; filtering via AllowedTools applies below.
	agenticFetch, err := c.agenticFetchTool(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	candidateTools = append(
		candidateTools,
		agenticFetch,
		tools.NewBashTool(c.permissions, c.cfg.WorkingDir()),
		tools.NewAnvilInfoTool(c.cfg, c.lspManager, allSkills, activeSkills, skillTracker),
		tools.NewAnvilLogsTool(logFile),
		tools.NewJobOutputTool(),
		tools.NewJobKillTool(),
		tools.NewDownloadTool(c.permissions, c.cfg.WorkingDir(), nil),
		tools.NewEditTool(c.lspManager, c.permissions, c.filetracker, c.cfg.WorkingDir()),
		tools.NewMultiEditTool(c.lspManager, c.permissions, c.filetracker, c.cfg.WorkingDir()),
		tools.NewFetchTool(c.permissions, c.cfg.WorkingDir(), nil),
		tools.NewGlobTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Glob),
		tools.NewGrepTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Grep),
		tools.NewLsTool(c.permissions, c.cfg.WorkingDir(), c.cfg.Config().Tools.Ls),
		tools.NewSourcegraphTool(nil),
		tools.NewTodosTool(c.sessions),
		tools.NewViewTool(c.lspManager, c.permissions, c.filetracker, skillTracker, c.cfg.WorkingDir(), mergeSkillsPaths(c.cfg.Config().Options.SkillsPaths, plugins)...),
		tools.NewWriteTool(c.lspManager, c.permissions, c.filetracker, c.cfg.WorkingDir()),
	)

	// Add LSP tools if user has configured LSPs or auto_lsp is enabled (nil or true).
	if len(c.cfg.Config().LSP) > 0 || c.cfg.Config().Options.AutoLSP == nil || *c.cfg.Config().Options.AutoLSP {
		candidateTools = append(
			candidateTools,
			tools.NewDiagnosticsTool(c.lspManager),
			tools.NewReferencesTool(c.lspManager),
			tools.NewLSPRestartTool(c.lspManager),
			tools.NewSymbolsTool(c.lspManager),
			tools.NewDefinitionTool(c.lspManager),
			tools.NewCallHierarchyTool(c.lspManager),
			tools.NewRenameTool(c.lspManager, c.permissions, c.filetracker, c.cfg.WorkingDir()),
		)
	}

	if len(c.cfg.Config().MCP) > 0 {
		candidateTools = append(
			candidateTools,
			tools.NewListMCPResourcesTool(c.cfg, c.permissions),
			tools.NewReadMCPResourceTool(c.cfg, c.permissions),
		)
	}

	// Build the full list of candidate tool names for ParseFilterList.
	allToolNames := make([]string, 0, len(candidateTools))
	for _, t := range candidateTools {
		allToolNames = append(allToolNames, t.Info().Name)
	}

	// Resolve allowed tool names using ParseFilterList.
	allowedNames, err := config.ParseFilterList(agent.AllowedTools, allToolNames)
	if err != nil {
		slog.Warn("Invalid AllowedTools filter for agent; falling back to all tools", "agent", agent.Name, "error", err)
		allowedNames = allToolNames
	}

	// Apply the global DisabledTools exclusion so that tools disabled at the
	// top level are removed regardless of per-agent AllowedTools config.
	if opts := c.cfg.Config().Options; opts != nil && len(opts.DisabledTools) > 0 {
		disabled := make(map[string]struct{}, len(opts.DisabledTools))
		for _, d := range opts.DisabledTools {
			disabled[d] = struct{}{}
		}
		filtered := allowedNames[:0]
		for _, n := range allowedNames {
			if _, ok := disabled[n]; !ok {
				filtered = append(filtered, n)
			}
		}
		allowedNames = filtered
	}

	allowedSet := make(map[string]bool, len(allowedNames))
	for _, n := range allowedNames {
		allowedSet[n] = true
	}

	var filteredTools []fantasy.AgentTool
	for _, t := range candidateTools {
		if allowedSet[t.Info().Name] {
			filteredTools = append(filteredTools, t)
		}
	}

	cfg := c.cfg.Config()
	lazyMCPToolMap := make(map[string]string)
	for _, tool := range tools.GetMCPTools(c.permissions, c.cfg, c.cfg.WorkingDir()) {
		allowed := false
		if agent.AllowedMCP == nil {
			// No MCP restrictions.
			allowed = true
		} else if len(agent.AllowedMCP) == 0 {
			// No MCPs allowed.
			slog.Debug("No MCPs allowed", "tool", tool.Name(), "agent", agent.Name)
		} else {
			for mcp, mcpTools := range agent.AllowedMCP {
				if mcp != tool.MCP() {
					continue
				}
				if len(mcpTools) == 0 || slices.Contains(mcpTools, tool.MCPToolName()) {
					allowed = true
					break
				}
				slog.Debug("MCP not allowed", "tool", tool.Name(), "agent", agent.Name)
			}
		}
		if !allowed {
			continue
		}
		filteredTools = append(filteredTools, tool)

		// Record lazy MCP tool mapping for filtering at run time.
		if mcpCfg, ok := cfg.MCP[tool.MCP()]; ok && mcpCfg.IsLazy() {
			lazyMCPToolMap[tool.Name()] = tool.MCP()
		}
	}

	// Collect lazy MCPs with their descriptions and apply AllowedMCP
	// filtering so the enable_mcp tool only exposes permitted servers.
	lazyMCPs := make(map[string]string)
	for name, mcpCfg := range cfg.MCP {
		if mcpCfg.IsLazy() {
			lazyMCPs[name] = mcpCfg.LazyDescription
		}
	}
	lazyMCPs = filterAllowedLazyMCPs(lazyMCPs, agent.AllowedMCP)
	if len(lazyMCPs) > 0 {
		// Inject a synchronous connect callback that connects a deferred
		// MCP and rebuilds the tool list so the same run's next
		// PrepareStep sees the newly registered tools.
		connectFn := func(ctx context.Context, name string) (int, error) {
			if err := toolsmcp.ConnectDeferred(ctx, name, c.cfg); err != nil {
				return 0, err
			}
			return c.refreshMCPTools(ctx, name)
		}
		filteredTools = append(filteredTools, tools.NewEnableMCPTool(lazyMCPs, connectFn))
	}
	slices.SortFunc(filteredTools, func(a, b fantasy.AgentTool) int {
		return strings.Compare(a.Info().Name, b.Info().Name)
	})

	// Wrap tools with hook interception for the top-level agent only.
	// Sub-agents run without hook interception to avoid firing the user's
	// hooks N times per delegated turn. The top-level invocation of the
	// sub-agent tool itself is still wrapped from the orchestrator's side.
	filteredTools = wrapToolsWithHooks(filteredTools, hookRunner, isSubAgent)

	return filteredTools, lazyMCPToolMap, nil
}

// buildAgentModels resolves the large and small models for an agent.
// If agentCfg.Model is set it is used for the large model via ResolveAgentModel;
// otherwise the global large model is used. The small model always comes from
// the global small model config.
func (c *coordinator) buildAgentModels(ctx context.Context, agentCfg config.Agent) (Model, Model, error) {
	// Resolve large model — per-agent if configured, else global large.
	largeModelCfg, err := config.ResolveAgentModel(agentCfg, c.cfg.Config())
	if err != nil {
		// Fall back to global large model on resolution failure.
		var globalOk bool
		largeModelCfg, globalOk = c.cfg.Config().Models[config.SelectedModelTypeLarge]
		if !globalOk {
			return Model{}, Model{}, errLargeModelNotSelected
		}
	}

	smallModelCfg, ok := c.cfg.Config().Models[config.SelectedModelTypeSmall]
	if !ok {
		return Model{}, Model{}, errSmallModelNotSelected
	}

	isSubAgent := agentCfg.ID != config.AgentOrchestrator

	largeProviderCfg, ok := c.cfg.Config().Providers.Get(largeModelCfg.Provider)
	if !ok {
		return Model{}, Model{}, errLargeModelProviderNotConfigured
	}

	largeProvider, err := c.buildProvider(largeProviderCfg, largeModelCfg, isSubAgent)
	if err != nil {
		return Model{}, Model{}, err
	}

	smallProviderCfg, ok := c.cfg.Config().Providers.Get(smallModelCfg.Provider)
	if !ok {
		return Model{}, Model{}, errSmallModelProviderNotConfigured
	}

	smallProvider, err := c.buildProvider(smallProviderCfg, smallModelCfg, true)
	if err != nil {
		return Model{}, Model{}, err
	}

	var largeCatwalkModel *catwalk.Model
	var smallCatwalkModel *catwalk.Model

	for _, m := range largeProviderCfg.Models {
		if m.ID == largeModelCfg.Model {
			largeCatwalkModel = &m
		}
	}
	for _, m := range smallProviderCfg.Models {
		if m.ID == smallModelCfg.Model {
			smallCatwalkModel = &m
		}
	}

	if largeCatwalkModel == nil {
		return Model{}, Model{}, errLargeModelNotFound
	}

	if smallCatwalkModel == nil {
		return Model{}, Model{}, errSmallModelNotFound
	}

	largeModelID := largeModelCfg.Model
	smallModelID := smallModelCfg.Model

	if largeModelCfg.Provider == openrouter.Name && isExactoSupported(largeModelID) {
		largeModelID += ":exacto"
	}

	if smallModelCfg.Provider == openrouter.Name && isExactoSupported(smallModelID) {
		smallModelID += ":exacto"
	}

	largeModel, err := largeProvider.LanguageModel(ctx, largeModelID)
	if err != nil {
		return Model{}, Model{}, err
	}
	smallModel, err := smallProvider.LanguageModel(ctx, smallModelID)
	if err != nil {
		return Model{}, Model{}, err
	}

	return Model{
			Model:      largeModel,
			CatwalkCfg: *largeCatwalkModel,
			ModelCfg:   largeModelCfg,
			FlatRate:   largeProviderCfg.FlatRate,
		}, Model{
			Model:      smallModel,
			CatwalkCfg: *smallCatwalkModel,
			ModelCfg:   smallModelCfg,
			FlatRate:   smallProviderCfg.FlatRate,
		}, nil
}

func isExactoSupported(modelID string) bool {
	supportedModels := []string{
		"moonshotai/kimi-k2-0905",
		"deepseek/deepseek-v3.1-terminus",
		"z-ai/glm-4.6",
		"openai/gpt-oss-120b",
		"qwen/qwen3-coder",
	}
	return slices.Contains(supportedModels, modelID)
}

func (c *coordinator) Cancel(sessionID string) {
	c.getOrchestrator().Cancel(sessionID)
}

func (c *coordinator) CancelAll() {
	c.getOrchestrator().CancelAll()
}

func (c *coordinator) ClearQueue(sessionID string) {
	c.getOrchestrator().ClearQueue(sessionID)
}

func (c *coordinator) IsBusy() bool {
	return c.getOrchestrator().IsBusy()
}

func (c *coordinator) IsSessionBusy(sessionID string) bool {
	return c.getOrchestrator().IsSessionBusy(sessionID)
}

func (c *coordinator) Model() Model {
	return c.getOrchestrator().Model()
}

// UpdateModels rebuilds the orchestrator with the latest model config and
// clears the lazy agent map so sub-agents are rebuilt on next delegation.
func (c *coordinator) UpdateModels(ctx context.Context) error {
	orchestratorCfg, ok := c.agentConfigs[config.AgentOrchestrator]
	if !ok {
		return errOrchestratorAgentNotConfigured
	}

	// Rebuild the orchestrator models.
	large, small, err := c.buildAgentModels(ctx, orchestratorCfg)
	if err != nil {
		return err
	}

	orch := c.getOrchestrator()
	orch.SetModels(large, small)

	// Update provider config so the agent sees the refreshed token.
	if largeProviderCfg, ok := c.cfg.Config().Providers.Get(large.ModelCfg.Provider); ok {
		orch.SetProviderConfig(largeProviderCfg)
	}

	agentTools, lazyMap, err := c.buildTools(ctx, orchestratorCfg, 3)
	if err != nil {
		return err
	}
	orch.SetTools(agentTools)
	orch.SetLazyMCPToolMap(lazyMap)

	// Inject the connect callback so the agent can reconnect replayed
	// deferred servers at Run start.
	orch.SetConnectFn(func(ctx context.Context, name string) (int, error) {
		if err := toolsmcp.ConnectDeferred(ctx, name, c.cfg); err != nil {
			return 0, err
		}
		return c.refreshMCPTools(ctx, name)
	})

	// Invalidate lazily-built agents so they rebuild with new model config.
	c.agents.Reset(make(map[string]SessionAgent))

	return nil
}

// refreshMCPTools rebuilds the orchestrator's tool list and lazy MCP
// tool map after a deferred MCP server has been connected. It pushes
// the new tools to live agents via SetTools/SetLazyMCPToolMap so the
// same run's next PrepareStep sees the newly registered tools. Returns
// the number of tools registered for the named server.
//
// NOTE: This only updates the orchestrator agent. Sub-agents (task
// agents) are not updated and will not see the new tools until
// rebuilt. This is an accepted limitation — sub-agents are
// short-lived and rarely need newly-connected MCP tools mid-run.
func (c *coordinator) refreshMCPTools(ctx context.Context, name string) (int, error) {
	orchestratorCfg, ok := c.agentConfigs[config.AgentOrchestrator]
	if !ok {
		return 0, errOrchestratorAgentNotConfigured
	}

	agentTools, lazyMap, err := c.buildTools(ctx, orchestratorCfg, 3)
	if err != nil {
		return 0, fmt.Errorf("rebuilding tools after MCP connect: %w", err)
	}

	orch := c.getOrchestrator()
	orch.SetTools(agentTools)
	orch.SetLazyMCPToolMap(lazyMap)

	// Count tools for the just-connected server.
	toolCount := 0
	for mcpName, mcpTools := range toolsmcp.Tools() {
		if mcpName == name {
			toolCount = len(mcpTools)
			break
		}
	}

	return toolCount, nil
}

func (c *coordinator) QueuedPrompts(sessionID string) int {
	return c.getOrchestrator().QueuedPrompts(sessionID)
}

func (c *coordinator) QueuedPromptsList(sessionID string) []string {
	return c.getOrchestrator().QueuedPromptsList(sessionID)
}

func (c *coordinator) Summarize(ctx context.Context, sessionID string) error {
	orch := c.getOrchestrator()
	providerCfg, ok := c.cfg.Config().Providers.Get(orch.Model().ModelCfg.Provider)
	if !ok {
		return errModelProviderNotConfigured
	}

	if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
		slog.Error("Failed to refresh OAuth2 token before summarize. Proceeding with existing token.", "error", err)
	}

	summarize := func() error {
		return orch.Summarize(ctx, sessionID, getProviderOptions(orch.Model(), providerCfg))
	}

	err := summarize()
	if err != nil && c.isUnauthorized(err) {
		if retryErr := c.retryAfterUnauthorized(ctx, providerCfg); retryErr == nil {
			return summarize()
		}
	}

	return err
}

// RegenerateTitle loads the conversation for the given session and
// regenerates the title using the full context. It always sets
// titleIsCustom to false.
func (c *coordinator) RegenerateTitle(ctx context.Context, sessionID string) error {
	msgs, err := c.messages.List(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("loading messages for title regeneration: %w", err)
	}
	if len(msgs) == 0 {
		return nil
	}

	orch := c.getOrchestrator()
	sa, ok := orch.(*sessionAgent)
	if !ok {
		return errors.New("orchestrator is not a *sessionAgent")
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(orch.Model().ModelCfg.Provider)
	if !ok {
		return errModelProviderNotConfigured
	}

	if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
		slog.Error("Failed to refresh OAuth2 token before title regeneration. Proceeding with existing token.", "error", err)
	}

	sa.generateTitle(ctx, sessionID, msgs)
	return nil
}

// refreshTokenIfExpired proactively refreshes the OAuth token if it is
// approaching expiry. Anthropic tokens use a fixed 60-second window
// (anthropicoauth.NeedsRefresh); all other providers use the generic
// 10% margin from Token.IsExpired.
func (c *coordinator) refreshTokenIfExpired(ctx context.Context, providerCfg config.ProviderConfig) error {
	if providerCfg.OAuthToken == nil {
		return nil
	}
	var needsRefresh bool
	if providerCfg.ID == string(catwalk.InferenceProviderAnthropic) {
		needsRefresh = anthropicoauth.NeedsRefresh(providerCfg.OAuthToken)
	} else {
		needsRefresh = providerCfg.OAuthToken.IsExpired()
	}
	if !needsRefresh {
		return nil
	}
	slog.Debug("Token needs to be refreshed", "provider", providerCfg.ID)
	return c.refreshOAuth2Token(ctx, providerCfg)
}

// retryAfterUnauthorized attempts to refresh credentials after receiving a 401
// and returns nil if retry should be attempted.
func (c *coordinator) retryAfterUnauthorized(ctx context.Context, providerCfg config.ProviderConfig) error {
	switch {
	case providerCfg.OAuthToken != nil:
		slog.Debug("Received 401. Refreshing token and retrying", "provider", providerCfg.ID)
		return c.refreshOAuth2Token(ctx, providerCfg)
	case strings.Contains(providerCfg.APIKeyTemplate, "$"):
		slog.Debug("Received 401. Refreshing API Key template and retrying", "provider", providerCfg.ID)
		return c.refreshApiKeyTemplate(ctx, providerCfg)
	default:
		return nil
	}
}

func (c *coordinator) isUnauthorized(err error) bool {
	var providerErr *fantasy.ProviderError
	return errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized
}

// makeAuthRefreshCallback returns an OnAuthRefresh callback for fantasy that
// delegates to the coordinator's existing credential refresh logic. Returns
// nil if no refresh mechanism is configured for the provider.
//
// Refreshing inside fantasy's retry loop, rather than re-invoking the whole
// agent run, is what keeps a 401 from producing a duplicate assistant
// message.
func (c *coordinator) makeAuthRefreshCallback(providerCfg config.ProviderConfig) func(context.Context, *fantasy.ProviderError) error {
	if providerCfg.OAuthToken == nil && !strings.Contains(providerCfg.APIKeyTemplate, "$") {
		return nil
	}
	return func(ctx context.Context, _ *fantasy.ProviderError) error {
		return c.retryAfterUnauthorized(ctx, providerCfg)
	}
}

func (c *coordinator) refreshOAuth2Token(ctx context.Context, providerCfg config.ProviderConfig) error {
	if err := c.cfg.RefreshOAuthToken(ctx, config.ScopeGlobal, providerCfg.ID); err != nil {
		slog.Error("Failed to refresh OAuth token after 401 error", "provider", providerCfg.ID, "error", err)
		return err
	}
	if err := c.UpdateModels(ctx); err != nil {
		return err
	}
	return nil
}

func (c *coordinator) refreshApiKeyTemplate(ctx context.Context, providerCfg config.ProviderConfig) error {
	newAPIKey, err := c.cfg.Resolve(providerCfg.APIKeyTemplate)
	if err != nil {
		slog.Error("Failed to re-resolve API key after 401 error", "provider", providerCfg.ID, "error", err)
		return err
	}

	providerCfg.APIKey = newAPIKey
	c.cfg.Config().Providers.Set(providerCfg.ID, providerCfg)

	if err := c.UpdateModels(ctx); err != nil {
		return err
	}
	return nil
}

// subAgentParams holds the parameters for running a sub-agent.
type subAgentParams struct {
	Agent          SessionAgent
	SessionID      string
	AgentMessageID string
	ToolCallID     string
	Prompt         string
	SessionTitle   string
	// SessionSetup is an optional callback invoked after session creation
	// but before agent execution, for custom session configuration.
	SessionSetup func(sessionID string)
}

// runSubAgent runs a sub-agent and handles session management and cost accumulation.
// It creates a sub-session, runs the agent with the given prompt, and propagates
// the cost to the parent session.
func (c *coordinator) runSubAgent(ctx context.Context, params subAgentParams) (fantasy.ToolResponse, error) {
	// Create sub-session
	agentToolSessionID := c.sessions.CreateAgentToolSessionID(params.AgentMessageID, params.ToolCallID)
	session, err := c.sessions.CreateTaskSession(ctx, agentToolSessionID, params.SessionID, params.SessionTitle)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("create session: %w", err)
	}
	defer c.permissions.RevokeAutoApproveSession(session.ID)

	// Call session setup function if provided
	if params.SessionSetup != nil {
		params.SessionSetup(session.ID)
	}

	// Get model configuration
	model := params.Agent.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return fantasy.ToolResponse{}, errModelProviderNotConfigured
	}

	// Run the agent
	result, err := params.Agent.Run(ctx, SessionAgentCall{
		SessionID:        session.ID,
		Prompt:           params.Prompt,
		MaxOutputTokens:  maxTokens,
		ProviderOptions:  getProviderOptions(model, providerCfg),
		Temperature:      model.ModelCfg.Temperature,
		TopP:             model.ModelCfg.TopP,
		TopK:             model.ModelCfg.TopK,
		FrequencyPenalty: model.ModelCfg.FrequencyPenalty,
		PresencePenalty:  model.ModelCfg.PresencePenalty,
		NonInteractive:   true,
		OnAuthRefresh:    c.makeAuthRefreshCallback(providerCfg),
	})
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to generate response: %s", err)), nil
	}

	// Update parent session cost on a best-effort basis. A failure here must
	// not discard the sub-agent output that was already produced.
	if err := c.updateParentSessionCost(ctx, session.ID, params.SessionID); err != nil {
		slog.Warn("Failed to update parent session cost",
			"child_session", session.ID,
			"parent_session", params.SessionID,
			"error", err,
		)
	}

	output := subAgentOutput(result)
	if output == "" {
		return fantasy.NewTextErrorResponse("Sub-agent completed but produced no text output."), nil
	}
	return fantasy.NewTextResponse(output), nil
}

func subAgentOutput(result *fantasy.AgentResult) string {
	if result == nil {
		return ""
	}
	return result.Response.Content.Text()
}

// updateParentSessionCost accumulates the cost from a child session to its parent session.
func (c *coordinator) updateParentSessionCost(ctx context.Context, childSessionID, parentSessionID string) error {
	childSession, err := c.sessions.Get(ctx, childSessionID)
	if err != nil {
		return fmt.Errorf("get child session: %w", err)
	}

	parentSession, err := c.sessions.Get(ctx, parentSessionID)
	if err != nil {
		return fmt.Errorf("get parent session: %w", err)
	}

	parentSession.Cost += childSession.Cost

	if _, err := c.sessions.Save(ctx, parentSession); err != nil {
		return fmt.Errorf("save parent session: %w", err)
	}

	return nil
}

// SkillStates returns a copy of the combined builtin and user skill
// discovery states captured at session start.
func (c *coordinator) SkillStates() []*skills.SkillState {
	c.orchestratorMu.RLock()
	defer c.orchestratorMu.RUnlock()
	return slices.Clone(c.skillStates)
}

// ActiveSkillByName returns the active skill with the given name, or nil if
// not found.
func (c *coordinator) ActiveSkillByName(name string) *skills.Skill {
	c.orchestratorMu.RLock()
	defer c.orchestratorMu.RUnlock()
	for _, s := range c.activeSkills {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// ReloadPlugins re-discovers all plugin content (skills, agents, commands)
// and rebuilds the orchestrator's system prompt and tools. The swap is
// atomic: if any step fails, the previous state is preserved. Lazy
// sub-agent caches are cleared so they rebuild on next use.
func (c *coordinator) ReloadPlugins(ctx context.Context) error {
	cfg := c.cfg.Config()

	// 1. Discover plugins.
	plugins := plugin.DiscoverAll(cfg.Plugins)

	// 2. Rebuild skills.
	newAll, newActive, newStates := discoverSkills(c.cfg, plugins)

	// 3. Rebuild agent MDs from embedded + plugins.
	newAgentMDs, err := discoverAgentMDs(agentMDFS, plugins)
	if err != nil {
		return fmt.Errorf("reloading agent descriptions: %w", err)
	}

	// 4. Re-apply .md defaults to produce new agent configs.
	// SetupAgentsWithDefaults is pure; cfg.Agents is not mutated until
	// the atomic swap below.
	mdDefaults := make(map[string]config.Agent, len(newAgentMDs))
	for name, md := range newAgentMDs {
		mdDefaults[name] = agentConfigFromMD(name, md)
	}
	// SetupAgentsWithDefaults is a pure function; it returns a new map
	// without touching cfg.Agents. The new agent configs are only committed
	// in the atomic swap section below.
	newAgentConfigs := cfg.SetupAgentsWithDefaults(mdDefaults)

	// 5. Validate delegates_to.
	agentMDSlice := make([]prompt.AgentMD, 0, len(newAgentMDs))
	for _, md := range newAgentMDs {
		agentMDSlice = append(agentMDSlice, md)
	}
	errs, warnings := prompt.ValidateDelegatesTo(agentMDSlice, cfg.DisabledAgents)
	for _, w := range warnings {
		slog.Warn("Agent delegation warning on reload", "error", w)
	}
	if len(errs) > 0 {
		return fmt.Errorf("agent delegates_to validation failed on reload: %w", errors.Join(errs...))
	}

	// 6. Rebuild orchestrator prompt and tools against the proposed state before
	// swapping coordinator fields. Any failure below must leave the existing
	// runtime state intact.
	orchestratorCfg, ok := newAgentConfigs[config.AgentOrchestrator]
	if !ok {
		return errOrchestratorAgentNotConfigured
	}

	orch := c.getOrchestrator()
	if orch == nil {
		return errOrchestratorAgentNotConfigured
	}

	newSkillTracker := skills.NewTracker(newActive)
	p, err := c.buildPromptWithState(config.AgentOrchestrator, orchestratorCfg, newActive, newAgentMDs, newAgentConfigs)
	if err != nil {
		return fmt.Errorf("rebuilding orchestrator prompt: %w", err)
	}

	large := orch.Model()
	systemPrompt, err := p.Build(ctx, large.Model.Provider(), large.Model.Model(), c.cfg)
	if err != nil {
		return fmt.Errorf("building orchestrator system prompt: %w", err)
	}

	agentTools, lazyMap, err := c.buildToolsWithState(ctx, orchestratorCfg, 3, newAll, newActive, newSkillTracker, newAgentMDs, plugins)
	if err != nil {
		return fmt.Errorf("rebuilding orchestrator tools: %w", err)
	}

	// 7. Atomic swap of coordinator state. Also commit the new agent
	// configs to the live Config so the rest of the application sees
	// a consistent view.
	c.orchestratorMu.Lock()
	cfg.Agents = newAgentConfigs
	c.allSkills = newAll
	c.activeSkills = newActive
	c.skillStates = newStates
	c.skillTracker = newSkillTracker
	c.agentConfigs = newAgentConfigs
	c.agentMDs = newAgentMDs
	c.plugins = plugins
	// Clear lazy agent cache so sub-agents rebuild on next use.
	c.agents.Reset(make(map[string]SessionAgent))
	orch.SetSystemPrompt(systemPrompt)
	orch.SetTools(agentTools)
	orch.SetLazyMCPToolMap(lazyMap)
	c.orchestratorMu.Unlock()

	slog.Info("Plugin reload complete",
		"skills", len(newActive),
		"agents", len(newAgentConfigs)-1, // exclude orchestrator
		"plugins", len(plugins))
	return nil
}

// mergeSkillsPaths returns a combined slice of user-configured skills paths
// and each plugin's skills directory (when non-empty).
func mergeSkillsPaths(userPaths []string, plugins []*plugin.Plugin) []string {
	merged := slices.Clone(userPaths)
	for _, p := range plugins {
		if p.SkillsPath != "" {
			merged = append(merged, p.SkillsPath)
		}
	}
	return merged
}

// discoverSkills runs the skill discovery pipeline and returns both the
// pre-filter (all discovered, after dedup) and post-filter (active) lists,
// plus the combined per-file discovery states. It also emits a single
// diagnostic log line summarising the outcome to help track skill-loading
// health over time.
func discoverSkills(cfg *config.ConfigStore, plugins []*plugin.Plugin) (allSkills, activeSkills []*skills.Skill, allStates []*skills.SkillState) {
	builtin, builtinStates := skills.DiscoverBuiltinWithStates()
	discovered := append([]*skills.Skill(nil), builtin...)
	allStates = append(allStates, builtinStates...)

	// Discover skills from plugins in reverse config order so that the first-
	// configured plugin has highest priority (appears last in the slice, and
	// Deduplicate keeps the last occurrence).
	for i := len(plugins) - 1; i >= 0; i-- {
		p := plugins[i]
		if p.SkillsPath == "" {
			continue
		}
		pluginSkills, pluginStates := skills.DiscoverWithStates([]string{p.SkillsPath})
		// Tag each skill with its plugin source.
		for _, s := range pluginSkills {
			s.Source = "plugin:" + p.Name
		}
		discovered = append(discovered, pluginSkills...)
		allStates = append(allStates, pluginStates...)
	}

	var userStates []*skills.SkillState
	var userPaths []string

	opts := cfg.Config().Options
	if opts != nil && len(opts.SkillsPaths) > 0 {
		userPaths = make([]string, 0, len(opts.SkillsPaths))
		for _, pth := range opts.SkillsPaths {
			expanded := home.Long(pth)
			if strings.HasPrefix(expanded, "$") {
				if resolved, err := cfg.Resolver().ResolveValue(expanded); err == nil {
					expanded = resolved
				}
			}
			userPaths = append(userPaths, expanded)
		}
		var userSkills []*skills.Skill
		userSkills, userStates = skills.DiscoverWithStates(userPaths)
		discovered = append(discovered, userSkills...)
		allStates = append(allStates, userStates...)
	}

	plugin.DetectCollisions(discovered)
	allSkills = skills.Deduplicate(discovered)
	var disabledSkills []string
	if opts != nil {
		disabledSkills = opts.DisabledSkills
	}
	activeSkills = skills.Filter(allSkills, disabledSkills)

	allStates = skills.DeduplicateStates(allStates)

	slices.SortStableFunc(allStates, func(a, b *skills.SkillState) int {
		return strings.Compare(strings.ToLower(a.Path), strings.ToLower(b.Path))
	})
	skills.SetLatestStates(allStates)
	skills.PublishStates(allStates)

	logDiscoveryStats(builtin, builtinStates, userStates, userPaths, allSkills, activeSkills, disabledSkills)
	return allSkills, activeSkills, allStates
}

// logTurnSkillUsage emits a per-turn diagnostic line showing which skills
// (if any) were loaded during this turn and which looked relevant based on
// a cheap keyword match against the user prompt. The goal is to surface
// "should-have-loaded but didn't" situations for later analysis.
//
// Logged at Info level under component=skills; heavy fields are elided when
// there is nothing interesting to report.
func logTurnSkillUsage(
	sessionID string,
	prompt string,
	activeSkills []*skills.Skill,
	tracker *skills.Tracker,
	before []string,
) {
	if tracker == nil || len(activeSkills) == 0 {
		return
	}

	after := tracker.LoadedNames()

	beforeSet := make(map[string]bool, len(before))
	for _, n := range before {
		beforeSet[n] = true
	}
	var loadedThisTurn []string
	for _, n := range after {
		if !beforeSet[n] {
			loadedThisTurn = append(loadedThisTurn, n)
		}
	}

	slog.Info(
		"Skill turn summary",
		"component", "skills",
		"session_id", sessionID,
		"prompt_len", len(prompt),
		"active_total", len(activeSkills),
		"loaded_total", len(after),
		"loaded_this_turn", loadedThisTurn,
	)
}

// logDiscoveryStats emits a single structured log line summarising skill
// discovery for the current session. It is intentionally low-volume: one
// line per session start.
func logDiscoveryStats(
	builtin []*skills.Skill,
	builtinStates, userStates []*skills.SkillState,
	userPaths []string,
	allSkills, activeSkills []*skills.Skill,
	disabled []string,
) {
	countErrors := func(states []*skills.SkillState) int {
		n := 0
		for _, s := range states {
			if s.State == skills.StateError {
				n++
			}
		}
		return n
	}

	userOK := 0
	for _, s := range userStates {
		if s.State == skills.StateNormal {
			userOK++
		}
	}

	activeNames := make([]string, 0, len(activeSkills))
	for _, s := range activeSkills {
		activeNames = append(activeNames, s.Name)
	}

	xml := skills.ToPromptXML(activeSkills)

	slog.Info(
		"Skill discovery complete",
		"component", "skills",
		"builtin_ok", len(builtin),
		"builtin_errors", countErrors(builtinStates),
		"user_ok", userOK,
		"user_errors", countErrors(userStates),
		"user_paths", len(userPaths),
		"deduped_total", len(allSkills),
		"active", len(activeSkills),
		"disabled", len(disabled),
		"prompt_bytes", len(xml),
		"prompt_tok_est", skills.ApproxTokenCount(xml),
		"active_names", activeNames,
	)
}
