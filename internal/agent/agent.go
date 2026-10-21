// Package agent is the core orchestration layer for Anvil AI agents.
//
// It provides session-based AI agent functionality for managing
// conversations, tool execution, and message handling. It coordinates
// interactions between language models, messages, sessions, and tools while
// handling features like automatic summarization, queuing, and token
// management.
package agent

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
	"github.com/Broderick-Westrope/anvil/internal/agent/cacheusage"
	"github.com/Broderick-Westrope/anvil/internal/agent/notify"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/jobevents"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/stringext"
	"github.com/Broderick-Westrope/anvil/internal/version"
	"github.com/google/uuid"
)

const (
	DefaultSessionName = "Untitled Session"

	// Constants for auto-summarization thresholds
	largeContextWindowThreshold = 200_000
	largeContextWindowBuffer    = 20_000
	smallContextWindowRatio     = 0.2
)

var userAgent = fmt.Sprintf("Anvil/%s", version.Version)

//go:embed templates/title.md
var titlePrompt []byte

//go:embed templates/summary.md
var summaryPrompt []byte

// Used to remove <think> tags from generated titles.
var (
	thinkTagRegex       = regexp.MustCompile(`(?s)<think>.*?</think>`)
	orphanThinkTagRegex = regexp.MustCompile(`</?think>`)
)

type SessionAgentCall struct {
	retrySummary     func(context.Context, error) error
	state            *runState
	SessionID        string
	Prompt           string
	ProviderOptions  fantasy.ProviderOptions
	Attachments      []message.Attachment
	MaxOutputTokens  int64
	Temperature      *float64
	TopP             *float64
	TopK             *int64
	FrequencyPenalty *float64
	PresencePenalty  *float64
	NonInteractive   bool
	// Await makes the caller wait for this call's own result, with its
	// context governing the run even after it queues or continues past
	// auto-compaction.
	Await bool

	// wake marks a run started by RunWake: it begins with a job notice
	// instead of a user prompt.
	wake bool

	// OnAuthRefresh, when non-nil, is called by fantasy when a stream
	// fails with an authentication error (HTTP 401). The callback should
	// refresh credentials and return nil on success, in which case
	// fantasy retries the stream transparently. Returning an error
	// surfaces the original auth error without retry.
	OnAuthRefresh func(ctx context.Context, err *fantasy.ProviderError) error
}

type SessionAgent interface {
	Run(context.Context, SessionAgentCall) (*fantasy.AgentResult, error)
	SetModels(large Model, small Model)
	SetProviderConfig(cfg config.ProviderConfig)
	SetTools(tools []fantasy.AgentTool)
	SetLazyMCPToolMap(m map[string]string)
	SetConnectFn(fn tools.ConnectFn)
	SetSystemPrompt(systemPrompt string)
	Cancel(sessionID string)
	CancelAll()
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	QueuedPrompts(sessionID string) int
	QueuedPromptsList(sessionID string) []string
	ClearQueue(sessionID string)
	Summarize(context.Context, string, fantasy.ProviderOptions) error
	// RunWake starts a run for an idle session to deliver pending job
	// events, using call's options with no user prompt. eligible is
	// re-checked under the dispatch lock. It returns ErrSessionBusy,
	// ErrWakeNotAllowed, or nil without running when nothing is pending.
	RunWake(ctx context.Context, call SessionAgentCall, eligible func() bool) (*fantasy.AgentResult, error)
	IsSummarizing(sessionID string) bool
	Model() Model
	// RequestOverhead is the size, in characters, of what every request
	// this agent sends carries besides the conversation: its system
	// prompt, with the MCP instructions a run adds, and the schemas of the
	// tools its model sees, with every lazy MCP server off.
	RequestOverhead() int
}

type Model struct {
	Model      fantasy.LanguageModel
	CatwalkCfg catwalk.Model
	ModelCfg   config.SelectedModel
	FlatRate   bool
}

type sessionAgent struct {
	largeModel         *csync.Value[Model]
	smallModel         *csync.Value[Model]
	systemPromptPrefix *csync.Value[string]
	systemPrompt       *csync.Value[string]
	tools              *csync.Slice[fantasy.AgentTool]
	lazyMCPToolMap     *csync.Map[string, string]
	connectFn          *csync.Value[tools.ConnectFn]
	// runningSteps holds the assistant message IDs of steps in flight.
	// The coordinator shares one set between all its agents.
	runningSteps *csync.Map[string, struct{}]

	depth                int
	isSubAgent           bool
	sessions             session.Service
	messages             message.Service
	disableAutoSummarize bool
	isYolo               bool
	notify               pubsub.Publisher[notify.Notification]
	providerConfig       *csync.Value[config.ProviderConfig]
	jobEvents            *jobevents.Store

	admission *admission
	onIdle    func(sessionID string)

	// usageRecorder receives one row per model response; nil disables
	// recording. agentName and workingDir are copied onto each row.
	usageRecorder *cacheusage.Recorder
	agentName     string
	workingDir    string
	// lastTurn holds each session's most recent turn-step history
	// fingerprint, so a run's first step can be compared with the previous
	// run. It lives in memory only. Subagent sessions are created per tool
	// call and run once, so their entries are deleted when the run ends.
	// Top-level entries stay for the life of the process; they are small
	// and bounded by the sessions used in it.
	lastTurn *csync.Map[string, turnPrefix]

	// dispatchLocks serialise, per session, the decisions that start,
	// queue, or finish a run, so concurrent prompts, wakes, and
	// summaries never start two runs at once or lose a queued prompt.
	// They are never held across model calls.
	dispatchLocksMu sync.Mutex
	dispatchLocks   map[string]*dispatchLock
	summarizing     *csync.Map[string, *submissionOwner]
	// wakeCounts counts consecutive wake runs since the last user run;
	// wakeSuppressed marks sessions canceled since the last user run.
	wakeCounts     *csync.Map[string, int]
	wakeSuppressed *csync.Map[string, bool]

	// backgroundJobs tracks fire-and-forget goroutines spawned by Run
	// (currently only title generation) so tests — and shutdown paths —
	// can wait for them instead of racing test/recorder teardown.
	backgroundJobs sync.WaitGroup
}

// dispatchLock is a session's dispatch mutex; refs, guarded by
// sessionAgent.dispatchLocksMu, counts its holders and waiters.
type dispatchLock struct {
	mu   sync.Mutex
	refs int
}

