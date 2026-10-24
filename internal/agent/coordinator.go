package agent

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/cacheusage"
	"github.com/Broderick-Westrope/anvil/internal/agent/notify"
	"github.com/Broderick-Westrope/anvil/internal/agent/prompt"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	toolsmcp "github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/filetracker"
	"github.com/Broderick-Westrope/anvil/internal/home"
	"github.com/Broderick-Westrope/anvil/internal/hooks"
	"github.com/Broderick-Westrope/anvil/internal/jobevents"
	"github.com/Broderick-Westrope/anvil/internal/lsp"
	"github.com/Broderick-Westrope/anvil/internal/message"
	anthropicoauth "github.com/Broderick-Westrope/anvil/internal/oauth/anthropic"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/plugin"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"golang.org/x/sync/errgroup"

	"charm.land/fantasy/providers/openrouter"
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

// ErrBusy is returned by Pause when active top-level runs don't finish
// within the budget.
var ErrBusy = errors.New("agent is busy")

// PluginWarning describes plugin content that a reload skipped because it
// could not be parsed. Warnings don't fail the reload.
type PluginWarning = plugin.Warning

type Coordinator interface {
	// INFO: (kujtim) this is not used yet we will use this when we have multiple agents
	// SetMainAgent(string)
	Run(ctx context.Context, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error)
	// RunTurn runs prompt in sessionID and waits for that turn's own result.
	// If the session is busy, the turn queues behind the work in progress;
	// messages queued after it run once it finishes.
	RunTurn(ctx context.Context, sessionID, prompt string) (*fantasy.AgentResult, error)
	// RunWorkflowAgent runs a fresh, isolated subagent for a workflow.
	RunWorkflowAgent(ctx context.Context, call WorkflowAgentCall) (WorkflowAgentOutput, error)
	// HasAgent reports whether name is a configured agent other than the
	// orchestrator.
	HasAgent(name string) bool
	// Hold stops new runs from starting in sessionIDs and waits for their
	// in-flight runs to finish. Messages sent meanwhile queue; exclusive
	// work such as summarizing is refused. Holds nest. The idempotent
	// release lifts the hold and starts work queued behind it.
	Hold(ctx context.Context, sessionIDs []string) (release func(), err error)
	// ReserveSession stops new runs from starting in sessionID at once,
	// without waiting for runs in flight, until release. Messages sent
	// meanwhile queue.
	ReserveSession(sessionID string) (release func())
	// RunWake starts a turn for an idle session to deliver its pending
	// job events. See SessionAgent.RunWake.
	RunWake(ctx context.Context, sessionID string, eligible func() bool) (*fantasy.AgentResult, error)
	// SummarizeBranch summarizes the branch a user rewound from and stores
	// the summary under the rewind target. See BranchSummaryRequest.
	SummarizeBranch(ctx context.Context, req BranchSummaryRequest) (message.Message, error)
	// DiscardBranchSummary deletes an unused branch summary and moves the
	// leaf back to its parent.
	DiscardBranchSummary(ctx context.Context, sessionID, summaryID string) error
	// BranchSummarizers lists the agents that can summarize branches.
	BranchSummarizers() []string
	// BranchWorthSummarizing reports whether rewinding from fromLeaf to
	// target leaves anything a summary could say.
	BranchWorthSummarizing(ctx context.Context, fromLeaf, target string) (bool, error)
	IsSummarizing(sessionID string) bool
	Cancel(sessionID string)
	CancelAll()
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	// IsStepRunning reports whether the step that created the assistant
	// message is still in flight in this process, in any of the
	// coordinator's agents. A tool call without a result whose step isn't
	// running was orphaned, such as by a crash.
	IsStepRunning(assistantMessageID string) bool
	QueuedPrompts(sessionID string) int
	QueuedPromptsList(sessionID string) []string
	ClearQueue(sessionID string)
	Summarize(context.Context, string) error
	RegenerateTitle(ctx context.Context, sessionID string) error
	// CompleteSmall sends one prompt to the small model, outside any
	// session, and returns its reply and the model's ID.
	CompleteSmall(ctx context.Context, system, prompt string) (text, model string, err error)
	Model() Model
	UpdateModels(ctx context.Context) error
	// ReloadPlugins re-discovers plugin content and rebuilds the
	// orchestrator. Skipped plugin content is returned as warnings.
	ReloadPlugins(ctx context.Context) ([]PluginWarning, error)
	// Pause stops new top-level runs from being admitted and waits for
	// active ones to finish, up to budget. On success the caller must
	// call resume. On timeout it un-pauses and returns ErrBusy.
	Pause(ctx context.Context, budget time.Duration) (resume func(), err error)
	SkillStates() []*skills.SkillState
	// ActiveSkillByName returns the active skill with the given name, or nil
	// if not found.
	ActiveSkillByName(name string) *skills.Skill
	// RefreshMCPTools rebuilds the orchestrator's tool list after an MCP
	// server has been (re)connected. Without this, the orchestrator's
	// PrepareStep would still use a stale snapshot that omits the
	// server's tools.
	RefreshMCPTools(ctx context.Context, name string) error
	// MCPRoutes returns how each MCP server's tools reach the model, in
	// name order, and false until MCP initialisation has finished.
	MCPRoutes() ([]tools.MCPRouteInfo, bool)

	// SteerSubagent sends a human message to the subagent running in
	// childSessionID, delivered at its next step boundary. With interrupt,
	// it also cancels the subagent's current attempt and continues the
	// same task call with the message. It fails once the call has started
	// building its result, or when no call is running there.
	SteerSubagent(childSessionID, text string, interrupt bool) error
	// CancelSubagent ends the task call running in childSessionID, as
	// cancelled by a human. It reports false when none is running.
	CancelSubagent(childSessionID string) bool
	// SubagentRunning reports whether a task call is running in
	// childSessionID.
	SubagentRunning(childSessionID string) bool
	// SubagentPendingSteers lists the human steers accepted for
	// childSessionID's running task call but not yet delivered.
	SubagentPendingSteers(childSessionID string) []string
	// SubscribeSubagentEvents nudges subscribers when a task call ends
	// with steers it never delivered. The events can be dropped; the
	// steers wait in TakeReturnedSteers.
	SubscribeSubagentEvents(ctx context.Context) <-chan pubsub.Event[SubagentSteersReturned]
	// TakeReturnedSteers returns, and forgets, the steers task calls
	// working for rootSessionID ended without delivering.
	TakeReturnedSteers(rootSessionID string) []SubagentSteersReturned
}

