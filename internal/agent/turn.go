package agent

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/stringext"
)

// turn holds the state of one streamed model run: the session and branch
// leaf it writes to, the assistant message of the current step, and the
// usage capture. Its methods are the fantasy stream callbacks.
type turn struct {
	a              *sessionAgent
	call           SessionAgentCall
	largeModel     Model
	providerCfg    config.ProviderConfig
	promptPrefix   string
	lazyMCPToolMap map[string]string
	lazyState      *tools.LazyMCPState
	injected       *injectedMessages
	runID          string
	// parentSessionID is set for child sessions.
	parentSessionID string

	// genCtx is the request context. persistCtx survives cancellation and
	// is used for persistence that must succeed even when the request is
	// canceled (finish parts, error tool results, leaf sync). Using the
	// plain parent ctx is not enough: for subagents the parent ctx is the
	// root session's generation context, which is canceled by the same
	// escape press. Writes would fail, the child's messages would never be
	// marked canceled, and drilled-in views would keep spinning forever.
	genCtx     context.Context
	persistCtx context.Context

	// mu guards the fields below it, which parallel tool-execution
	// goroutines and the stream callbacks both touch.
	mu           sync.Mutex
	session      session.Session
	leaf         string
	stepMessages []fantasy.Message
	capture      *stepCapture
	prevTurn     turnPrefix
	havePrevTurn bool

	// assistant is the current step's assistant message. PrepareStep sets
	// it and the stream callbacks, which fantasy calls in order, update it.
	assistant *message.Message

	sanitizedMu sync.Mutex
	sanitized   map[string]bool

	shouldSummarize bool
}

func (t *turn) currentLeaf() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.leaf
}

func (t *turn) setLeaf(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.leaf = id
}

func (t *turn) sessionTitle() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.session.Title
}

// newAgent builds the model agent for this turn. Lazy MCP tools that
// aren't enabled are left out, along with their server instructions.
func (t *turn) newAgent(agentTools []fantasy.AgentTool, systemPrompt string) fantasy.Agent {
	agentTools = filterLazyMCPTools(agentTools, t.lazyMCPToolMap, t.lazyState)
	if s := mcpInstructions(t.lazyMCPToolMap, t.lazyState); s != "" {
		systemPrompt += "\n\n<mcp-instructions>\n" + s + "\n</mcp-instructions>"
	}
	if len(agentTools) > 0 {
		// Add Anthropic caching to the last tool.
		agentTools[len(agentTools)-1].SetProviderOptions(t.a.getCacheControlOptions())
	}
	return fantasy.NewAgent(
		t.largeModel.Model,
		fantasy.WithSystemPrompt(systemPrompt),
		fantasy.WithTools(agentTools...),
		fantasy.WithUserAgent(userAgent),
	)
}

// start adds the message this turn begins from to msgs and moves the leaf
// to it: background job notices for a wake, a new user message for a
// prompt, or nothing when retrying a user message that was already
// accepted. It reports false when a wake has no notices to deliver.
func (t *turn) start(ctx context.Context, msgs []message.Message) ([]message.Message, bool, error) {
	a, call, state := t.a, t.call, t.call.state
	switch {
	case call.wake:
		noticeMsg, err := a.deliverJobEvents(ctx, call.SessionID, t.leaf)
		if err != nil {
			a.refundWake(call.SessionID)
			return nil, false, err
		}
		if noticeMsg == nil {
			a.refundWake(call.SessionID)
			return nil, false, nil
		}
		t.leaf = noticeMsg.ID
		return append(msgs, *noticeMsg), true, nil
	case state.acceptedUserID != "":
		return slices.DeleteFunc(msgs, func(m message.Message) bool { return m.ID == state.acceptedUserID }), true, nil
	default:
		userMsg, err := a.createUserMessage(ctx, call, t.leaf)
		if err != nil {
			return nil, false, err
		}
		t.leaf = userMsg.ID
		state.acceptedUserID = userMsg.ID
		return msgs, true, nil
	}
}