// WaitBackgroundJobs blocks until all fire-and-forget goroutines spawned
// by Run (e.g. title generation) have finished. Primarily for tests: the
// async title request must complete before a VCR recorder closes or the
// interaction is lost / logged after test completion.
func (a *sessionAgent) WaitBackgroundJobs() {
	a.backgroundJobs.Wait()
}

type SessionAgentOptions struct {
	admission            *admission
	runningSteps         *csync.Map[string, struct{}]
	LargeModel           Model
	SmallModel           Model
	SystemPromptPrefix   string
	SystemPrompt         string
	Depth                int
	IsSubAgent           bool
	DisableAutoSummarize bool
	IsYolo               bool
	Sessions             session.Service
	Messages             message.Service
	Tools                []fantasy.AgentTool
	Notify               pubsub.Publisher[notify.Notification]
	ProviderConfig       config.ProviderConfig
	// JobEvents delivers background job notifications; nil disables them.
	JobEvents *jobevents.Store
	// OnIdle, when non-nil, is called after a run or summary finishes
	// with nothing queued for the session. It must not block.
	OnIdle func(sessionID string)
	// UsageRecorder records per-response cache usage; nil disables it.
	UsageRecorder *cacheusage.Recorder
	// AgentName and WorkingDir label recorded usage rows.
	AgentName  string
	WorkingDir string
}

func NewSessionAgent(
	opts SessionAgentOptions,
) SessionAgent {
	if opts.admission == nil {
		opts.admission = newAdmission(context.Background())
	}
	if opts.runningSteps == nil {
		opts.runningSteps = csync.NewMap[string, struct{}]()
	}
	return &sessionAgent{
		runningSteps:         opts.runningSteps,
		largeModel:           csync.NewValue(opts.LargeModel),
		smallModel:           csync.NewValue(opts.SmallModel),
		systemPromptPrefix:   csync.NewValue(opts.SystemPromptPrefix),
		systemPrompt:         csync.NewValue(opts.SystemPrompt),
		depth:                opts.Depth,
		isSubAgent:           opts.IsSubAgent,
		sessions:             opts.Sessions,
		messages:             opts.Messages,
		disableAutoSummarize: opts.DisableAutoSummarize,
		tools:                csync.NewSliceFrom(opts.Tools),
		lazyMCPToolMap:       csync.NewMap[string, string](),
		connectFn:            csync.NewValue[tools.ConnectFn](nil),
		isYolo:               opts.IsYolo,
		notify:               opts.Notify,
		providerConfig:       csync.NewValue(opts.ProviderConfig),
		admission:            opts.admission,
		jobEvents:            opts.JobEvents,
		onIdle:               opts.OnIdle,
		usageRecorder:        opts.UsageRecorder,
		agentName:            opts.AgentName,
		workingDir:           opts.WorkingDir,
		lastTurn:             csync.NewMap[string, turnPrefix](),
		dispatchLocks:        make(map[string]*dispatchLock),
		summarizing:          csync.NewMap[string, *submissionOwner](),
		wakeCounts:           csync.NewMap[string, int](),
		wakeSuppressed:       csync.NewMap[string, bool](),
	}
}

func (a *sessionAgent) Run(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
	if call.Prompt == "" && !message.ContainsTextAttachment(call.Attachments) {
		return nil, ErrEmptyPrompt
	}
	if call.SessionID == "" {
		return nil, ErrSessionMissing
	}

	call.Attachments = slices.Clone(call.Attachments)
	if call.state == nil {
		call.state = &runState{}
	}
	job := submission{prompt: call.Prompt, run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		return a.runOwned(ctx, call)
	}}
	if call.Await {
		return a.admission.submitAwait(ctx, call.SessionID, job)
	}
	return a.admission.submit(ctx, call.SessionID, job)
}

func (a *sessionAgent) runOwned(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state := call.state
	if err := a.beginAttempt(ctx, call); err != nil {
		return nil, err
	}
	// Copy mutable fields under lock to avoid races with SetTools/SetModels.
	agentTools := a.tools.Copy()
	t := &turn{
		a:              a,
		call:           call,
		largeModel:     a.largeModel.Get(),
		providerCfg:    a.providerConfig.Get(),
		promptPrefix:   a.systemPromptPrefix.Get(),
		lazyMCPToolMap: a.lazyMCPToolMap.Copy(),
		injected:       newInjectedMessages(),
		runID:          uuid.NewString(),
		sanitized:      make(map[string]bool),
	}

	currentSession, err := a.sessions.Get(ctx, call.SessionID)
	if err != nil {
		return a.abortSetup(ctx, fmt.Errorf("failed to get session: %w", err))
	}
	if state.acceptedUserID != "" {
		currentSession.LeafMessageID = state.acceptedUserID
	}
	t.session = currentSession
	t.leaf = currentSession.LeafMessageID
	t.parentSessionID = currentSession.ParentSessionID

	msgs, raw, err := a.getSessionMessages(ctx, currentSession)
	if err != nil {
		return a.abortSetup(ctx, fmt.Errorf("failed to get session messages: %w", err))
	}

	// Derive the lazy MCP state from conversation history and inject
	// into the context so tool handlers can query it.
	initialEnabled := deriveLazyMCPState(raw)
	t.lazyState = tools.NewLazyMCPState(initialEnabled)
	ctx = tools.WithLazyMCPState(ctx, t.lazyState)

	// Reconnect replayed-enabled deferred servers. This runs at Run
	// start (not session load) so browsing history never triggers
	// connections. Failures are non-fatal: the server is downgraded
	// to not-enabled for this run.
	reconnectDeferredServers(ctx, a.connectFn.Get(), initialEnabled, t.lazyState)

	agent := t.newAgent(agentTools, a.systemPrompt.Get())

	// Capture before any messages are added so we can auto-regenerate the
	// title after the first assistant response completes.
	if state.acceptedUserID == "" {
		state.firstMessage = len(msgs) == 0
	}
	isFirstMessage := state.firstMessage
	msgs, started, err := t.start(ctx, msgs)
	if err != nil {
		return a.abortSetup(ctx, err)
	}
	if !started {
		return nil, nil
	}

	// Add the session to the context. Subagents run on their parent's tool
	// context, so an inherited root session ID wins over this session's own.
	rootSessionID := cmp.Or(tools.GetRootSessionFromContext(ctx), call.SessionID)
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, call.SessionID)
	ctx = context.WithValue(ctx, tools.RootSessionIDContextKey, rootSessionID)
	t.genCtx = ctx
	t.persistCtx = context.WithoutCancel(ctx)

	// Drain any debounced message updates before returning. message.Service
	// already flushes synchronously on terminal updates, but a defer here
	// guarantees the contract at every Run exit (success, error, panic
	// recovery upstream) without callers needing to know.
	defer func() {
		if flushErr := a.messages.FlushAll(t.persistCtx); flushErr != nil {
			slog.Error("Failed to flush pending message updates after run", "error", flushErr)
		}
	}()

	history := a.preparePrompt(msgs, t.largeModel.CatwalkCfg.SupportsImages)
	files := attachmentFiles(call.Attachments)

	// prevTurn is the history of the session's last recorded turn step
	// before this run; the turn updates it after each step.
	if a.usageRecorder != nil {
		t.prevTurn, t.havePrevTurn = a.lastTurn.Get(call.SessionID)
		if t.parentSessionID != "" {
			defer a.lastTurn.Del(call.SessionID)
		}
	}
	// Deferred so that no exit, including a panic recovered upstream,
	// leaves the last step looking in flight and its calls timing forever.
	defer t.endStep()
	result, err := agent.Stream(ctx, t.streamCall(history, files))
	if err != nil {
		if t.assistant == nil {
			return result, err
		}
		if recordErr := t.recordFailure(err); recordErr != nil {
			return nil, recordErr
		}
		return nil, err
	}
	if t.shouldSummarize {
		if err := a.summarizeAndResume(ctx, call, len(t.assistant.ToolCalls()) > 0); err != nil {
			return nil, err
		}
	}

	// Retitle after the first reply, except in non-interactive (task)
	// sub-sessions.
	if isFirstMessage && !call.NonInteractive {
		a.retitleFromFirstReply(ctx, call.SessionID, t.currentLeaf())
	}

	// Notify when the turn finishes, except in nested and non-interactive
	// sessions.
	if !call.NonInteractive && a.notify != nil {
		if err := a.notifyWhenFinished(ctx, call.SessionID, t.sessionTitle); err != nil {
			return nil, err
		}
	}

	a.reportIdleWhenDone(ctx, call.SessionID)
	return result, err
}

