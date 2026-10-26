package agent

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/Broderick-Westrope/anvil/internal/message"
)

const replyCheckTimeout = 2 * time.Second

const diagramFixText = "Some diagrams in your last reply don't render, so the chat shows them as plain code. " +
	"Reply with only the corrected diagrams, each in its own mermaid fence, and check them with validate_mermaid first. " +
	"Don't repeat the rest of the reply. Line numbers count from the start of your reply.\n\n"

func (a *sessionAgent) diagramFix(ctx context.Context, t *turn) (SessionAgentCall, bool) {
	call, reply := t.call, t.assistant
	if !a.checkDiagrams || t.shouldSummarize || call.NonInteractive || call.Await || reply == nil || reply.FinishReason() != message.FinishReasonEndTurn {
		return SessionAgentCall{}, false
	}
	if slices.ContainsFunc(call.Injections, func(inj message.InjectionContent) bool {
		return inj.Kind == message.InjectionKindDiagramFix
	}) {
		return SessionAgentCall{}, false
	}
	text := reply.Content().Text
	if !strings.Contains(text, "mermaid") {
		return SessionAgentCall{}, false
	}
	checkCtx, cancel := context.WithTimeout(ctx, replyCheckTimeout)
	defer cancel()
	report, err := mermaid.CheckMarkdown(checkCtx, text)
	if err != nil {
		slog.Warn("Failed to check reply diagrams", "session_id", call.SessionID, "error", err)
		return SessionAgentCall{}, false
	}
	report = report.Conclusive()
	if len(report.Problems) == 0 {
		return SessionAgentCall{}, false
	}
	fix := call
	fix.state = nil
	fix.wake = false
	fix.Prompt = ""
	fix.Attachments = nil
	fix.Injections = []message.InjectionContent{{
		Source: message.InjectionSourceHarness,
		Kind:   message.InjectionKindDiagramFix,
		Text:   diagramFixText + report.String(),
	}}
	return fix, a.admission.ownsWithNothingQueued(ctx, call.SessionID)
}

func (a *sessionAgent) runDiagramFix(ctx context.Context, t *turn, fix SessionAgentCall) (*fantasy.AgentResult, error) {
	t.endStep()
	if err := a.messages.FlushAll(t.persistCtx); err != nil {
		return nil, localPersistence(t.persistCtx, err)
	}
	return a.Run(ctx, fix)
}