func (t *turn) streamCall(history []fantasy.Message, files []fantasy.FilePart) fantasy.AgentStreamCall {
	call := t.call
	// Don't send MaxOutputTokens if 0. Some providers (e.g. LM Studio)
	// reject it.
	var maxOutputTokens *int64
	if call.MaxOutputTokens > 0 {
		maxOutputTokens = &call.MaxOutputTokens
	}
	return fantasy.AgentStreamCall{
		Prompt:           message.PromptWithTextAttachments(call.Prompt, call.Attachments),
		Files:            files,
		Messages:         history,
		Headers:          sessionHeaders(call.SessionID),
		ProviderOptions:  call.ProviderOptions,
		MaxOutputTokens:  maxOutputTokens,
		TopP:             call.TopP,
		Temperature:      call.Temperature,
		PresencePenalty:  call.PresencePenalty,
		TopK:             call.TopK,
		FrequencyPenalty: call.FrequencyPenalty,
		PrepareStep:      t.prepareStep,
		OnReasoningStart: func(_ string, reasoning fantasy.ReasoningContent) error {
			t.assistant.AppendReasoningContent(reasoning.Text)
			return t.a.messages.Update(t.genCtx, *t.assistant)
		},
		OnReasoningDelta: func(_ string, text string) error {
			t.assistant.AppendReasoningContent(text)
			return t.a.messages.Update(t.genCtx, *t.assistant)
		},
		OnReasoningEnd:   t.onReasoningEnd,
		OnTextDelta:      t.onTextDelta,
		OnToolInputStart: t.onToolInputStart,
		OnRetry:          t.onRetry,
		OnAuthRefresh:    call.OnAuthRefresh,
		OnStreamFinish:   t.onStreamFinish,
		// ModelProvider is re-read on each attempt so a stream retried
		// after OnAuthRefresh picks up the model rebuilt against the
		// refreshed credentials rather than the stale one.
		ModelProvider: func() fantasy.LanguageModel {
			return t.a.largeModel.Get().Model
		},
		OnToolCall:   t.onToolCall,
		OnToolResult: t.onToolResult,
		OnStepFinish: t.onStepFinish,
		StopWhen: []fantasy.StopCondition{
			t.contextNearlyFull,
			func(steps []fantasy.StepResult) bool {
				return hasRepeatedToolCalls(steps, loopDetectionWindowSize, loopDetectionMaxRepeats)
			},
		},
	}
}

func (t *turn) prepareStep(callContext context.Context, options fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
	a := t.a
	prepared.Messages = t.injected.apply(options.Messages)
	for i := range prepared.Messages {
		prepared.Messages[i].ProviderOptions = nil
	}

	// Use latest tools (updated by SetTools when MCP tools change),
	// filtering out lazy MCP tools that haven't been enabled.
	prepared.Tools = filterLazyMCPTools(a.tools.Copy(), t.lazyMCPToolMap, t.lazyState)

	if a.jobEvents != nil {
		noticeMsg, deliverErr := a.deliverJobEvents(callContext, t.call.SessionID, t.currentLeaf())
		if deliverErr != nil {
			return callContext, prepared, deliverErr
		}
		if noticeMsg != nil {
			t.setLeaf(noticeMsg.ID)
			aiMessages := noticeMsg.ToAIMessage()
			t.injected.add(options.Messages, aiMessages...)
			prepared.Messages = append(prepared.Messages, aiMessages...)
		}
	}

	prepared.Messages = a.workaroundProviderMediaLimitations(prepared.Messages, t.largeModel)
	t.addCacheControl(prepared.Messages)

	if t.promptPrefix != "" {
		prepared.Messages = append([]fantasy.Message{fantasy.NewSystemMessage(t.promptPrefix)}, prepared.Messages...)
	}

	if isAnthropicOAuth(t.providerCfg) {
		prepared.Messages = transformForAnthropicOAuth(prepared.Messages)
	}

	t.mu.Lock()
	t.stepMessages = cloneFantasyMessages(prepared.Messages)
	t.mu.Unlock()

	assistantMsg, err := a.messages.Create(callContext, t.call.SessionID, message.CreateMessageParams{
		Role:            message.Assistant,
		Parts:           []message.ContentPart{},
		Model:           t.largeModel.ModelCfg.Model,
		Provider:        t.largeModel.ModelCfg.Provider,
		ParentMessageID: t.currentLeaf(),
	})
	if err != nil {
		return callContext, prepared, err
	}
	t.call.state.assistantIDs = append(t.call.state.assistantIDs, assistantMsg.ID)
	t.setLeaf(assistantMsg.ID)
	// The capture starts after the message is created so
	// request_started_at is as close to the provider request as
	// PrepareStep allows.
	if a.usageRecorder != nil {
		stepUsage := a.newCapture(usageKindTurn, t.runID, t.call.SessionID, t.parentSessionID, t.largeModel, prepared.Tools, prepared.Messages)
		stepUsage.stepIndex = options.StepNumber
		stepUsage.messageID = assistantMsg.ID
		t.mu.Lock()
		if t.havePrevTurn {
			stepUsage.comparePrefix(t.prevTurn)
		}
		t.capture = stepUsage
		t.mu.Unlock()
	}
	callContext = context.WithValue(callContext, tools.MessageIDContextKey, assistantMsg.ID)
	callContext = context.WithValue(callContext, tools.SupportsImagesContextKey, t.largeModel.CatwalkCfg.SupportsImages)
	callContext = context.WithValue(callContext, tools.ModelNameContextKey, t.largeModel.CatwalkCfg.Name)
	callContext = context.WithValue(callContext, ownerKey{}, struct{}{})
	t.assistant = &assistantMsg
	return callContext, prepared, nil
}