// notifyWhenFinished publishes an agent-finished notification once the
// session's submission completes, so the title is read after any rename.
func (a *sessionAgent) notifyWhenFinished(ctx context.Context, sessionID string, title func() string) error {
	owner, ok := ctx.Value(ownerKey{}).(*submissionOwner)
	if !ok || owner == nil || owner.sessionID != sessionID {
		return fmt.Errorf("missing submission owner for session %q", sessionID)
	}
	owner.onFinish = func() {
		a.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			SessionID: sessionID, SessionTitle: title(), Type: notify.TypeAgentFinished,
		})
	}
	return nil
}

// summarizeAndResume compacts the session after a turn stopped because the
// context window was nearly full. If the turn was cut off mid tool use, it
// queues a new run that picks up from the summary.
func (a *sessionAgent) summarizeAndResume(ctx context.Context, call SessionAgentCall, interrupted bool) error {
	err := a.Summarize(ctx, call.SessionID, call.ProviderOptions)
	if err != nil && call.retrySummary != nil {
		if refreshErr := call.retrySummary(ctx, err); refreshErr == nil {
			err = a.Summarize(ctx, call.SessionID, call.ProviderOptions)
		}
	}
	if err != nil {
		call.state.summaryFailed = true
		return err
	}
	if !interrupted {
		return nil
	}
	if call.wake {
		call.wake = false
		call.Prompt = "The previous session was interrupted because it got too long while handling background job updates. Continue from the summary."
	} else {
		call.Prompt = fmt.Sprintf("The previous session was interrupted because it got too long, the initial user request was: `%s`", call.Prompt)
	}
	call.state = nil
	a.admission.continueWith(ctx, call.SessionID, submission{prompt: call.Prompt, run: func(ctx context.Context) (*fantasy.AgentResult, error) { return a.Run(ctx, call) }})
	return nil
}

// retitleFromFirstReply regenerates the session title in the background
// from the branch ending at leaf, unless the user set the title. The
// assistant reply is already persisted, so the branch is loaded fresh.
func (a *sessionAgent) retitleFromFirstReply(ctx context.Context, sessionID, leaf string) {
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		slog.Error("Failed to load session for title regeneration", "error", err)
		return
	}
	if sess.TitleIsCustom {
		return
	}
	msgs, err := a.messages.GetBranchPath(ctx, leaf)
	if err != nil {
		slog.Error("Failed to load messages for title regeneration", "error", err)
		return
	}
	a.backgroundJobs.Add(1)
	go func() {
		defer a.backgroundJobs.Done()
		a.generateTitle(context.WithoutCancel(ctx), sessionID, msgs)
	}()
}

// RunWake implements SessionAgent.
func (a *sessionAgent) RunWake(ctx context.Context, call SessionAgentCall, eligible func() bool) (*fantasy.AgentResult, error) {
	if call.SessionID == "" {
		return nil, ErrSessionMissing
	}
	unlock := a.lockDispatch(call.SessionID)
	owner, err := a.admission.claimIdle(ctx, call.SessionID, func() error {
		if suppressed, _ := a.wakeSuppressed.Get(call.SessionID); suppressed ||
			a.wakeCount(call.SessionID) >= maxConsecutiveWakes ||
			(eligible != nil && !eligible()) {
			return ErrWakeNotAllowed
		}
		return nil
	})
	if err != nil {
		unlock()
		return nil, err
	}
	a.wakeCounts.Set(call.SessionID, a.wakeCount(call.SessionID)+1)
	unlock()

	call.wake = true
	call.Prompt = ""
	call.Attachments = nil
	call.state = &runState{}
	return a.admission.execute(owner, submission{run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		return a.runOwned(ctx, call)
	}})
}

func (a *sessionAgent) wakeCount(sessionID string) int {
	n, _ := a.wakeCounts.Get(sessionID)
	return n
}

// refundWake returns a wake that delivered nothing to the session's
// budget.
func (a *sessionAgent) refundWake(sessionID string) {
	unlock := a.lockDispatch(sessionID)
	defer unlock()
	if n := a.wakeCount(sessionID); n > 0 {
		a.wakeCounts.Set(sessionID, n-1)
	}
}

