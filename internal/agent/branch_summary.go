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
	"github.com/Broderick-Westrope/anvil/internal/session"
)

// BuiltinBranchSummarizer is the summarizer agent Anvil ships.
const BuiltinBranchSummarizer = "branch-summarizer"

const (
	// defaultSummarizerContextWindow is assumed when the summarizer's model
	// doesn't report a context window.
	defaultSummarizerContextWindow = 128_000
	// summarizerOverheadTokens reserves room for the summarizer's system
	// prompt, tool schemas and the tool calls it may make.
	summarizerOverheadTokens = 16_000
	// minSummarizerInputTokens is the least room worth sending a branch
	// in; with less, the model would see almost nothing.
	minSummarizerInputTokens = 4_000
)

var (
	// ErrNothingToSummarize means the abandoned range has no assistant
	// reply, so there is nothing a summary could say.
	ErrNothingToSummarize = errors.New("nothing to summarize")
	// ErrStaleBranchSummary means the session moved away from the rewind
	// target while the summary was generating, so it was thrown away.
	ErrStaleBranchSummary = errors.New("conversation moved on before the summary finished")
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
	// ChildSessionID, when set, is the ID of the session the summarizer
	// runs in, so a caller can point at it before the summary finishes.
	// Otherwise one is generated.
	ChildSessionID string
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
		if _, err := config.ResolveAgentModel(agentCfg, c.cfg.Config()); err != nil {
			continue
		}
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int {
		return cmp.Or(
			-cmp.Compare(boolInt(a == BuiltinBranchSummarizer), boolInt(b == BuiltinBranchSummarizer)),
			strings.Compare(a, b),
		)
	})
	return names
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SummarizeBranch runs a summarizer agent over the branch between
// req.FromLeafID and req.TargetID, and stores its summary as a child of
// the target. It holds the session for the whole run, so a message sent
// meanwhile queues and lands after the summary. If the session's leaf has
// moved off the target by the time the summary is ready, nothing is
// stored. On any failure the summarizer's child session is deleted.
func (c *coordinator) SummarizeBranch(ctx context.Context, req BranchSummaryRequest) (message.Message, error) {
	release, err := c.Hold(ctx, []string{req.SessionID})
	if err != nil {
		return message.Message{}, err
	}
	defer release()

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

	sub, err := c.getOrBuildAgent(ctx, req.Agent, workflowAgentDepth, "")
	if err != nil {
		return message.Message{}, err
	}
	budget, err := summarizerBudget(sub.Model())
	if err != nil {
		return message.Message{}, err
	}

	childID := cmp.Or(req.ChildSessionID, uuid.NewString())
	// The summarizer acts for the root session, as a task subagent would.
	runCtx := context.WithValue(ctx, tools.RootSessionIDContextKey, req.SessionID)
	out, err := c.RunWorkflowAgent(runCtx, WorkflowAgentCall{
		ParentSessionID: req.SessionID,
		Agent:           req.Agent,
		Prompt:          branchsummary.Prompt(rng, ledger, req.Focus, budget),
		Title:           "Branch summary",
		SessionID:       childID,
	})
	if err == nil && strings.TrimSpace(out.Text) == "" {
		err = errors.New("summarizer returned no summary")
	}
	if err == nil {
		var sess session.Session
		if sess, err = c.sessions.Get(ctx, req.SessionID); err == nil && sess.LeafMessageID != req.TargetID {
			err = ErrStaleBranchSummary
		}
	}
	if err != nil {
		c.deleteChildSession(childID)
		return message.Message{}, err
	}

	summary, err := c.messages.Create(ctx, req.SessionID, message.CreateMessageParams{
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
	if err != nil {
		c.deleteChildSession(childID)
		return message.Message{}, fmt.Errorf("store branch summary: %w", err)
	}
	return summary, nil
}

// DiscardBranchSummary deletes a branch summary and its child session.
// When the summary is the session's leaf, the leaf moves back to the
// summary's parent. It is refused once a message has been sent after the
// summary.
func (c *coordinator) DiscardBranchSummary(ctx context.Context, sessionID, summaryID string) error {
	// Holding waits out a send that has read the leaf but not yet stored
	// its message, so the checks below see it.
	release, err := c.Hold(ctx, []string{sessionID})
	if err != nil {
		return err
	}
	defer release()

	sess, err := c.sessions.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get session %s: %w", sessionID, err)
	}
	children, err := c.messages.GetChildren(ctx, summaryID)
	if err != nil {
		return fmt.Errorf("get children of %s: %w", summaryID, err)
	}
	if len(children) > 0 {
		return ErrBranchSummaryInUse
	}
	summary, err := c.messages.Get(ctx, summaryID)
	if err != nil {
		return fmt.Errorf("get branch summary %s: %w", summaryID, err)
	}
	if summary.MessageType != message.MessageTypeBranchSummary {
		return fmt.Errorf("message %s is not a branch summary", summaryID)
	}
	if sess.LeafMessageID == summaryID {
		if err := c.sessions.MoveLeaf(ctx, sessionID, summary.ParentMessageID); err != nil {
			return fmt.Errorf("move leaf: %w", err)
		}
	}
	if err := c.messages.Delete(ctx, summaryID); err != nil {
		return fmt.Errorf("delete branch summary %s: %w", summaryID, err)
	}
	for _, p := range summary.Parts {
		if s, ok := p.(message.BranchSummaryContent); ok {
			c.deleteChildSession(s.ChildSessionID)
		}
	}
	return nil
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

func (c *coordinator) deleteChildSession(id string) {
	if id == "" {
		return
	}
	if err := c.sessions.Delete(context.Background(), id); err != nil {
		slog.Warn("Failed to delete branch summary session", "session", id, "error", err)
	}
}

// summarizerBudget is how many tokens of branch the summarizer's model
// can take, after its overhead and output.
func summarizerBudget(model Model) (int, error) {
	window := int(model.CatwalkCfg.ContextWindow)
	if window == 0 {
		window = defaultSummarizerContextWindow
	}
	output := int(cmp.Or(model.ModelCfg.MaxTokens, model.CatwalkCfg.DefaultMaxTokens))
	budget := window - output - summarizerOverheadTokens
	if budget < minSummarizerInputTokens {
		return 0, fmt.Errorf("summarizer model %s has a %d-token context window, too small to summarize a branch", model.CatwalkCfg.ID, window)
	}
	return budget, nil
}
