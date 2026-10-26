package agent

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/Broderick-Westrope/anvil/internal/message"
)

const diagramFixText = "Some diagrams in your last reply don't render, so the chat shows them as plain code. " +
	"Reply with only the corrected diagrams, each in its own mermaid fence, and check them with validate_mermaid first. " +
	"Don't repeat the rest of the reply.\n\n"

func (a *sessionAgent) checkReplyDiagrams(ctx context.Context, call SessionAgentCall, reply *message.Message) {
	if !a.checkDiagrams || call.NonInteractive || reply == nil || reply.FinishReason() != message.FinishReasonEndTurn {
		return
	}
	if slices.ContainsFunc(call.Injections, func(inj message.InjectionContent) bool {
		return inj.Kind == message.InjectionKindDiagramFix
	}) {
		return
	}
	text := reply.Content().Text
	if !strings.Contains(text, "mermaid") {
		return
	}
	report, err := mermaid.CheckMarkdown(ctx, text)
	if err != nil {
		slog.Warn("Failed to check reply diagrams", "session_id", call.SessionID, "error", err)
		return
	}
	if len(report.Problems) == 0 {
		return
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
	a.admission.continueWith(ctx, call.SessionID, submission{prompt: fix.Injections[0].Text, run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		return a.Run(ctx, fix)
	}})
}