// lockDispatch locks the session's dispatch mutex and returns the
// function that unlocks it. Entries are reference-counted, holders and
// waiters alike, and removed when the last one unlocks, so the map only
// holds sessions with dispatch decisions in progress.
func (a *sessionAgent) lockDispatch(sessionID string) (unlock func()) {
	a.dispatchLocksMu.Lock()
	l, ok := a.dispatchLocks[sessionID]
	if !ok {
		l = &dispatchLock{}
		a.dispatchLocks[sessionID] = l
	}
	l.refs++
	a.dispatchLocksMu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		a.dispatchLocksMu.Lock()
		defer a.dispatchLocksMu.Unlock()
		if l.refs--; l.refs == 0 {
			delete(a.dispatchLocks, sessionID)
		}
	}
}

func (a *sessionAgent) abortSetup(ctx context.Context, err error) (*fantasy.AgentResult, error) {
	if owner, ok := ctx.Value(ownerKey{}).(*submissionOwner); ok && owner != nil {
		owner.handoffOnError = true
	}
	return nil, err
}

func (a *sessionAgent) reportIdleWhenDone(ctx context.Context, sessionID string) {
	if a.onIdle == nil {
		return
	}
	if owner, ok := ctx.Value(ownerKey{}).(*submissionOwner); ok && owner != nil && owner.sessionID == sessionID {
		owner.onIdle = func() { a.onIdle(sessionID) }
	}
}

func (a *sessionAgent) Summarize(ctx context.Context, sessionID string, opts fantasy.ProviderOptions) error {
	_, err := a.admission.submit(ctx, sessionID, submission{exclusive: true, run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		return nil, a.summarizeOwned(ctx, sessionID, opts)
	}})
	return err
}

func (a *sessionAgent) summarizeOwned(ctx context.Context, sessionID string, opts fantasy.ProviderOptions) error {
	if owner, ok := ctx.Value(ownerKey{}).(*submissionOwner); ok && owner != nil {
		a.summarizing.Set(sessionID, owner)
		defer a.summarizing.CompareAndDelete(sessionID, owner)
	}
	currentSession, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}
	// Copy mutable fields under lock to avoid races with SetModels.
	largeModel := a.largeModel.Get()
	call := a.newSideCall(usageKindSummary, sessionID, currentSession.ParentSessionID, largeModel)
	msgs, rawPath, err := a.getSessionMessages(ctx, currentSession)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		// Nothing to summarize.
		return nil
	}

	aiMsgs := a.preparePrompt(msgs, largeModel.CatwalkCfg.SupportsImages)

	genCtx := ctx
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	defer func() {
		if flushErr := a.messages.FlushAll(persistCtx); flushErr != nil {
			slog.Error("Failed to flush pending message updates after summarize", "error", flushErr)
		}
	}()

	agent := fantasy.NewAgent(largeModel.Model,
		fantasy.WithSystemPrompt(string(summaryPrompt)),
		fantasy.WithUserAgent(userAgent),
	)

	// Determine the firstKeptEntryId using the raw branch path (which
	// has real message IDs, not synthetic entries from context filtering).
	firstKeptEntryID := message.ComputeFirstKeptEntryID(rawPath, 20000)

	// Estimate total tokens before compaction using the raw path.
	totalTokensBefore := 0
	for _, m := range rawPath {
		totalTokensBefore += message.EstimateMessageTokens(m)
	}

	// Create a compaction message as a placeholder (MessageType =
	// compaction), parented to the current leaf.
	compactionMsg, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:            message.Assistant,
		Model:           largeModel.Model.Model(),
		Provider:        largeModel.Model.Provider(),
		ParentMessageID: currentSession.LeafMessageID,
		MessageType:     message.MessageTypeCompaction,
	})
	if err != nil {
		return err
	}

	call.messageID = compactionMsg.ID
	streamCall := call.streamCall(buildSummaryPrompt(currentSession.Todos))
	streamCall.Messages = aiMsgs
	streamCall.Headers = sessionHeaders(sessionID)
	streamCall.ProviderOptions = opts
	run := &summaryRun{a: a, sessionID: sessionID, genCtx: genCtx, persistCtx: persistCtx, msg: &compactionMsg}
	streamCall.OnReasoningDelta = run.onReasoningDelta
	streamCall.OnReasoningEnd = run.onReasoningEnd
	streamCall.OnTextDelta = run.onTextDelta
	resp, err := agent.Stream(genCtx, streamCall)
	if err != nil {
		return run.recordFailure(err)
	}

	// Populate the CompactionContent part with the generated summary.
	summaryText := compactionMsg.Content().Text
	summaryText = appendBackgroundJobsSection(summaryText,
		shell.GetBackgroundShellManager().ListBySession(sessionID), time.Now())
	compactionMsg.Parts = []message.ContentPart{
		message.CompactionContent{
			Summary:          summaryText,
			FirstKeptEntryID: firstKeptEntryID,
			TokensBefore:     totalTokensBefore,
		},
	}
	compactionMsg.AddFinish(message.FinishReasonEndTurn, "", "")
	// Use parent ctx (not genCtx) so the CompactionContent update
	// survives even if the user cancels between LLM completion and
	// this write. Without this, the compaction message would retain
	// its streaming TextContent instead of the final CompactionContent,
	// silently losing all compacted history.
	err = a.messages.Update(persistCtx, compactionMsg)
	if err != nil {
		return err
	}

	a.updateSessionUsage(largeModel, &currentSession, resp.TotalUsage, a.openrouterRunCost(resp.Steps), false)

	// Update session usage. The leaf already points to the compaction
	// message (advanced atomically by Create() above).
	usage := resp.Response.Usage
	currentSession.CompletionTokens = summaryCompletionTokens(usage, compactionMsg)
	currentSession.PromptTokens = 0
	currentSession.EstimatedUsage = usageIsZero(usage)
	_, err = a.sessions.Save(genCtx, currentSession)
	if err != nil {
		return err
	}

	a.reportIdleWhenDone(ctx, sessionID)
	return nil
}

// anthropicCacheProviders are the provider types that take explicit
// Anthropic ephemeral cache markers.
var anthropicCacheProviders = []string{anthropic.Name, bedrock.Name, vercel.Name}

// usesAnthropicCacheMarkers reports whether providerType takes explicit
// Anthropic ephemeral cache markers.
func usesAnthropicCacheMarkers(providerType string) bool {
	return slices.Contains(anthropicCacheProviders, providerType)
}