// addCacheControl marks the last system message and the last two messages
// for prompt caching.
func (t *turn) addCacheControl(msgs []fantasy.Message) {
	lastSystemRoleInx := 0
	systemMessageUpdated := false
	for i, msg := range msgs {
		if msg.Role == fantasy.MessageRoleSystem {
			lastSystemRoleInx = i
		} else if !systemMessageUpdated {
			msgs[lastSystemRoleInx].ProviderOptions = t.a.getCacheControlOptions()
			systemMessageUpdated = true
		}
		if i > len(msgs)-3 {
			msgs[i].ProviderOptions = t.a.getCacheControlOptions()
		}
	}
}

func (t *turn) onReasoningEnd(_ string, reasoning fantasy.ReasoningContent) error {
	if anthropicData, ok := reasoning.ProviderMetadata[anthropic.Name]; ok {
		if meta, ok := anthropicData.(*anthropic.ReasoningOptionMetadata); ok {
			t.assistant.AppendReasoningSignature(meta.Signature)
		}
	}
	if googleData, ok := reasoning.ProviderMetadata[google.Name]; ok {
		if meta, ok := googleData.(*google.ReasoningMetadata); ok {
			t.assistant.AppendThoughtSignature(meta.Signature, meta.ToolID)
		}
	}
	if openaiData, ok := reasoning.ProviderMetadata[openai.Name]; ok {
		if meta, ok := openaiData.(*openai.ResponsesReasoningMetadata); ok {
			t.assistant.SetReasoningResponsesData(meta)
		}
	}
	t.assistant.FinishThinking()
	return t.a.messages.Update(t.genCtx, *t.assistant)
}

func (t *turn) onTextDelta(_ string, text string) error {
	// Strip leading newline from initial text content. This is
	// particularly important in non-interactive mode where leading
	// newlines are very visible.
	if len(t.assistant.Parts) == 0 {
		text = strings.TrimPrefix(text, "\n")
	}
	t.assistant.AppendContent(text)
	return t.a.messages.Update(t.genCtx, *t.assistant)
}

func (t *turn) onToolInputStart(id string, toolName string) error {
	t.assistant.AddToolCall(message.ToolCall{
		ID:               id,
		Name:             toolName,
		ProviderExecuted: false,
		Finished:         false,
	})
	// Use persistCtx so the update succeeds even if the request is
	// canceled mid-stream.
	return t.a.messages.Update(t.persistCtx, *t.assistant)
}

func (t *turn) onRetry(err *fantasy.ProviderError, delay time.Duration) {
	slog.Warn("Provider request failed, retrying", providerRetryLogFields(err, delay)...)
	t.mu.Lock()
	if t.capture != nil {
		t.capture.retries++
	}
	t.mu.Unlock()
	// Reset streamed content so the retried response doesn't concatenate
	// with partial content from the failed attempt. On the final attempt
	// (no more retries), any partial content stays in the message as
	// useful context beneath the error.
	t.assistant.ResetStreamedContent()
	if updateErr := t.a.messages.Update(t.genCtx, *t.assistant); updateErr != nil {
		slog.Error("Failed to reset message on retry", "error", updateErr)
	}
}

// onStreamFinish fires as soon as the provider reports usage, before tools
// run, so steps whose tools later fail are still recorded. fantasy
// (v0.45.2) calls it from its agent processStepStream on the finish part,
// inside the per-step retry closure in its agent Stream. A retried attempt
// that failed before its finish part never reaches it, so a retried request
// is recorded once, by the attempt that finished. A critical tool error is
// wrapped in ToolExecutionError (processStepStream), which isRetryableError
// rejects, so a finished step is not retried and is recorded once. The
// triage skill still checks for duplicates defensively.
func (t *turn) onStreamFinish(usage fantasy.Usage, reason fantasy.FinishReason, meta fantasy.ProviderMetadata) error {
	finished := time.Now()
	t.mu.Lock()
	if t.capture == nil {
		t.mu.Unlock()
		return nil
	}
	// Snapshot the capture so the row is built outside the lock.
	snapshot := *t.capture
	t.prevTurn, t.havePrevTurn = snapshot.turnPrefix(), true
	t.a.lastTurn.Set(t.call.SessionID, t.prevTurn)
	t.mu.Unlock()
	t.a.usageRecorder.Record(t.a.newRow(&snapshot, usage, reason, meta, finished))
	return nil
}