type coordinator struct {
	admission    *admission
	runningSteps *csync.Map[string, struct{}]
	cfg          *config.ConfigStore
	sessions     session.Service
	messages     message.Service
	permissions  permission.Service
	filetracker  filetracker.Service
	lspManager   *lsp.Manager
	notify       pubsub.Publisher[notify.Notification]
	jobEvents    *jobevents.Store // Nil disables job notifications.
	jobArchive   tools.JobArchive // Nil disables persisted job fallbacks.
	onIdle       func(sessionID string)
	// jobWakeEnabled reports whether job events wake idle sessions; nil
	// means they never do.
	jobWakeEnabled func() bool
	// usageRecorder records per-response cache usage; nil disables it.
	usageRecorder *cacheusage.Recorder

	// orchestrator is the eagerly-built top-level agent. Protected by orchestratorMu.
	// Do NOT use csync.Value[SessionAgent] — it panics on interface types backed by pointers.
	orchestrator SessionAgent
	// orchestratorMu guards the plugin-derived state below. Lock order:
	// agentBuildMu, then orchestratorMu. Never take agentBuildMu while
	// holding orchestratorMu; ReloadPlugins never takes agentBuildMu.
	orchestratorMu sync.RWMutex

	// agents is a lazy map of named sub-agents, populated on first delegation.
	agents *csync.Map[string, SessionAgent]

	// agentsGen counts plugin reloads so a sub-agent build that started
	// before one is not cached after it. Protected by orchestratorMu.
	agentsGen uint64

	// agentBuildMu serialises lazy agent construction to prevent duplicate
	// builds when two goroutines race on the same agent name. Lock order:
	// agentBuildMu, then orchestratorMu.
	agentBuildMu sync.Mutex

	// routesMu guards routes, the MCP route map. It stays nil until the
	// startup snapshot exists and is never written after.
	routesMu sync.Mutex
	routes   mcpRoutes

	// Admission gate for top-level runs (Run, RunWake, Summarize). While
	// pauses > 0 new runs wait on resumed; active counts admitted calls
	// from entry to return. Nested runs (sub-agents, auto-summarise, title
	// generation) go through SessionAgent directly and are never gated.
	admitMu sync.Mutex
	pauses  int
	active  int
	resumed chan struct{} // Closed and cleared when the last pause ends.

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

	// leases holds the child sessions task calls are running in.
	leases subagentLeases
	// subagentRetryDelays are the waits before each automatic resume of a
	// failed subagent; nil means defaultSubagentRetryDelays.
	subagentRetryDelays []time.Duration
	subagentEventsOnce  sync.Once
	subagentEvents      *pubsub.Broker[SubagentSteersReturned]
	// returned holds steers handed back until the UI takes them.
	returnedMu sync.Mutex
	returned   []SubagentSteersReturned
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
	jobEvents *jobevents.Store,
	jobArchive tools.JobArchive,
	onIdle func(sessionID string),
	jobWakeEnabled func() bool,
	usageRecorder *cacheusage.Recorder,
) (Coordinator, error) {
	// Discover plugins once for both skills and agents.
	plugins := plugin.DiscoverAll(cfg.Config().Plugins, nil)
	// Discover skills once at session start.
	allSkills, activeSkills, skillStates := discoverSkills(cfg, plugins)
	skillTracker := skills.NewTracker(activeSkills)

	c := &coordinator{
		admission:      newAdmission(ctx),
		runningSteps:   csync.NewMap[string, struct{}](),
		cfg:            cfg,
		sessions:       sessions,
		messages:       messages,
		permissions:    permissions,
		filetracker:    filetracker,
		lspManager:     lspManager,
		notify:         notify,
		jobEvents:      jobEvents,
		jobArchive:     jobArchive,
		onIdle:         onIdle,
		jobWakeEnabled: jobWakeEnabled,
		usageRecorder:  usageRecorder,
		allSkills:      allSkills,
		activeSkills:   activeSkills,
		skillStates:    skillStates,
		skillTracker:   skillTracker,
		plugins:        plugins,
		agents:         csync.NewMap[string, SessionAgent](),
	}
	// Runs the admission queue starts on their own goroutines count as
	// active for Pause, but a cancelled submission stops waiting for one.
	c.admission.gate = c.admit

	// Enable MCP OAuth tool-name rename when the Anthropic provider uses
	// OAuth credentials. This must happen once during coordinator
	// initialisation, before any tools are registered.
	for providerCfg := range cfg.Config().Providers.Seq() {
		if providerCfg.ID == string(catwalk.InferenceProviderAnthropic) && providerCfg.OAuthToken != nil {
			toolsmcp.SetOAuthRename(true)
			break
		}
	}

	agentMDs, err := discoverAgentMDs(agentMDFS, plugins, nil)
	if err != nil {
		return nil, fmt.Errorf("loading agent descriptions: %w", err)
	}
	c.agentMDs = agentMDs

	if len(agentMDs) == 0 {
		slog.Warn("No specialist agents discovered; the orchestrator will handle all tasks directly. Configure plugins to provide agent definitions.")
	}

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

	// Convert AgentMD capability fields to config.Agent defaults and re-setup
	// the agent roster, replacing the hardcoded non-orchestrator defaults set
	// during config loading with values sourced from the .md frontmatter.
	// The store keeps them so later config reloads retain plugin agents.
	cfg.SetAgentDefaults(agentDefaultsFromMDs(agentMDs))
	c.agentConfigs = cfg.Config().Agents

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

// agentDefaultsFromMDs converts every agent .md file into its config.Agent
// default.
func agentDefaultsFromMDs(agentMDs map[string]prompt.AgentMD) map[string]config.Agent {
	defaults := make(map[string]config.Agent, len(agentMDs))
	for name, md := range agentMDs {
		defaults[name] = agentConfigFromMD(name, md)
	}
	return defaults
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
// are intentionally ignored; plugin agents must be at the top level. Files
// that can't be read or parsed are skipped and reported to warn, which may
// be nil.
func loadAgentMDsFromDir(dir string, warn func(PluginWarning)) (map[string]prompt.AgentMD, error) {
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
		path := filepath.Join(dir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("Failed to read plugin agent file", "path", path, "error", err)
			if warn != nil {
				warn(PluginWarning{Path: path, Err: fmt.Errorf("reading agent file: %w", err)})
			}
			continue
		}
		md, err := prompt.ParseAgentMD(name, content)
		if err != nil {
			slog.Warn("Failed to parse plugin agent file", "path", path, "error", err)
			if warn != nil {
				warn(PluginWarning{Path: path, Err: fmt.Errorf("parsing agent file: %w", err)})
			}
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
// order (earlier-configured plugins win on name collisions). Plugin agent
// files that can't be loaded are skipped and reported to warn, which may be
// nil.
func discoverAgentMDs(builtinFS fs.FS, plugins []*plugin.Plugin, warn func(PluginWarning)) (map[string]prompt.AgentMD, error) {
	agentMDs, err := loadAgentMDs(builtinFS)
	if err != nil {
		return nil, err
	}
	for i := len(plugins) - 1; i >= 0; i-- {
		p := plugins[i]
		if p.AgentsPath == "" {
			continue
		}
		pluginAgentMDs, loadErr := loadAgentMDsFromDir(p.AgentsPath, warn)
		if loadErr != nil {
			slog.Warn("Failed to load plugin agents",
				"plugin", p.Name, "path", p.AgentsPath, "error", loadErr)
			if warn != nil {
				warn(PluginWarning{Path: p.AgentsPath, Err: loadErr})
			}
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

// admit waits until no Pause is in effect, then counts the caller as an
// active top-level run until it calls release. It returns ctx.Err() if ctx
// is done first.
func (c *coordinator) admit(ctx context.Context) (release func(), err error) {
	c.admitMu.Lock()
	for c.pauses > 0 {
		resumed := c.resumed
		c.admitMu.Unlock()
		select {
		case <-resumed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		c.admitMu.Lock()
	}
	c.active++
	c.admitMu.Unlock()
	return func() {
		c.admitMu.Lock()
		c.active--
		c.admitMu.Unlock()
	}, nil
}

// Pause stops new top-level runs from being admitted and waits for active
// ones to finish, up to budget. On success the caller must call resume. On
// timeout it un-pauses and returns ErrBusy. Overlapping pauses nest: runs
// are admitted again once every one has resumed.
func (c *coordinator) Pause(ctx context.Context, budget time.Duration) (resume func(), err error) {
	c.admitMu.Lock()
	if c.pauses == 0 {
		c.resumed = make(chan struct{})
	}
	c.pauses++
	c.admitMu.Unlock()

	var once sync.Once
	resume = func() {
		once.Do(func() {
			c.admitMu.Lock()
			defer c.admitMu.Unlock()
			c.pauses--
			if c.pauses == 0 {
				close(c.resumed)
				c.resumed = nil
			}
		})
	}

	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		c.admitMu.Lock()
		idle := c.active == 0
		c.admitMu.Unlock()
		if idle {
			return resume, nil
		}
		select {
		case <-ctx.Done():
			resume()
			return nil, ctx.Err()
		case <-deadline.C:
			resume()
			return nil, ErrBusy
		case <-poll.C:
		}
	}
}

// Run implements Coordinator.
func (c *coordinator) Run(ctx context.Context, sessionID string, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	if sessionID == "" {
		return nil, ErrSessionMissing
	}
	if prompt == "" && !message.ContainsTextAttachment(attachments) {
		return nil, ErrEmptyPrompt
	}
	release, err := c.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	agentCfg, ok := c.orchestratorConfig()
	if !ok {
		return nil, errOrchestratorAgentNotConfigured
	}
	largeSelection, selectionErr := config.ResolveAgentModel(agentCfg, c.cfg.Config())
	smallSelection := c.cfg.Config().Models[config.SelectedModelTypeSmall]
	attachments = slices.Clone(attachments)
	state := &runState{}
	return c.admission.submit(ctx, sessionID, submission{prompt: prompt, run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		if selectionErr != nil {
			return nil, selectionErr
		}
		ctx = context.WithValue(ctx, modelSelectionKey{}, modelSelection{large: largeSelection, small: smallSelection})
		return c.runOwned(ctx, sessionID, prompt, state, attachments)
	}})
}

// RunTurn implements Coordinator.
func (c *coordinator) RunTurn(ctx context.Context, sessionID, prompt string) (*fantasy.AgentResult, error) {
	if sessionID == "" {
		return nil, ErrSessionMissing
	}
	if prompt == "" {
		return nil, ErrEmptyPrompt
	}
	agentCfg, ok := c.orchestratorConfig()
	if !ok {
		return nil, errOrchestratorAgentNotConfigured
	}
	largeSelection, selectionErr := config.ResolveAgentModel(agentCfg, c.cfg.Config())
	smallSelection := c.cfg.Config().Models[config.SelectedModelTypeSmall]
	state := &runState{}
	return c.admission.submitAwait(ctx, sessionID, submission{prompt: prompt, run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		if selectionErr != nil {
			return nil, selectionErr
		}
		ctx = context.WithValue(ctx, modelSelectionKey{}, modelSelection{large: largeSelection, small: smallSelection})
		return c.runOwned(ctx, sessionID, prompt, state, nil)
	}})
}

func (c *coordinator) runOwned(ctx context.Context, sessionID, prompt string, state *runState, attachments []message.Attachment) (*fantasy.AgentResult, error) {
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

	run := func() (*fantasy.AgentResult, error) {
		result, err := orch.Run(ctx, SessionAgentCall{
			retrySummary: func(ctx context.Context, err error) error {
				if !c.isUnauthorized(err) {
					return err
				}
				return c.retryAfterUnauthorized(ctx, providerCfg)
			},
			SessionID:        sessionID,
			Prompt:           prompt,
			Attachments:      attachments,
			MaxOutputTokens:  maxTokens,
			ProviderOptions:  mergedOptions,
			Temperature:      temp,
			TopP:             topP,
			TopK:             callTopK(providerCfg, topK),
			FrequencyPenalty: freqPenalty,
			PresencePenalty:  presPenalty,
			state:            state,
			OnAuthRefresh:    c.makeAuthRefreshCallback(providerCfg),
		})
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

	if c.isUnauthorized(originalErr) && !state.summaryFailed {
		if err := c.retryAfterUnauthorized(ctx, providerCfg); err == nil {
			return run()
		}
	}

	return result, originalErr
}

// RunWake implements Coordinator.
func (c *coordinator) RunWake(ctx context.Context, sessionID string, eligible func() bool) (*fantasy.AgentResult, error) {
	release, err := c.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	// Cheap pre-check; the session agent re-checks under its dispatch
	// lock.
	if c.getOrchestrator().IsSessionBusy(sessionID) {
		return nil, ErrSessionBusy
	}
	if err := toolsmcp.WaitForInit(ctx); err != nil {
		return nil, fmt.Errorf("failed to wait for MCP initialization: %w", err)
	}
	if err := c.UpdateModels(ctx); err != nil {
		return nil, fmt.Errorf("failed to update models: %w", err)
	}

	orch := c.getOrchestrator()
	model := orch.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}
	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return nil, errModelProviderNotConfigured
	}
	mergedOptions, temp, topP, topK, freqPenalty, presPenalty := mergeCallOptions(model, providerCfg)
	if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
		slog.Error("Failed to refresh OAuth2 token before wake. Proceeding with existing token.", "error", err)
	}

	return orch.RunWake(ctx, SessionAgentCall{
		SessionID:        sessionID,
		MaxOutputTokens:  maxTokens,
		ProviderOptions:  mergedOptions,
		Temperature:      temp,
		TopP:             topP,
		TopK:             callTopK(providerCfg, topK),
		FrequencyPenalty: freqPenalty,
		PresencePenalty:  presPenalty,
		OnAuthRefresh:    c.makeAuthRefreshCallback(providerCfg),
	}, eligible)
}

// IsSummarizing implements Coordinator.
func (c *coordinator) IsSummarizing(sessionID string) bool {
	return c.getOrchestrator().IsSummarizing(sessionID)
}

// getOrchestrator returns the orchestrator session agent, reading under lock.
func (c *coordinator) getOrchestrator() SessionAgent {
	c.orchestratorMu.RLock()
	defer c.orchestratorMu.RUnlock()
	return c.orchestrator
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

	slog.Debug("Resolved agent model",
		"agent", agentName,
		"model", large.ModelCfg.Provider+"/"+large.ModelCfg.Model,
		"reasoning_effort", effectiveReasoningEffort(large),
		"think", large.ModelCfg.Think,
		"depth", depth,
	)

	largeProviderCfg, _ := c.cfg.Config().Providers.Get(large.ModelCfg.Provider)
	// Only top-level sessions are woken, so only the orchestrator reports
	// idleness.
	var onIdle func(string)
	if !isSubAgent {
		onIdle = c.onIdle
	}
	result := NewSessionAgent(SessionAgentOptions{
		admission:            c.admission,
		runningSteps:         c.runningSteps,
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
		JobEvents:            c.jobEvents,
		OnIdle:               onIdle,
		UsageRecorder:        c.usageRecorder,
		AgentName:            agentName,
		WorkingDir:           c.cfg.WorkingDir(),
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
		agentTools, listing, buildErr := c.buildTools(ctx, agentCfg, depth)
		if buildErr != nil {
			return buildErr
		}
		result.SetTools(agentTools)
		result.SetMCPListing(listing)
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

	// buildTools reports an invalid AllowedTools filter.
	access, _ := newToolAccess(agentCfg, c.cfg.Config().Options)
	opts = append(opts, prompt.WithViewToolAvailable(access.allowsTool(tools.ViewToolName)))

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
		// Only include agents that are configured, not the orchestrator
		// itself, and not branch summarizers, which the user runs.
		if _, ok := agentConfigs[name]; ok && name != config.AgentOrchestrator && !md.BranchSummarizer {
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
	return c.getOrBuild(cacheKey, agentName, func(agentCfg config.Agent) (SessionAgent, error) {
		// Apply model override to a copy of the agent config when requested.
		if modelOverride != "" {
			agentCfg.Model = modelOverride
		}
		// Lazily-built specialist agents pass waitForMCP=true: by the time a
		// specialist is requested (via the task tool during a Run), MCP init has
		// already completed, so the wait is instant in practice, but keeping it
		// explicit makes correctness independent of that call-order assumption.
		return c.buildAgent(ctx, agentName, agentCfg, depth, true)
	})
}

// getOrBuild returns the agent cached under cacheKey, or builds it from
// agentName's config and caches it. Only one goroutine builds at a time.
func (c *coordinator) getOrBuild(cacheKey, agentName string, build func(config.Agent) (SessionAgent, error)) (SessionAgent, error) {
	if existing, ok := c.agents.Get(cacheKey); ok {
		return existing, nil
	}

	c.agentBuildMu.Lock()
	defer c.agentBuildMu.Unlock()

	// Double-check after acquiring the lock.
	if existing, ok := c.agents.Get(cacheKey); ok {
		return existing, nil
	}

	c.orchestratorMu.RLock()
	agentCfg, ok := c.agentConfigs[agentName]
	gen := c.agentsGen
	c.orchestratorMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("agent %q not configured", agentName)
	}

	built, err := build(agentCfg)
	if err != nil {
		return nil, err
	}
	c.cacheAgent(cacheKey, gen, built)
	return built, nil
}

// cacheAgent caches a built sub-agent unless a plugin reload happened since
// gen was read. The compare and Set share one critical section so they
// can't interleave with ReloadPlugins' Reset. It reports whether the agent
// was cached.
func (c *coordinator) cacheAgent(cacheKey string, gen uint64, built SessionAgent) bool {
	c.orchestratorMu.RLock()
	defer c.orchestratorMu.RUnlock()
	if c.agentsGen != gen {
		return false
	}
	c.agents.Set(cacheKey, built)
	return true
}

// orchestratorConfig returns the orchestrator's agent config, read under
// orchestratorMu.
func (c *coordinator) orchestratorConfig() (config.Agent, bool) {
	c.orchestratorMu.RLock()
	defer c.orchestratorMu.RUnlock()
	agentCfg, ok := c.agentConfigs[config.AgentOrchestrator]
	return agentCfg, ok
}

// buildTools assembles the tool set for an agent at the given delegation depth.
// At depth ≤ 1 the task delegation tool is excluded.
// AllowedTools is applied via ParseFilterList; AllowedMCP is applied per server.
// It snapshots coordinator fields under RLock to avoid races with ReloadPlugins.
// named lists tools a workflow agent asked for by name; a named MCP tool
// is listed even when its server is routed through the catalogue.
func (c *coordinator) buildTools(ctx context.Context, agent config.Agent, depth int, named ...string) ([]fantasy.AgentTool, mcpListing, error) {
	c.orchestratorMu.RLock()
	allSkills := c.allSkills
	activeSkills := c.activeSkills
	skillTracker := c.skillTracker
	agentMDs := c.agentMDs
	plugins := c.plugins
	c.orchestratorMu.RUnlock()
	return c.buildToolsWithState(ctx, agent, depth, allSkills, activeSkills, skillTracker, agentMDs, plugins, named)
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
	namedTools []string,
) ([]fantasy.AgentTool, mcpListing, error) {
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
	if depth > 1 && (agent.ID == config.AgentOrchestrator || len(agentMDs[agent.ID].DelegatesTo) > 0) {
		taskTool, err := c.taskTool(ctx, agent.ID, depth)
		if err != nil {
			return nil, mcpListing{}, err
		}
		candidateTools = append(candidateTools, taskTool)
	}

	// Add the agentic_fetch tool to the candidate set; filtering via AllowedTools applies below.
	agenticFetch, err := c.agenticFetchTool(ctx, nil)
	if err != nil {
		return nil, mcpListing{}, err
	}
	spillDataDir := config.GlobalDataDir()
	jobOpts := tools.JobToolOptions{Events: c.jobEvents, Archive: c.jobArchive, SpillDir: spillDataDir}
	candidateTools = append(
		candidateTools,
		agenticFetch,
		tools.NewBashTool(c.permissions, c.cfg.WorkingDir(), spillDataDir, c.jobWakeEnabled),
		tools.NewAnvilInfoTool(c.cfg, c.lspManager, allSkills, activeSkills, skillTracker, c.MCPRoutes),
		tools.NewAnvilLogsTool(logFile),
		tools.NewJobOutputTool(jobOpts),
		tools.NewJobKillTool(jobOpts),
		tools.NewJobListTool(jobOpts),
		tools.NewDownloadTool(c.permissions, c.cfg.WorkingDir(), nil),
		tools.NewEditTool(c.lspManager, c.permissions, c.filetracker, c.cfg.WorkingDir()),
		tools.NewMultiEditTool(c.lspManager, c.permissions, c.filetracker, c.cfg.WorkingDir()),
		tools.NewFetchTool(c.permissions, c.cfg.WorkingDir(), nil),
		tools.NewGlobTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Glob),
		tools.NewGrepTool(c.cfg.WorkingDir(), c.cfg.Config().Tools.Grep),
		tools.NewLsTool(c.permissions, c.cfg.WorkingDir(), c.cfg.Config().Tools.Ls),
		tools.NewSourcegraphTool(nil),
		tools.NewTodosTool(c.sessions),
		tools.NewViewTool(c.lspManager, c.permissions, c.filetracker, skillTracker, activeSkills, []string{shell.SpillDir(spillDataDir)}, c.cfg.WorkingDir(), trustedReadPaths(c.cfg.Config(), plugins)...),
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

	access, err := newToolAccess(agent, c.cfg.Config().Options)
	if err != nil {
		slog.Warn("Invalid AllowedTools filter for agent; falling back to all tools", "agent", agent.Name, "error", err)
	}
	var filteredTools []fantasy.AgentTool
	for _, t := range candidateTools {
		if access.allowsTool(t.Info().Name) {
			filteredTools = append(filteredTools, t)
		}
	}

	mcpTools := c.newAgentMCPTools(access, hookRunner, isSubAgent)
	listed, listing := mcpTools.listed(namedTools)
	filteredTools = append(filteredTools, listed...)

	// Wrap tools with hook interception for the top-level agent only.
	// Sub-agents run without hook interception to avoid firing the user's
	// hooks N times per delegated turn. The top-level invocation of the
	// sub-agent tool itself is still wrapped from the orchestrator's side.
	filteredTools = mcpTools.wrap(filteredTools)

	// call_mcp_tool is not hook-wrapped: the inner tool it runs is, so a
	// hook fires once per call, under the inner name.
	for _, tool := range mcpTools.catalogueTools() {
		if tool.Info().Name == tools.CallMCPToolToolName {
			filteredTools = append(filteredTools, tool)
		} else {
			filteredTools = append(filteredTools, mcpTools.wrap([]fantasy.AgentTool{tool})...)
		}
	}
	slices.SortFunc(filteredTools, func(a, b fantasy.AgentTool) int {
		return strings.Compare(a.Info().Name, b.Info().Name)
	})

	return filteredTools, listing, nil
}

// buildAgentModels resolves the large and small models for an agent.
// If agentCfg.Model is set it is used for the large model via ResolveAgentModel;
// otherwise the global large model is used. The small model always comes from
// the global small model config.
func (c *coordinator) buildAgentModels(ctx context.Context, agentCfg config.Agent) (Model, Model, error) {
	if selected, ok := ctx.Value(modelSelectionKey{}).(modelSelection); ok && agentCfg.ID == config.AgentOrchestrator {
		return c.buildResolvedAgentModels(ctx, selected.large, selected.small, false)
	}
	// Resolve large model — per-agent if configured, else global large.
	largeModelCfg, err := config.ResolveAgentModel(agentCfg, c.cfg.Config())
	if err != nil {
		// Fall back to global large model on resolution failure. Warn loudly:
		// a typo in agent frontmatter would otherwise silently promote a cheap
		// specialist onto the (expensive) orchestrator model.
		slog.Warn("Failed to resolve agent model; falling back to the global large model",
			"agent", agentCfg.ID,
			"configured_model", agentCfg.Model,
			"error", err,
		)
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

	return c.buildResolvedAgentModels(ctx, largeModelCfg, smallModelCfg, agentCfg.ID != config.AgentOrchestrator)
}

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

func (c *coordinator) buildResolvedAgentModels(ctx context.Context, largeModelCfg, smallModelCfg config.SelectedModel, isSubAgent bool) (Model, Model, error) {
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

	// A ChatGPT or Grok login serves its own catalog, which lives outside
	// Models; FindModel looks there first.
	largeCatwalkModel := largeProviderCfg.FindModel(largeModelCfg.Model)
	smallCatwalkModel := smallProviderCfg.FindModel(smallModelCfg.Model)

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

	// Bound every request so a hung provider fails instead of blocking the
	// session. Applied per request, so each retry gets a fresh budget.
	requestTimeout := c.cfg.Config().Options.GetRequestTimeout()
	largeModel = newRequestTimeoutModel(largeModel, requestTimeout)
	smallModel = newRequestTimeoutModel(smallModel, requestTimeout)

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

// HasAgent implements Coordinator.
func (c *coordinator) HasAgent(name string) bool {
	c.orchestratorMu.RLock()
	defer c.orchestratorMu.RUnlock()
	_, ok := c.agentConfigs[name]
	return ok && name != config.AgentOrchestrator
}

// Hold implements Coordinator.
func (c *coordinator) Hold(ctx context.Context, sessionIDs []string) (release func(), err error) {
	return c.admission.hold(ctx, sessionIDs)
}

func (c *coordinator) ReserveSession(sessionID string) (release func()) {
	return c.admission.reserve([]string{sessionID})
}

func (c *coordinator) IsSessionBusy(sessionID string) bool {
	return c.getOrchestrator().IsSessionBusy(sessionID)
}

func (c *coordinator) IsStepRunning(assistantMessageID string) bool {
	_, ok := c.runningSteps.Get(assistantMessageID)
	return ok
}

func (c *coordinator) Model() Model {
	return c.getOrchestrator().Model()
}

// UpdateModels rebuilds the orchestrator with the latest model config and
// clears the lazy agent map so sub-agents are rebuilt on next delegation.
func (c *coordinator) UpdateModels(ctx context.Context) error {
	// A ChatGPT or Grok login whose catalog fetch failed at sign-in would
	// leave its models missing; fill it in. A no-op once it exists.
	c.cfg.RefetchMissingSubscriptionModels(ctx)

	orchestratorCfg, ok := c.orchestratorConfig()
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

	agentTools, listing, err := c.buildTools(ctx, orchestratorCfg, 3)
	if err != nil {
		return err
	}
	orch.SetTools(agentTools)
	orch.SetMCPListing(listing)

	// Invalidate lazily-built agents so they rebuild with new model config.
	c.agents.Reset(make(map[string]SessionAgent))

	return nil
}

// RefreshMCPTools rebuilds the orchestrator's tool list after an MCP
// server has been (re)connected, and pushes it to the live agent so the
// same run's next PrepareStep sees the new tools.
//
// NOTE: This only updates the orchestrator agent. Sub-agents (task
// agents) are not updated and will not see the new tools until
// rebuilt. This is an accepted limitation: sub-agents are short-lived
// and rarely need newly-connected MCP tools mid-run.
func (c *coordinator) RefreshMCPTools(ctx context.Context, name string) error {
	orchestratorCfg, ok := c.orchestratorConfig()
	if !ok {
		return errOrchestratorAgentNotConfigured
	}

	agentTools, listing, err := c.buildTools(ctx, orchestratorCfg, 3)
	if err != nil {
		return fmt.Errorf("rebuild tools after connecting MCP %s: %w", name, err)
	}

	orch := c.getOrchestrator()
	orch.SetTools(agentTools)
	orch.SetMCPListing(listing)
	return nil
}

func (c *coordinator) QueuedPrompts(sessionID string) int {
	return c.getOrchestrator().QueuedPrompts(sessionID)
}

func (c *coordinator) QueuedPromptsList(sessionID string) []string {
	return c.getOrchestrator().QueuedPromptsList(sessionID)
}

func (c *coordinator) Summarize(ctx context.Context, sessionID string) error {
	release, err := c.admit(ctx)
	if err != nil {
		return err
	}
	defer release()

	_, err = c.admission.submit(ctx, sessionID, submission{exclusive: true, run: func(ctx context.Context) (*fantasy.AgentResult, error) { return nil, c.summarizeOwned(ctx, sessionID) }})
	return err
}

func (c *coordinator) summarizeOwned(ctx context.Context, sessionID string) error {
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
func (c *coordinator) CompleteSmall(ctx context.Context, system, prompt string) (string, string, error) {
	sa, ok := c.getOrchestrator().(*sessionAgent)
	if !ok {
		return "", "", errors.New("orchestrator is not a *sessionAgent")
	}
	if providerCfg, ok := c.cfg.Config().Providers.Get(sa.smallModel.Get().ModelCfg.Provider); ok {
		if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
			slog.Warn("Failed to refresh OAuth2 token before a small-model call. Proceeding with existing token.", "error", err)
		}
	}
	return sa.completeSmall(ctx, system, prompt)
}

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

var errRefreshSubAgentModels = errors.New("refresh sub-agent models")

// subAgentCall prepares a non-interactive run of agent in a sub-session,
// refreshing credentials and the agent's models first when they changed.
func (c *coordinator) subAgentCall(ctx context.Context, agent SessionAgent, sessionID, prompt string) (SessionAgentCall, error) {
	model := agent.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return SessionAgentCall{}, errModelProviderNotConfigured
	}

	if err := c.refreshTokenIfExpired(ctx, providerCfg); err != nil {
		slog.Error("Failed to refresh OAuth2 token before sub-agent execution. Proceeding with existing token.", "provider", providerCfg.ID, "error", err)
	}
	updatedProviderCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return SessionAgentCall{}, errModelProviderNotConfigured
	}
	if updatedProviderCfg.OAuthToken != providerCfg.OAuthToken || updatedProviderCfg.APIKey != providerCfg.APIKey {
		if err := c.refreshSubAgentModels(ctx, agent); err != nil {
			return SessionAgentCall{}, fmt.Errorf("%w: %w", errRefreshSubAgentModels, err)
		}
	}
	providerCfg = updatedProviderCfg

	authRefresh := c.makeAuthRefreshCallback(providerCfg)
	if authRefresh != nil {
		refreshCredentials := authRefresh
		authRefresh = func(ctx context.Context, providerErr *fantasy.ProviderError) error {
			if err := refreshCredentials(ctx, providerErr); err != nil {
				return err
			}
			return c.refreshSubAgentModels(ctx, agent)
		}
	}

	mergedOptions, temp, topP, topK, freqPenalty, presPenalty := mergeCallOptions(model, providerCfg)
	return SessionAgentCall{
		SessionID:        sessionID,
		Prompt:           prompt,
		MaxOutputTokens:  maxTokens,
		ProviderOptions:  mergedOptions,
		Temperature:      temp,
		TopP:             topP,
		TopK:             callTopK(providerCfg, topK),
		FrequencyPenalty: freqPenalty,
		PresencePenalty:  presPenalty,
		NonInteractive:   true,
		OnAuthRefresh:    authRefresh,
	}, nil
}

func (c *coordinator) refreshSubAgentModels(ctx context.Context, agent SessionAgent) error {
	model := agent.Model()
	smallModelCfg, ok := c.cfg.Config().Models[config.SelectedModelTypeSmall]
	if !ok {
		return errSmallModelNotSelected
	}
	large, small, err := c.buildResolvedAgentModels(ctx, model.ModelCfg, smallModelCfg, true)
	if err != nil {
		return err
	}
	model.Model = large.Model
	agent.SetModels(model, small)
	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return errModelProviderNotConfigured
	}
	agent.SetProviderConfig(providerCfg)
	return nil
}

func subAgentOutput(result *fantasy.AgentResult) string {
	if result == nil {
		return ""
	}
	return result.Response.Content.Text()
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
// sub-agent caches are cleared so they rebuild on next use. Malformed
// manifests and unparsable agent files are skipped and returned as
// warnings; they don't fail the reload.
func (c *coordinator) ReloadPlugins(ctx context.Context) ([]PluginWarning, error) {
	cfg := c.cfg.Config()
	var pluginWarnings []PluginWarning
	collect := func(w PluginWarning) { pluginWarnings = append(pluginWarnings, w) }

	// 1. Discover plugins.
	plugins := plugin.DiscoverAll(cfg.Plugins, collect)

	// 2. Rebuild skills.
	newAll, newActive, newStates := discoverSkills(c.cfg, plugins)

	// 3. Rebuild agent MDs from embedded + plugins.
	newAgentMDs, err := discoverAgentMDs(agentMDFS, plugins, collect)
	if err != nil {
		return pluginWarnings, fmt.Errorf("reloading agent descriptions: %w", err)
	}

	// 4. Re-apply .md defaults to produce new agent configs.
	// SetupAgentsWithDefaults is pure; cfg.Agents is not mutated until
	// the atomic swap below.
	mdDefaults := agentDefaultsFromMDs(newAgentMDs)
	// SetupAgentsWithDefaults is a pure function; it returns a new map
	// without touching cfg.Agents. The defaults are only committed to the
	// store after every fallible step below has succeeded.
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
		return pluginWarnings, fmt.Errorf("agent delegates_to validation failed on reload: %w", errors.Join(errs...))
	}

	// 6. Rebuild orchestrator prompt and tools against the proposed state before
	// swapping coordinator fields. Any failure below must leave the existing
	// runtime state intact.
	orchestratorCfg, ok := newAgentConfigs[config.AgentOrchestrator]
	if !ok {
		return pluginWarnings, errOrchestratorAgentNotConfigured
	}

	orch := c.getOrchestrator()
	if orch == nil {
		return pluginWarnings, errOrchestratorAgentNotConfigured
	}

	newSkillTracker := skills.NewTracker(newActive)
	p, err := c.buildPromptWithState(config.AgentOrchestrator, orchestratorCfg, newActive, newAgentMDs, newAgentConfigs)
	if err != nil {
		return pluginWarnings, fmt.Errorf("rebuilding orchestrator prompt: %w", err)
	}

	large := orch.Model()
	systemPrompt, err := p.Build(ctx, large.Model.Provider(), large.Model.Model(), c.cfg)
	if err != nil {
		return pluginWarnings, fmt.Errorf("building orchestrator system prompt: %w", err)
	}

	agentTools, listing, err := c.buildToolsWithState(ctx, orchestratorCfg, 3, newAll, newActive, newSkillTracker, newAgentMDs, plugins, nil)
	if err != nil {
		return pluginWarnings, fmt.Errorf("rebuilding orchestrator tools: %w", err)
	}

	// 7. Commit the new agent defaults through the store, which publishes
	// a Config whose Agents include them, then atomically swap coordinator
	// state.
	c.cfg.SetAgentDefaults(mdDefaults)
	newAgentConfigs = c.cfg.Config().Agents
	c.orchestratorMu.Lock()
	c.allSkills = newAll
	c.activeSkills = newActive
	c.skillStates = newStates
	c.skillTracker = newSkillTracker
	c.agentConfigs = newAgentConfigs
	c.agentMDs = newAgentMDs
	c.plugins = plugins
	// Clear lazy agent cache so sub-agents rebuild on next use, and bump
	// the generation so builds already in flight don't repopulate it.
	c.agentsGen++
	c.agents.Reset(make(map[string]SessionAgent))
	orch.SetSystemPrompt(systemPrompt)
	orch.SetTools(agentTools)
	orch.SetMCPListing(listing)
	c.orchestratorMu.Unlock()

	slog.Info("Plugin reload complete",
		"skills", len(newActive),
		"agents", len(newAgentConfigs)-1, // exclude orchestrator
		"plugins", len(plugins),
		"warnings", len(pluginWarnings))
	return pluginWarnings, nil
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

// trustedReadPaths returns the directories the view tool reads without a
// permission prompt: skill directories and command directories, so skills
// and commands can both load their bundled resources.
func trustedReadPaths(cfg *config.Config, plugins []*plugin.Plugin) []string {
	return append(mergeSkillsPaths(cfg.Options.SkillsPaths, plugins), commands.SourcePaths(cfg, plugins)...)
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

func (c *coordinator) WaitBackgroundJobs() {
	if waiter, ok := c.getOrchestrator().(interface{ WaitBackgroundJobs() }); ok {
		waiter.WaitBackgroundJobs()
	}
}

type modelSelectionKey struct{}

type modelSelection struct {
	large config.SelectedModel
	small config.SelectedModel
}