// anthropicCacheDisabled reports whether ANVIL_DISABLE_ANTHROPIC_CACHE
// turns Anthropic cache markers off.
func anthropicCacheDisabled() bool {
	disabled, _ := strconv.ParseBool(os.Getenv("ANVIL_DISABLE_ANTHROPIC_CACHE"))
	return disabled
}

func (a *sessionAgent) getCacheControlOptions() fantasy.ProviderOptions {
	if anthropicCacheDisabled() {
		return fantasy.ProviderOptions{}
	}
	opts := make(fantasy.ProviderOptions, len(anthropicCacheProviders))
	for _, name := range anthropicCacheProviders {
		opts[name] = &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		}
	}
	return opts
}

// sessionHeaders returns the HTTP headers we use for cache affinity on
// every LLM request for a given session.
//
// The session hash is used instead of the raw UUID so the header
// value is deterministic and opaque.
func sessionHeaders(sessionID string) map[string]string {
	hash := session.HashID(sessionID)
	return map[string]string{
		"x-session-id":       hash,
		"x-session-affinity": hash,
	}
}

func (a *sessionAgent) createUserMessage(ctx context.Context, call SessionAgentCall, parentMessageID string) (message.Message, error) {
	parts := []message.ContentPart{message.TextContent{Text: call.Prompt}}
	var attachmentParts []message.ContentPart
	for _, attachment := range call.Attachments {
		attachmentParts = append(attachmentParts, message.BinaryContent{Path: attachment.FilePath, MIMEType: attachment.MimeType, Data: attachment.Content})
	}
	parts = append(parts, attachmentParts...)
	msg, err := a.messages.Create(ctx, call.SessionID, message.CreateMessageParams{
		Role:            message.User,
		Parts:           parts,
		ParentMessageID: parentMessageID,
	})
	if err != nil {
		return message.Message{}, fmt.Errorf("failed to create user message: %w", err)
	}
	return msg, nil
}

func (a *sessionAgent) preparePrompt(msgs []message.Message, supportsImages bool) []fantasy.Message {
	var history []fantasy.Message
	if !a.isSubAgent {
		history = append(history, fantasy.NewUserMessage(
			fmt.Sprintf("<system_reminder>%s</system_reminder>",
				`This is a reminder that your todo list is currently empty. DO NOT mention this to the user explicitly because they are already aware.
If you are working on tasks that would benefit from a todo list please use the "todos" tool to create one.
If not, please feel free to ignore. Again do not mention this message to the user.`,
			),
		))
	}
	// Tool results are emitted right after the assistant message that
	// requested them, not at their stored position; see toolResultIndex.
	results := newToolResultIndex(msgs)

	for _, m := range msgs {
		// Skip empty messages, and assistant messages without content or
		// tool calls (cancelled before they returned anything).
		if len(m.Parts) == 0 || (m.Role == message.Assistant && len(m.ToolCalls()) == 0 && m.Content().Text == "" && m.ReasoningContent().String() == "") {
			continue
		}
		switch m.Role {
		case message.Tool:
			// Tool results are emitted right after their assistant message.
		case message.Assistant:
			history = append(history, m.ToAIMessage()...)
			if len(m.ToolCalls()) > 0 {
				history = append(history, results.take(m))
			}
		default:
			history = append(history, m.ToAIMessage()...)
		}
	}

	if !supportsImages {
		for i := range history {
			if history[i].Role == fantasy.MessageRoleUser {
				history[i].Content = filterFileParts(history[i].Content)
			}
		}
	}
	return history
}

// attachmentFiles returns the binary attachments as file parts. Text
// attachments are sent in the prompt text instead.
func attachmentFiles(attachments []message.Attachment) []fantasy.FilePart {
	var files []fantasy.FilePart
	for _, attachment := range attachments {
		if attachment.IsText() {
			continue
		}
		files = append(files, fantasy.FilePart{
			Filename:  attachment.FileName,
			Data:      attachment.Content,
			MediaType: attachment.MimeType,
		})
	}
	return files
}

// filterFileParts removes fantasy.FilePart entries from a slice of message
// parts. Used to strip image attachments from historical user messages when
// the current model does not support them.
func filterFileParts(parts []fantasy.MessagePart) []fantasy.MessagePart {
	filtered := make([]fantasy.MessagePart, 0, len(parts))
	for _, part := range parts {
		if _, ok := fantasy.AsMessagePart[fantasy.FilePart](part); ok {
			continue
		}
		filtered = append(filtered, part)
	}
	return filtered
}

// toolResultIndex holds a transcript's tool results by the ID of the call
// they answer. Messages can be written to a session concurrently (e.g.
// resuming while a tool is still running), which interleaves user messages
// between a tool call and its result, or stores a result before its call.
// LLM APIs require every tool call to be followed by its results before any
// other message, and strict-adjacency providers (e.g. Kimi, DeepSeek)
// reject the request otherwise, permanently locking the session. So results
// are re-emitted next to the assistant message that requested them.
type toolResultIndex map[string][]fantasy.MessagePart

// newToolResultIndex indexes the tool results in msgs, dropping any result
// whose call is not in msgs: an orphaned result fails API validation on
// every later turn.
func newToolResultIndex(msgs []message.Message) toolResultIndex {
	knownCalls := make(map[string]struct{})
	var toolMsgs []fantasy.Message
	for _, m := range msgs {
		switch m.Role {
		case message.Assistant:
			for _, tc := range m.ToolCalls() {
				knownCalls[tc.ID] = struct{}{}
			}
		case message.Tool:
			toolMsgs = append(toolMsgs, m.ToAIMessage()...)
		}
	}
	index := make(toolResultIndex)
	for _, aiMsg := range toolMsgs {
		for _, part := range aiMsg.Content {
			tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
			_, known := knownCalls[tr.ToolCallID]
			switch {
			case !ok:
				// Tool-role ToAIMessage only emits ToolResultParts today;
				// log so unexpected parts do not vanish silently.
				slog.Warn(
					"Dropping unexpected non-tool-result part from tool message",
					"part_type", fmt.Sprintf("%T", part),
				)
			case !known:
				slog.Warn(
					"Dropping orphaned tool result with no matching tool call",
					"tool_call_id", tr.ToolCallID,
				)
			default:
				index[tr.ToolCallID] = append(index[tr.ToolCallID], part)
			}
		}
	}
	return index
}

