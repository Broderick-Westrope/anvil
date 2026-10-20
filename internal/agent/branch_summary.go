package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/Broderick-Westrope/anvil/internal/agent/branchsummary"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
)

// BuiltinBranchSummarizer is the summarizer agent Anvil ships.
const BuiltinBranchSummarizer = "branch-summarizer"

const (
	// defaultSummarizerContextWindow is assumed when the summarizer's model
	// doesn't report a context window.
	defaultSummarizerContextWindow = 128_000
	// toolLoopShare is the share of the window (one in toolLoopShare)
	// kept free for the tool calls and results of a summarizer with tools.
	toolLoopShare = 8
	// minSummarizerInputTokens is the least room worth sending a branch
	// in; with less, the model would see almost nothing.
	minSummarizerInputTokens = 4_000
)

// readOnlySummarizerTools are the most a summarizer may use. It reads a
// branch's untrusted content, so it gets no tool that changes anything.
var readOnlySummarizerTools = []string{tools.ViewToolName, tools.GrepToolName, tools.GlobToolName, tools.LSToolName}

var (
	// ErrNothingToSummarize means the abandoned range has no assistant
	// reply, so there is nothing a summary could say.
	ErrNothingToSummarize = errors.New("nothing to summarize")
	// ErrStaleBranchSummary means the session moved away from the rewind
	// target while the summary was generating, so it was thrown away.
	ErrStaleBranchSummary = errors.New("conversation moved on before the summary finished")
	// ErrNotABranchSummarizer means the requested agent isn't marked
	// branch_summarizer, so it may not be run over a branch.
	ErrNotABranchSummarizer = errors.New("agent is not a branch summarizer")
	// ErrBranchSummaryInUse means a message was sent after the summary,
	// so it can no longer be discarded.
	ErrBranchSummaryInUse = errors.New("branch summary is already part of the conversation")
)

// BranchSummaryRequest asks for a summary of the branch the user rewound
// from.
type BranchSummaryRequest struct {
	SessionID string
	// FromLeafID is the leaf the user rewound from.
	FromLeafID string
	// TargetID is the message the user rewound to, which the summary is
	// stored under. Empty means the root of the conversation.
	TargetID string
	Agent    string
	Focus    string
}