func (t *turn) onToolCall(tc fantasy.ToolCallContent) error {
	input, wasSanitized := sanitizeToolInput(tc.ToolName, tc.ToolCallID, tc.Input)
	if wasSanitized {
		t.sanitizedMu.Lock()
		t.sanitized[tc.ToolCallID] = true
		t.sanitizedMu.Unlock()
	}
	t.assistant.AddToolCall(message.ToolCall{
		ID:               tc.ToolCallID,
		Name:             tc.ToolName,
		Input:            input,
		ProviderExecuted: false,
		Finished:         true,
	})
	// Use persistCtx so the update succeeds even if the request is
	// canceled mid-stream.
	return t.a.messages.Update(t.persistCtx, *t.assistant)
}

func (t *turn) onToolResult(result fantasy.ToolResultContent) error {
	toolResult := t.a.convertToToolResult(result)
	t.sanitizedMu.Lock()
	wasSanitized := t.sanitized[result.ToolCallID]
	t.sanitizedMu.Unlock()
	if wasSanitized {
		toolResult.Content = "Tool call failed: arguments were not valid JSON. Please check your tool call format and try again."
		toolResult.IsError = true
	}
	// Hold the lock across the entire read→create→update sequence.
	// OnToolResult may be called from parallel tool-execution goroutines;
	// without the lock two goroutines could read the same leaf and create
	// sibling messages (an unintended fork).
	t.mu.Lock()
	defer t.mu.Unlock()
	// Use persistCtx: a completed tool result must be recorded even when
	// cancellation races the tool goroutine, or the error path would
	// misrecord the success as a canceled call.
	toolMsg, err := t.a.messages.Create(t.persistCtx, t.assistant.SessionID, message.CreateMessageParams{
		Role:            message.Tool,
		Parts:           []message.ContentPart{toolResult},
		ParentMessageID: t.leaf,
	})
	if err != nil {
		return err
	}
	t.leaf = toolMsg.ID
	if t.a.jobEvents != nil && !toolResult.IsError {
		t.a.observeJobResult(result.ToolName, toolResult.Metadata)
	}
	return nil
}

var stepFinishReasons = map[fantasy.FinishReason]message.FinishReason{
	fantasy.FinishReasonLength:    message.FinishReasonMaxTokens,
	fantasy.FinishReasonStop:      message.FinishReasonEndTurn,
	fantasy.FinishReasonToolCalls: message.FinishReasonToolUse,
	// The provider's safety classifier stopped the model (Anthropic
	// stop_reason=refusal, OpenAI content_filter). The TUI owns the display
	// copy; only the reason is persisted so the UI can show a REFUSED
	// banner.
	fantasy.FinishReasonContentFilter: message.FinishReasonContentFilter,
}

func (t *turn) onStepFinish(stepResult fantasy.StepResult) error {
	for _, w := range stepResult.Warnings {
		slog.Warn("Provider warning", "type", w.Type, "message", w.Message)
	}
	finishReason := cmp.Or(stepFinishReasons[stepResult.FinishReason], message.FinishReasonUnknown)
	if finishReason == message.FinishReasonContentFilter {
		slog.Warn("Provider content filter stopped the model",
			"session_id", t.call.SessionID,
			"finish_reason", string(stepResult.FinishReason),
		)
	}
	// If a tool result halted the turn (e.g. a hook halt or a permission
	// denial), the step ends on FinishReasonToolCalls but the model will
	// not be called again. Treat it as the end of the turn so the UI can
	// render the assistant footer.
	if finishReason == message.FinishReasonToolUse && stopsTurn(stepResult) {
		finishReason = message.FinishReasonEndTurn
	}
	t.assistant.AddFinish(finishReason, "", "")
	t.mu.Lock()
	defer t.mu.Unlock()

	updatedSession, err := t.a.sessions.Get(t.genCtx, t.call.SessionID)
	if err != nil {
		return err
	}
	usage, estimated := fallbackStepUsage(t.stepMessages, stepResult)
	t.a.updateSessionUsage(t.largeModel, &updatedSession, usage, t.a.openrouterCost(stepResult.ProviderMetadata), estimated)
	if _, err := t.a.sessions.Save(t.genCtx, updatedSession); err != nil {
		return err
	}
	t.session = updatedSession
	return t.a.messages.Update(t.genCtx, *t.assistant)
}