// take builds the tool message that must immediately follow the assistant
// message m, removing its results from the index so a result stored in a
// message that also holds results for other assistants' calls is emitted
// exactly once. Tool calls without any stored result (e.g. an interrupted
// session) receive a synthetic error response so the conversation keeps
// working.
func (idx toolResultIndex) take(m message.Message) fantasy.Message {
	content := make([]fantasy.MessagePart, 0, len(m.ToolCalls()))
	for _, tc := range m.ToolCalls() {
		parts := idx[tc.ID]
		delete(idx, tc.ID)
		if len(parts) > 0 {
			content = append(content, parts...)
			continue
		}
		slog.Warn("Injecting synthetic tool result for orphaned tool call",
			"tool_call_id", tc.ID,
			"tool_name", tc.Name,
		)
		content = append(content, fantasy.ToolResultPart{
			ToolCallID: tc.ID,
			Output: fantasy.ToolResultOutputContentError{
				Error: errors.New("tool call was interrupted and did not produce a result, you may retry this call if the result is still needed"),
			},
		})
	}
	return fantasy.Message{
		Role:    fantasy.MessageRoleTool,
		Content: content,
	}
}

func (a *sessionAgent) getSessionMessages(ctx context.Context, sess session.Session) (filtered, raw []message.Message, err error) {
	if sess.LeafMessageID == "" {
		return nil, nil, nil
	}

	raw, err = a.messages.GetBranchPath(ctx, sess.LeafMessageID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get branch path: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil, nil
	}

	return message.FilterBranchPathForContext(raw), raw, nil
}

// maxTitleConversationChars is the maximum number of characters from the
// conversation to include in the title generation prompt.
const maxTitleConversationChars = 4000

// formatConversationForTitle serializes messages as "role: content" lines,
// including only user and assistant text messages. The result is truncated
// to maxTitleConversationChars.
func formatConversationForTitle(msgs []message.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role != message.User && m.Role != message.Assistant {
			continue
		}
		text := strings.TrimSpace(m.Content().Text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", m.Role, text)
		if b.Len() >= maxTitleConversationChars {
			break
		}
	}
	result := b.String()
	if len(result) > maxTitleConversationChars {
		// Truncate to rune boundary to avoid splitting multi-byte characters.
		runes := []rune(result)
		truncated := make([]rune, 0, len(runes))
		size := 0
		for _, r := range runes {
			size += utf8.RuneLen(r)
			if size > maxTitleConversationChars {
				break
			}
			truncated = append(truncated, r)
		}
		result = string(truncated)
	}
	return result
}

// completeSmallMaxTokens bounds a one-shot small-model reply from a model
// that doesn't reason.
const completeSmallMaxTokens = 1024

// completeSmall sends one prompt to the small model outside any session
// and returns its reply and the model's ID.
func (a *sessionAgent) completeSmall(ctx context.Context, system, prompt string) (string, string, error) {
	small := a.smallModel.Get()
	if small.Model == nil {
		return "", "", errors.New("small model not configured")
	}
	tok := int64(completeSmallMaxTokens)
	if small.CatwalkCfg.CanReason {
		tok = max(tok, small.CatwalkCfg.DefaultMaxTokens)
	}
	agent := fantasy.NewAgent(small.Model,
		fantasy.WithSystemPrompt(system),
		fantasy.WithMaxOutputTokens(tok),
		fantasy.WithUserAgent(userAgent),
	)
	call := a.newSideCall(usageKindSmall, "", "", small)
	call.agentName = bouncerReviewerAgentName
	resp, err := agent.Stream(ctx, call.streamCall(prompt))
	if err != nil {
		return "", small.ModelCfg.Model, fmt.Errorf("small model: %w", err)
	}
	return resp.Response.Content.Text(), small.ModelCfg.Model, nil
}

// generateTitle generates a session title from the full conversation
// context. Callers must pre-check TitleIsCustom before calling.
func (a *sessionAgent) generateTitle(ctx context.Context, sessionID string, msgs []message.Message) {
	conversationText := formatConversationForTitle(msgs)
	if conversationText == "" {
		return
	}

	// Ensure the session always gets a title even if every path below
	// fails or the context is cancelled before we finish.
	var titleSaved bool
	defer func() {
		if !titleSaved {
			fallbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := a.sessions.Rename(fallbackCtx, sessionID, DefaultSessionName, false); err != nil {
				slog.Error("Failed to save fallback session title", "error", err)
			}
		}
	}()

	smallModel := a.smallModel.Get()
	largeModel := a.largeModel.Get()
	// The fallback loop below sets the call's attempt and model before each
	// attempt so every attempt that reports usage is recorded against the
	// model that made it.
	call := a.newSideCall(usageKindTitle, sessionID, "", Model{})

	newAgent := func(m fantasy.LanguageModel, p []byte, tok int64) fantasy.Agent {
		return fantasy.NewAgent(m,
			fantasy.WithSystemPrompt(string(p)+"\n /no_think"),
			fantasy.WithMaxOutputTokens(tok),
			fantasy.WithUserAgent(userAgent),
		)
	}

	streamCall := call.streamCall(fmt.Sprintf("Generate a concise title for the following conversation:\n\n%s\n <think>\n\n</think>", conversationText))

	type modelAttempt struct {
		name  string
		model Model
	}
	attempts := []modelAttempt{
		{"small", smallModel},
		{"large", largeModel},
	}

	var resp *fantasy.AgentResult
	var err error
	var model Model
	var success bool
	for i, attempt := range attempts {
		call.attempt, call.model = i, attempt.model
		tok := int64(40)
		if attempt.model.CatwalkCfg.CanReason {
			tok = attempt.model.CatwalkCfg.DefaultMaxTokens
		}
		agent := newAgent(attempt.model.Model, titlePrompt, tok)
		resp, err = agent.Stream(ctx, streamCall)
		if err == nil && resp.Response.FinishReason != fantasy.FinishReasonLength {
			model = attempt.model
			slog.Debug("Generated title with " + attempt.name + " model")
			success = true
			break
		}
		if err != nil {
			slog.Error("Error generating title with "+attempt.name+" model; trying next", "err", err)
		} else {
			slog.Error("Title generation hit token limit with " + attempt.name + " model; trying next")
		}
	}
	if !success {
		// The deferred fallback will save the default session name.
		return
	}

	// Clean up title.
	var title string
	title = strings.ReplaceAll(resp.Response.Content.Text(), "\n", " ")

	// Remove thinking tags if present.
	title = thinkTagRegex.ReplaceAllString(title, "")
	title = orphanThinkTagRegex.ReplaceAllString(title, "")

	title = strings.TrimSpace(title)
	title = cmp.Or(title, DefaultSessionName)

	// Calculate usage and cost.
	cost := usageCost(model, resp.TotalUsage, a.openrouterRunCost(resp.Steps), false)
	promptTokens := contextTokens(resp.TotalUsage)
	completionTokens := resp.TotalUsage.OutputTokens

	// Atomically update only title and usage fields to avoid overriding other
	// concurrent session updates.
	saveErr := a.sessions.UpdateTitleAndUsage(ctx, sessionID, title, false, promptTokens, completionTokens, cost)
	if saveErr != nil {
		slog.Error("Failed to save session title and usage", "error", saveErr)
		return
	}
	titleSaved = true
}