// BranchSummarizers lists the agents that can summarize branches: marked
// agents whose model resolves, the built-in first and the rest by name.
func (c *coordinator) BranchSummarizers() []string {
	c.orchestratorMu.RLock()
	defer c.orchestratorMu.RUnlock()
	var names []string
	for name, md := range c.agentMDs {
		agentCfg, ok := c.agentConfigs[name]
		if !md.BranchSummarizer || !ok {
			continue
		}
		_, err := config.ResolveAgentModel(agentCfg, c.cfg.Config())
		if err != nil {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	if i := slices.Index(names, BuiltinBranchSummarizer); i > 0 {
		names = slices.Insert(slices.Delete(names, i, i+1), 0, BuiltinBranchSummarizer)
	}
	return names
}

// SummarizeBranch runs a summarizer agent over the branch between
// req.FromLeafID and req.TargetID, and stores its summary as a child of
// the target. It holds the session for the whole run, so a message sent
// meanwhile queues and lands after the summary. If the session's leaf has
// moved off the target by the time the summary is ready, nothing is
// stored. On any failure the summarizer's child session is deleted. The
// summarizer runs with only the read-only tools its agent allows.
func (c *coordinator) SummarizeBranch(ctx context.Context, req BranchSummaryRequest) (message.Message, error) {
	c.orchestratorMu.RLock()
	summarizer := c.agentMDs[req.Agent].BranchSummarizer
	agentCfg := c.agentConfigs[req.Agent]
	c.orchestratorMu.RUnlock()
	if !summarizer {
		return message.Message{}, fmt.Errorf("summarize branch with %q: %w", req.Agent, ErrNotABranchSummarizer)
	}
	release, err := c.Hold(ctx, []string{req.SessionID})
	if err != nil {
		return message.Message{}, err
	}
	defer release()

	// A send that got in before the hold has moved the leaf, and the
	// summary could never be stored, so it isn't worth paying for.
	sess, err := c.sessions.Get(ctx, req.SessionID)
	if err != nil {
		return message.Message{}, fmt.Errorf("get session %s: %w", req.SessionID, err)
	}
	if sess.LeafMessageID != req.TargetID {
		return message.Message{}, ErrStaleBranchSummary
	}

	rng, err := c.abandonedRange(ctx, req.FromLeafID, req.TargetID)
	if err != nil {
		return message.Message{}, err
	}
	if !branchsummary.Worthwhile(rng) {
		return message.Message{}, ErrNothingToSummarize
	}
	ledger := branchsummary.Ledger(rng, func(messageID string, call message.ToolCall) []message.Message {
		return c.delegatedMessages(ctx, messageID, call)
	})

	call := WorkflowAgentCall{
		ParentSessionID: req.SessionID,
		Agent:           req.Agent,
		Title:           "Branch summary",
		SessionID:       uuid.NewString(),
		Tools:           summarizerTools(agentCfg, c.cfg.Config().Options),
	}
	// The budget is measured on the agent that runs, with its restricted
	// tools.
	sub, err := c.getOrBuild(summarizerCacheKey(req.Agent, call.Tools), req.Agent, func(agentCfg config.Agent) (SessionAgent, error) {
		sub, _, err := c.buildWorkflowAgent(ctx, call, agentCfg)
		return sub, err
	})
	if err != nil {
		return message.Message{}, err
	}
	budget, err := summarizerBudget(sub.Model(), sub.RequestOverhead(), len(call.Tools) > 0)
	if err != nil {
		return message.Message{}, err
	}
	call.Prompt = branchsummary.Prompt(rng, ledger, req.Focus, budget)

	childID := call.SessionID
	// The summarizer acts for the root session, as a task subagent would.
	runCtx := context.WithValue(ctx, tools.RootSessionIDContextKey, req.SessionID)
	out, err := c.runWorkflowAgent(runCtx, call, sub, nil)
	if err == nil && strings.TrimSpace(out.Text) == "" {
		err = errors.New("summarizer returned no summary")
	}
	if err != nil {
		c.deleteChildSession(childID)
		return message.Message{}, err
	}

	// Stored only if the leaf is still the target, checked in the same
	// transaction, so a rewind can't land between the check and the
	// insert.
	summary, err := c.messages.CreateAtLeaf(ctx, req.SessionID, req.TargetID, message.CreateMessageParams{
		Role:            message.User,
		MessageType:     message.MessageTypeBranchSummary,
		ParentMessageID: req.TargetID,
		Parts: []message.ContentPart{message.BranchSummaryContent{
			Summary:        branchsummary.Finalize(out.Text, ledger),
			FromID:         req.FromLeafID,
			ChildSessionID: childID,
			Agent:          req.Agent,
			Ledger:         ledger,
		}},
	})
	if errors.Is(err, message.ErrLeafMoved) {
		err = ErrStaleBranchSummary
	}
	if err != nil {
		c.deleteChildSession(childID)
		return message.Message{}, fmt.Errorf("store branch summary: %w", err)
	}
	c.refreshSession(req.SessionID)
	return summary, nil
}

// DiscardBranchSummary deletes a branch summary and its child session.
// When the summary is the session's leaf, the leaf moves back to the
// summary's parent in the same transaction. It is refused once a message
// has been sent after the summary.
func (c *coordinator) DiscardBranchSummary(ctx context.Context, sessionID, summaryID string) error {
	// Holding waits out a send that has read the leaf but not yet stored
	// its message, so the delete sees it as a child.
	release, err := c.Hold(ctx, []string{sessionID})
	if err != nil {
		return err
	}
	defer release()

	summary, err := c.messages.Get(ctx, summaryID)
	if err != nil {
		return fmt.Errorf("get branch summary %s: %w", summaryID, err)
	}
	if summary.MessageType != message.MessageTypeBranchSummary || summary.SessionID != sessionID {
		return fmt.Errorf("message %s is not a branch summary in session %s", summaryID, sessionID)
	}
	err = c.messages.DeleteChildless(ctx, sessionID, summaryID)
	if err != nil {
		if errors.Is(err, message.ErrHasChildren) {
			return ErrBranchSummaryInUse
		}
		return fmt.Errorf("delete branch summary %s: %w", summaryID, err)
	}
	c.refreshSession(sessionID)
	for _, p := range summary.Parts {
		if s, ok := p.(message.BranchSummaryContent); ok {
			c.deleteChildSession(s.ChildSessionID)
		}
	}
	return nil
}

// BranchWorthSummarizing reports whether rewinding from fromLeaf to target
// leaves anything a summary could say.
func (c *coordinator) BranchWorthSummarizing(ctx context.Context, fromLeaf, target string) (bool, error) {
	rng, err := c.abandonedRange(ctx, fromLeaf, target)
	if err != nil {
		return false, err
	}
	return branchsummary.Worthwhile(rng), nil
}

// abandonedRange returns the messages on the path to fromLeaf that aren't
// on the path to target.
func (c *coordinator) abandonedRange(ctx context.Context, fromLeaf, target string) ([]message.Message, error) {
	fromPath, err := c.messages.GetBranchPath(ctx, fromLeaf)
	if err != nil {
		return nil, fmt.Errorf("get branch path for %s: %w", fromLeaf, err)
	}
	var targetPath []message.Message
	if target != "" {
		if targetPath, err = c.messages.GetBranchPath(ctx, target); err != nil {
			return nil, fmt.Errorf("get branch path for %s: %w", target, err)
		}
	}
	return branchsummary.Abandoned(fromPath, targetPath), nil
}

// delegatedMessages returns the messages of the child session a task or
// agentic_fetch call ran in, or nil for any other call.
func (c *coordinator) delegatedMessages(ctx context.Context, messageID string, call message.ToolCall) []message.Message {
	if call.Name != TaskToolName && call.Name != tools.AgenticFetchToolName {
		return nil
	}
	msgs, err := c.messages.List(ctx, c.sessions.CreateAgentToolSessionID(messageID, call.ID))
	if err != nil {
		slog.Warn("Failed to load delegated session for branch ledger", "tool_call", call.ID, "error", err)
		return nil
	}
	return msgs
}

// refreshSession publishes the session after the message store moved its
// leaf, so the UI's copy, which later messages are parented on, follows.
func (c *coordinator) refreshSession(id string) {
	err := c.sessions.Refresh(context.Background(), id)
	if err != nil {
		slog.Warn("Failed to publish session after a branch summary change", "session", id, "error", err)
	}
}

func (c *coordinator) deleteChildSession(id string) {
	if id == "" {
		return
	}
	err := c.sessions.Delete(context.Background(), id)
	if err != nil {
		slog.Warn("Failed to delete branch summary session", "session", id, "error", err)
	}
}

// summarizerTools is the read-only tools agentCfg allows. It is never nil:
// empty means no tools.
func summarizerTools(agentCfg config.Agent, opts *config.Options) []string {
	// An invalid filter allows every tool, and the read-only set still caps it.
	access, _ := newToolAccess(agentCfg, opts)
	allowed := []string{}
	for _, name := range readOnlySummarizerTools {
		if access.allowsTool(name) {
			allowed = append(allowed, name)
		}
	}
	return allowed
}

// summarizerCacheKey caches a summarizer per tool set, apart from the
// agent's unrestricted instance.
func summarizerCacheKey(agentName string, toolNames []string) string {
	return fmt.Sprintf("%s|%d|tools=%s", agentName, workflowAgentDepth, strings.Join(toolNames, ","))
}

// summarizerBudget is how many tokens of branch the summarizer's model
// can take. It subtracts the model's output, the agent's measured request
// overhead (overheadChars, see SessionAgent.RequestOverhead) and, for an
// agent with tools, room for its tool loop. When too little is left, it
// fails, so nothing is sent.
func summarizerBudget(model Model, overheadChars int, hasTools bool) (int, error) {
	window := int(model.CatwalkCfg.ContextWindow)
	if window == 0 {
		window = defaultSummarizerContextWindow
	}
	output := int(cmp.Or(model.ModelCfg.MaxTokens, model.CatwalkCfg.DefaultMaxTokens))
	overhead := branchsummary.Tokens(overheadChars)
	budget := window - output - overhead
	if hasTools {
		budget -= window / toolLoopShare
	}
	if budget < minSummarizerInputTokens {
		return 0, fmt.Errorf("summarizer on %s: its %d-token prompt and tools and %d-token output leave only %d of the %d-token window for the branch",
			model.CatwalkCfg.ID, overhead, output, max(budget, 0), window)
	}
	return budget, nil
}