func stopsTurn(stepResult fantasy.StepResult) bool {
	for _, tr := range stepResult.Content.ToolResults() {
		if tr.StopTurn {
			return true
		}
	}
	return false
}

// contextNearlyFull stops the stream and marks the turn for summarizing
// when the session's prompt is close to the model's context window.
func (t *turn) contextNearlyFull(_ []fantasy.StepResult) bool {
	cw := int64(t.largeModel.CatwalkCfg.ContextWindow)
	// If context window is unknown (0), skip auto-summarize to avoid
	// immediately truncating custom/local models.
	if cw == 0 || t.a.disableAutoSummarize {
		return false
	}
	threshold := int64(float64(cw) * smallContextWindowRatio)
	if cw >= largeContextWindowThreshold {
		threshold = largeContextWindowBuffer
	}
	if cw-t.session.PromptTokens > threshold {
		return false
	}
	t.shouldSummarize = true
	return true
}

// recordFailure closes the failed step: it finishes open tool calls, gives
// every tool call without a result an error result, sets the assistant
// message's finish reason from err, and syncs the session leaf.
func (t *turn) recordFailure(err error) error {
	isCancelErr := errors.Is(err, context.Canceled)
	// Ensure we finish thinking on error to close the reasoning state.
	t.assistant.FinishThinking()
	// Use persistCtx: genCtx has been canceled, and for subagents the
	// parent ctx is canceled too.
	msgs, listErr := t.a.messages.List(t.persistCtx, t.assistant.SessionID)
	if listErr != nil {
		return listErr
	}
	answered := make(map[string]bool)
	for _, msg := range msgs {
		if msg.Role != message.Tool {
			continue
		}
		for _, tr := range msg.ToolResults() {
			answered[tr.ToolCallID] = true
		}
	}
	content := "There was an error while executing the tool"
	if isCancelErr {
		content = "Error: user cancelled assistant tool calling"
	}
	for _, tc := range t.assistant.ToolCalls() {
		if !tc.Finished {
			tc.Finished = true
			tc.Input = "{}"
			t.assistant.AddToolCall(tc)
			if updateErr := t.a.messages.Update(t.persistCtx, *t.assistant); updateErr != nil {
				return updateErr
			}
		}
		if answered[tc.ID] {
			continue
		}
		errToolMsg, createErr := t.a.messages.Create(t.persistCtx, t.assistant.SessionID, message.CreateMessageParams{
			Role: message.Tool,
			Parts: []message.ContentPart{message.ToolResult{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    content,
				IsError:    true,
			}},
			ParentMessageID: t.currentLeaf(),
		})
		if createErr != nil {
			return createErr
		}
		t.setLeaf(errToolMsg.ID)
	}
	reason, title, detail := failureFinish(err)
	t.assistant.AddFinish(reason, title, detail)
	if updateErr := t.a.messages.Update(t.persistCtx, *t.assistant); updateErr != nil {
		return updateErr
	}
	// Ensure the session's leaf pointer is synced and published so the UI
	// reflects the current position (e.g. branch dialog). message.Create
	// already advanced the DB leaf, but no session pubsub event was fired
	// because OnStepFinish never ran.
	if moveErr := t.a.sessions.MoveLeaf(t.persistCtx, t.call.SessionID, t.currentLeaf()); moveErr != nil {
		slog.Warn("Failed to sync session leaf after error", "err", moveErr)
	}
	return nil
}

// failureFinish returns the finish reason, title and detail to show for a
// stream that ended with err.
func failureFinish(err error) (reason message.FinishReason, title, detail string) {
	const defaultTitle = "Provider Error"
	var fantasyErr *fantasy.Error
	var providerErr *fantasy.ProviderError
	switch {
	case errors.Is(err, context.Canceled):
		return message.FinishReasonCanceled, "User canceled request", ""
	case errors.As(err, &providerErr):
		return message.FinishReasonError, cmp.Or(stringext.Capitalize(providerErr.Title), defaultTitle), providerErr.Message
	case errors.As(err, &fantasyErr):
		return message.FinishReasonError, cmp.Or(stringext.Capitalize(fantasyErr.Title), defaultTitle), fantasyErr.Message
	case fantasy.IsTransportError(err):
		wrapped := fantasy.NewTransportError(err)
		return message.FinishReasonError, stringext.Capitalize(wrapped.Title), wrapped.Message
	default:
		return message.FinishReasonError, defaultTitle, err.Error()
	}
}