func (a *sessionAgent) openrouterCost(metadata fantasy.ProviderMetadata) *float64 {
	openrouterMetadata, ok := metadata[openrouter.Name]
	if !ok {
		return nil
	}

	opts, ok := openrouterMetadata.(*openrouter.ProviderMetadata)
	if !ok {
		return nil
	}
	return &opts.Usage.Cost
}

// openrouterRunCost sums the cost OpenRouter reported for each step of a
// run, or returns nil when it reported none.
func (a *sessionAgent) openrouterRunCost(steps []fantasy.StepResult) *float64 {
	var total *float64
	for _, step := range steps {
		if stepCost := a.openrouterCost(step.ProviderMetadata); stepCost != nil {
			sum := *stepCost
			if total != nil {
				sum += *total
			}
			total = &sum
		}
	}
	return total
}

// usageCost prices usage at the model's catwalk rates. An override cost
// (e.g. from OpenRouter) replaces the rates, and flat-rate models and
// estimated usage cost nothing.
func usageCost(model Model, usage fantasy.Usage, overrideCost *float64, estimated bool) float64 {
	if estimated || model.FlatRate {
		return 0
	}
	if overrideCost != nil {
		return *overrideCost
	}
	modelConfig := model.CatwalkCfg
	return modelConfig.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		modelConfig.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		modelConfig.CostPer1MIn/1e6*float64(usage.InputTokens) +
		modelConfig.CostPer1MOut/1e6*float64(usage.OutputTokens)
}

func (a *sessionAgent) updateSessionUsage(model Model, session *session.Session, usage fantasy.Usage, overrideCost *float64, estimated bool) {
	if !usageIsZero(usage) {
		session.EstimatedUsage = estimated
	}

	session.Cost += usageCost(model, usage, overrideCost, estimated)
	updateSessionTokenCounters(session, usage)
}

// contextTokens returns the size of the prompt the provider processed for
// a step. Providers with prompt caching (Anthropic, Bedrock, Vercel) report
// the prompt as three disjoint buckets: tokens served from cache, tokens
// newly written to the cache, and the uncached remainder. All three occupy
// the context window, so all three count. Providers without cache writes
// leave CacheCreationTokens at zero, and the OpenAI-style providers already
// subtract cached tokens from InputTokens, so nothing is counted twice.
func contextTokens(usage fantasy.Usage) int64 {
	return usage.InputTokens + usage.CacheReadTokens + usage.CacheCreationTokens
}

func updateSessionTokenCounters(session *session.Session, usage fantasy.Usage) {
	if usage.OutputTokens != 0 {
		session.CompletionTokens = usage.OutputTokens
	}
	if promptTokens := contextTokens(usage); promptTokens != 0 {
		session.PromptTokens = promptTokens
	}
}

func summaryCompletionTokens(usage fantasy.Usage, summaryMessage message.Message) int64 {
	if usage.OutputTokens != 0 {
		return usage.OutputTokens
	}
	return approxTokenCount(summaryMessage.Content().Text) + approxTokenCount(summaryMessage.ReasoningContent().String())
}

func (a *sessionAgent) Cancel(sessionID string) {
	unlock := a.lockDispatch(sessionID)
	a.wakeSuppressed.Set(sessionID, true)
	unlock()

	a.admission.cancel(sessionID)
}

func (a *sessionAgent) ClearQueue(sessionID string) {
	a.admission.clear(sessionID)
}

func (a *sessionAgent) IsSummarizing(sessionID string) bool {
	_, summarizing := a.summarizing.Get(sessionID)
	return summarizing
}

func (a *sessionAgent) CancelAll() {
	a.admission.cancelAll()
}

func (a *sessionAgent) IsBusy() bool {
	return a.admission.busy("")
}

func (a *sessionAgent) IsSessionBusy(sessionID string) bool {
	return a.admission.busy(sessionID)
}

func (a *sessionAgent) QueuedPrompts(sessionID string) int {
	return len(a.admission.queued(sessionID))
}

func (a *sessionAgent) QueuedPromptsList(sessionID string) []string {
	return a.admission.queued(sessionID)
}

func (a *sessionAgent) SetModels(large Model, small Model) {
	a.largeModel.Set(large)
	a.smallModel.Set(small)
}

func (a *sessionAgent) SetProviderConfig(cfg config.ProviderConfig) {
	a.providerConfig.Set(cfg)
}

func (a *sessionAgent) SetTools(tools []fantasy.AgentTool) {
	a.tools.SetSlice(tools)
}

func (a *sessionAgent) SetLazyMCPToolMap(m map[string]string) {
	a.lazyMCPToolMap.Reset(m)
}

func (a *sessionAgent) SetConnectFn(fn tools.ConnectFn) {
	a.connectFn.Set(fn)
}

func (a *sessionAgent) RequestOverhead() int {
	off := tools.NewLazyMCPState(nil)
	lazy := a.lazyMCPToolMap.Copy()
	size := len(a.systemPromptPrefix.Get()) + len(a.systemPrompt.Get()) + len(mcpInstructions(lazy, off))
	for _, tool := range filterLazyMCPTools(a.tools.Copy(), lazy, off) {
		info := tool.Info()
		schema, err := json.Marshal(struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Parameters  any      `json:"parameters"`
			Required    []string `json:"required"`
		}{info.Name, info.Description, info.Parameters, info.Required})
		if err != nil {
			// Unencodable parameters can't be sent either; count what is
			// known.
			size += len(info.Name) + len(info.Description)
			continue
		}
		size += len(schema)
	}
	return size
}

func (a *sessionAgent) SetSystemPrompt(systemPrompt string) {
	a.systemPrompt.Set(systemPrompt)
}

func (a *sessionAgent) Model() Model {
	return a.largeModel.Get()
}

// convertToToolResult converts a fantasy tool result to a message tool result.
func (a *sessionAgent) convertToToolResult(result fantasy.ToolResultContent) message.ToolResult {
	baseResult := message.ToolResult{
		ToolCallID: result.ToolCallID,
		Name:       result.ToolName,
		Metadata:   result.ClientMetadata,
	}

	switch result.Result.GetType() {
	case fantasy.ToolResultContentTypeText:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](result.Result); ok {
			baseResult.Content = r.Text
		}
	case fantasy.ToolResultContentTypeError:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result.Result); ok {
			baseResult.Content = r.Error.Error()
			baseResult.IsError = true
		}
	case fantasy.ToolResultContentTypeMedia:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](result.Result); ok {
			if !stringext.IsValidBase64(r.Data) {
				slog.Warn("Tool returned media with invalid base64 data, discarding image",
					"tool", result.ToolName,
					"tool_call_id", result.ToolCallID,
				)
				baseResult.Content = "Tool returned image data with invalid encoding"
				baseResult.IsError = true
			} else {
				content := r.Text
				if content == "" {
					content = message.MediaPlaceholder(r.MediaType)
				}
				baseResult.Content = content
				baseResult.Data = r.Data
				baseResult.MIMEType = r.MediaType
			}
		}
	}

	return baseResult
}

// workaroundProviderMediaLimitations converts media content in tool results to
// user messages for providers that don't natively support images in tool results.
//
// Problem: OpenAI, Google, OpenRouter, and other OpenAI-compatible providers
// don't support sending images/media in tool result messages - they only accept
// text in tool results. However, they DO support images in user messages.
//
// If we send media in tool results to these providers, the API returns an error.
//
// Solution: For these providers, we:
//  1. Replace the media in the tool result with a text placeholder
//  2. Inject a user message immediately after with the image as a file attachment
//  3. This maintains the tool execution flow while working around API limitations
//
// Anthropic and Bedrock support images natively in tool results, so we skip
// this workaround for them.
//
// Example transformation:
//
//	BEFORE: [tool result: image data]
//	AFTER:  [tool result: "Image loaded - see attached"], [user: image attachment]
func (a *sessionAgent) workaroundProviderMediaLimitations(messages []fantasy.Message, largeModel Model) []fantasy.Message {
	providerSupportsMedia := largeModel.ModelCfg.Provider == string(catwalk.InferenceProviderAnthropic) ||
		largeModel.ModelCfg.Provider == string(catwalk.InferenceProviderBedrock) ||
		largeModel.ModelCfg.Provider == string(catwalk.InferenceProviderBedrockEurope)

	if providerSupportsMedia {
		return messages
	}

	supportsImages := largeModel.CatwalkCfg.SupportsImages

	convertedMessages := make([]fantasy.Message, 0, len(messages))

	for _, msg := range messages {
		if msg.Role != fantasy.MessageRoleTool {
			convertedMessages = append(convertedMessages, msg)
			continue
		}

		textParts := make([]fantasy.MessagePart, 0, len(msg.Content))
		var mediaFiles []fantasy.FilePart

		for _, part := range msg.Content {
			toolResult, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
			if !ok {
				textParts = append(textParts, part)
				continue
			}

			if media, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](toolResult.Output); ok {
				if !supportsImages {
					// Model cannot process images. Replace with a text
					// placeholder and skip creating a synthetic user
					// message with FilePart, which would brick the
					// session on text-only models.
					textParts = append(textParts, fantasy.ToolResultPart{
						ToolCallID: toolResult.ToolCallID,
						Output: fantasy.ToolResultOutputContentText{
							Text: "[Image/media content not supported by this model]",
						},
						ProviderOptions: toolResult.ProviderOptions,
					})
					continue
				}

				decoded, err := base64.StdEncoding.DecodeString(media.Data)
				if err != nil {
					slog.Warn("Failed to decode media data", "error", err)
					textParts = append(textParts, part)
					continue
				}

				mediaFiles = append(mediaFiles, fantasy.FilePart{
					Data:      decoded,
					MediaType: media.MediaType,
					Filename:  fmt.Sprintf("tool-result-%s", toolResult.ToolCallID),
				})

				textParts = append(textParts, fantasy.ToolResultPart{
					ToolCallID: toolResult.ToolCallID,
					Output: fantasy.ToolResultOutputContentText{
						Text: "[Image/media content loaded - see attached file]",
					},
					ProviderOptions: toolResult.ProviderOptions,
				})
			} else {
				textParts = append(textParts, part)
			}
		}

		convertedMessages = append(convertedMessages, fantasy.Message{
			Role:    fantasy.MessageRoleTool,
			Content: textParts,
		})

		if len(mediaFiles) > 0 {
			convertedMessages = append(convertedMessages, fantasy.NewUserMessage(
				"Here is the media content from the tool result:",
				mediaFiles...,
			))
		}
	}

	return convertedMessages
}

// buildSummaryPrompt constructs the prompt text for session summarization.
func buildSummaryPrompt(todos []session.Todo) string {
	var sb strings.Builder
	sb.WriteString("Provide a detailed summary of our conversation above.")
	if len(todos) > 0 {
		sb.WriteString("\n\n## Current Todo List\n\n")
		for _, t := range todos {
			fmt.Fprintf(&sb, "- [%s] %s\n", t.Status, t.Content)
		}
		sb.WriteString("\nInclude these tasks and their statuses in your summary. ")
		sb.WriteString("Instruct the resuming assistant to use the `todos` tool to continue tracking progress on these tasks.")
	}
	return sb.String()
}

func providerRetryLogFields(err *fantasy.ProviderError, delay time.Duration) []any {
	fields := []any{
		"retry_delay", delay.String(),
	}
	if err == nil {
		return fields
	}
	fields = append(fields, "status_code", err.StatusCode)
	if err.Title != "" {
		fields = append(fields, "title", err.Title)
	}
	if err.Message != "" {
		fields = append(fields, "message", err.Message)
	}
	return fields
}

// sanitizeToolInput validates tool call JSON from the provider.
// Malformed input is replaced with an empty object to prevent
// stuck conversations from truncated or malformed model output.
// The second return value indicates whether sanitization occurred.
func sanitizeToolInput(toolName, toolCallID, input string) (string, bool) {
	if !json.Valid([]byte(input)) {
		slog.Warn("Malformed tool call JSON from provider, replacing with empty object",
			"tool", toolName,
			"id", toolCallID,
			"input_len", len(input),
		)
		return "{}", true
	}
	return input, false
}
