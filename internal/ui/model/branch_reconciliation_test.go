package model

import (
	"context"
	"errors"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

type branchGateWorkspace struct {
	workspace.Workspace
	mu              sync.Mutex
	readErr         error
	runErr          error
	entered         chan struct{}
	release         chan struct{}
	runEntered      chan struct{}
	runRelease      chan struct{}
	writeEntered    chan struct{}
	writeRelease    chan struct{}
	writes          int
	reads           int
	moves           int
	acceptedEntered chan struct{}
	acceptedRelease chan struct{}
	submissions     int
	cancels         int
	busy            bool
}

func (w *branchGateWorkspace) GetSession(ctx context.Context, id string) (session.Session, error) {
	w.mu.Lock()
	w.reads++
	err, entered, release := w.readErr, w.entered, w.release
	w.entered, w.release = nil, nil
	w.mu.Unlock()
	if entered != nil {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return session.Session{}, ctx.Err()
		}
	}
	if err != nil {
		return session.Session{}, err
	}
	return w.Workspace.GetSession(ctx, id)
}

func (w *branchGateWorkspace) MoveLeaf(ctx context.Context, id, leaf string) error {
	w.mu.Lock()
	w.moves++
	w.mu.Unlock()
	return w.Workspace.MoveLeaf(ctx, id, leaf)
}

func (w *branchGateWorkspace) AgentRunFromMessage(ctx context.Context, id, prompt string, opts agent.BranchRunOptions, atts ...message.Attachment) error {
	w.mu.Lock()
	w.submissions++
	err, entered, release := w.runErr, w.runEntered, w.runRelease
	w.mu.Unlock()
	if entered != nil {
		close(entered)
		<-release
	}
	if err != nil {
		return err
	}
	if w.acceptedEntered != nil {
		callback := opts.OnUserMessageCreated
		opts.OnUserMessageCreated = func(msg message.Message) { close(w.acceptedEntered); <-w.acceptedRelease; callback(msg) }
	}
	return w.Workspace.AgentRunFromMessage(ctx, id, prompt, opts, atts...)
}

func (w *branchGateWorkspace) AgentCancel(id string) {
	w.mu.Lock()
	w.cancels++
	w.mu.Unlock()
	w.Workspace.AgentCancel(id)
}

func (w *branchGateWorkspace) AgentIsBusy() bool {
	w.mu.Lock()
	busy := w.busy
	w.mu.Unlock()
	return busy || w.Workspace.AgentIsBusy()
}

func branchFixtureUI(t *testing.T) (*branchfixture.Fixture, *UI, *branchGateWorkspace, message.Message) {
	t.Helper()
	f := branchfixture.New(t)
	sess, err := f.Workspace.CreateSession(f.Context, "root")
	require.NoError(t, err)
	source := seedUserSource(t, f, sess.ID, "source")
	m := newIntegrationUI(t, f, sess.ID, &source)
	ws := &branchGateWorkspace{Workspace: f.Workspace}
	m.com.Workspace = ws
	t.Cleanup(m.clearBranchState)
	return f, m, ws, source
}

func TestBranchReloadBudgetAndReadOnlyReturnRetry(t *testing.T) {
	_, m, ws, source := branchFixtureUI(t)
	m.textarea.SetValue("original")
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	driveAcceptedBranch(t, m)
	ws.readErr = errors.New("read unavailable")
	for i := range 4 {
		var cmd tea.Cmd
		if i == 0 {
			cmd = m.scheduleBranchRead(m.branchRun)
		} else {
			_, cmd = m.Update(branchRetryMsg{run: m.branchRun})
		}
		require.NotNil(t, cmd)
		m.Update(cmd())
		require.Equal(t, i+1, m.branchRun.failCount)
	}
	require.Error(t, m.branchRun.reloadErr)
	require.Nil(t, m.scheduleBranchRead(m.branchRun))
	reads := ws.reads
	m.Update(pubsub.Event[message.Message]{Type: pubsub.UpdatedEvent, Payload: source})
	require.Equal(t, reads, ws.reads)
	ws.readErr = nil
	_, retry := branchAction(m, dialog.ActionRetryBranchReload{})
	m.Update(retry())
	require.NoError(t, m.branchRun.reloadErr)
	require.Equal(t, 1, ws.submissions)

	_, returning := branchAction(m, dialog.ActionReturnToPreBranch{})
	ws.readErr = errors.New("return reload unavailable")
	m.Update(returning())
	require.Equal(t, 1, ws.moves)
	require.True(t, m.branchLoading)
	require.True(t, m.branchActive())
	require.True(t, m.mutationsPending())
	m.Update(util.DrillInMsg{SessionID: "hidden", Label: "hidden"})
	require.Empty(t, m.drillStack)
	for range 3 {
		_, cmd := m.Update(branchRetryMsg{run: m.branchRun})
		m.Update(cmd())
	}
	require.Error(t, m.branchRun.reloadErr)
	require.NotNil(t, m.branchReturn)
	ws.readErr = nil
	_, retry = branchAction(m, dialog.ActionRetryBranchReload{})
	m.Update(retry())
	require.Nil(t, m.branchRun)
	require.Nil(t, m.branchReturn)
	require.Equal(t, 1, ws.moves)
	require.Equal(t, 1, ws.submissions)
	require.Equal(t, source.ID, m.session.LeafMessageID)
	require.Equal(t, "original", m.textarea.Value())
}

func TestBranchDelayedReadIgnoresEventBodies(t *testing.T) {
	f, m, ws, source := branchFixtureUI(t)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	accepted := driveAcceptedBranch(t, m)
	ws.entered, ws.release = make(chan struct{}), make(chan struct{})
	entered, release := ws.entered, ws.release
	cmd := m.scheduleBranchRead(m.branchRun)
	results := make(chan tea.Msg, 1)
	go func() { results <- cmd() }()
	<-entered
	stale, err := f.Messages.Get(f.Context, accepted)
	require.NoError(t, err)
	stale.Parts = []message.ContentPart{message.TextContent{Text: "stale body"}}
	for range 5000 {
		m.Update(pubsub.Event[message.Message]{Type: pubsub.UpdatedEvent, Payload: stale})
		m.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: source})
	}
	require.True(t, m.branchRun.dirty)
	close(release)
	_, next := m.Update(<-results)
	require.NotNil(t, next)
	m.Update(next())
	require.Nil(t, m.chat.MessageItem(source.ID))
	item := m.chat.MessageItem(accepted).(chat.SourceMessageProvider)
	visible := item.SourceMessage()
	require.Equal(t, "source", visible.Content().Text)
	require.False(t, m.branchRun.dirty)
}

func TestBranchWatchdogRoutesChildMessageEvents(t *testing.T) {
	_, m, _, _ := branchFixtureUI(t)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	driveAcceptedBranch(t, m)
	require.False(t, m.branchRun.dirty)

	agentItem := chat.NewAgentToolMessageItem(m.com.Styles, message.ToolCall{
		ID: "agent-call", Name: "agent", Input: `{}`, Finished: true,
	}, nil, false)
	m.chat.AppendMessages(agentItem)
	childSessionID := m.com.Workspace.CreateAgentToolSessionID("parent-message", agentItem.ID())
	childChat := NewChat(m.com)
	m.drillStack = []drillInEntry{{sessionID: childSessionID, chat: childChat}}

	child := message.Message{
		ID: "child-message", SessionID: childSessionID, Role: message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "initial content"}},
	}
	m.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: child})
	require.NotNil(t, childChat.MessageItem(child.ID))
	visible := childChat.MessageItem(child.ID).(chat.SourceMessageProvider).SourceMessage()
	require.Equal(t, "initial content", visible.Content().Text)
	turns, toolCalls := m.viewedSessionStats()
	require.Equal(t, 1, turns)
	require.Zero(t, toolCalls)
	require.Equal(t, childSessionID, agentItem.DrillIn())
	require.False(t, m.branchRun.dirty)

	child.Parts = []message.ContentPart{
		message.TextContent{Text: "streamed content"},
		message.ToolCall{ID: "child-call", Name: "view", Input: `{}`, Finished: true},
	}
	for range 2 {
		m.Update(pubsub.Event[message.Message]{Type: pubsub.UpdatedEvent, Payload: child})
	}
	visible = childChat.MessageItem(child.ID).(chat.SourceMessageProvider).SourceMessage()
	require.Equal(t, "streamed content", visible.Content().Text)
	turns, toolCalls = m.viewedSessionStats()
	require.Equal(t, 1, turns)
	require.Equal(t, 1, toolCalls)
	require.Len(t, agentItem.NestedTools(), 1)
	require.False(t, m.branchRun.dirty)

	m.Update(pubsub.Event[message.Message]{Type: pubsub.DeletedEvent, Payload: child})
	require.Nil(t, childChat.MessageItem(child.ID))
	require.False(t, m.branchRun.dirty)

	child.SessionID = "foreign-session"
	m.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: child})
	require.Nil(t, childChat.MessageItem(child.ID))
	require.Nil(t, m.chat.MessageItem(child.ID))
	require.False(t, m.branchRun.dirty)
}

func TestBranchWatchdogInvalidatesSameSessionMessageEvents(t *testing.T) {
	_, m, _, _ := branchFixtureUI(t)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	acceptedID := driveAcceptedBranch(t, m)
	item := m.chat.MessageItem(acceptedID).(chat.SourceMessageProvider)
	original := item.SourceMessage()
	stale := original
	stale.Parts = []message.ContentPart{message.TextContent{Text: "stale body"}}

	for _, eventType := range []pubsub.EventType{pubsub.CreatedEvent, pubsub.UpdatedEvent, pubsub.DeletedEvent} {
		m.branchRun.dirty = false
		m.Update(pubsub.Event[message.Message]{Type: eventType, Payload: stale})
		require.True(t, m.branchRun.dirty)
		require.NotNil(t, m.chat.MessageItem(acceptedID))
		visible := m.chat.MessageItem(acceptedID).(chat.SourceMessageProvider).SourceMessage()
		require.Equal(t, original, visible)
	}
}

func TestBranchPreinsertFailureAndRepeatedEnter(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "transient", true: "stale"}[stale], func(t *testing.T) {
			_, m, ws, _ := branchFixtureUI(t)
			ws.runErr = errors.New("temporarily unavailable")
			if stale {
				ws.runErr = agent.ErrBranchStaleSource
			}
			m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
			m.textarea.SetValue("unsent draft")
			_, submit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			run := m.branchRun
			_, repeated := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			require.Nil(t, repeated)
			require.Same(t, run, m.branchRun)
			results := collectMsgs(submit)
			outcome, ok := findMsg[branchOutcomeMsg](results)
			require.True(t, ok)
			m.Update(outcome)
			require.Nil(t, m.branchRun)
			require.NotNil(t, m.branchPreview)
			require.Equal(t, "unsent draft", m.textarea.Value())
			require.Equal(t, stale, m.branchPreview.invalid)
			_, retry := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if stale {
				require.NotNil(t, retry)
				require.Nil(t, m.branchRun)
				require.Equal(t, 1, ws.submissions)
			} else {
				require.NotNil(t, m.branchRun)
			}
		})
	}
}

func TestBranchLifetimeTeardownUnblocksReadAndWait(t *testing.T) {
	_, m, ws, _ := branchFixtureUI(t)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	m.trySubmitBranch()
	run := m.branchRun
	result := make(chan tea.Msg, 1)
	go func() { result <- waitBranchOutcomeCmd(run, run.outcome)() }()
	ws.entered, ws.release = make(chan struct{}), make(chan struct{})
	entered := ws.entered
	read := m.scheduleBranchRead(run)
	readResult := make(chan tea.Msg, 1)
	go func() { readResult <- read() }()
	<-entered
	m.clearBranchState()
	require.Nil(t, <-result)
	require.ErrorIs(t, (<-readResult).(branchReadResultMsg).err, context.Canceled)
	run.outcome <- branchOutcome{kind: branchOutcomeAccepted, userID: "obsolete"}
	run.outcome <- branchOutcome{kind: branchOutcomeFinished}
	m.Update(branchOutcomeMsg{run: run, outcome: <-run.outcome})
	require.Nil(t, m.branchReturn)
	require.Nil(t, m.branchPreview)
}

func TestBranchNormalEscapeAfterReconcile(t *testing.T) {
	_, m, ws, _ := branchFixtureUI(t)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	driveAcceptedBranch(t, m)
	ws.busy = true
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.True(t, m.isCanceling)
	require.Zero(t, ws.cancels)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.False(t, m.isCanceling)
	require.Equal(t, 1, ws.cancels)
}

type branchActionDialog struct{ action dialog.Action }

func (d branchActionDialog) ID() string                               { return "branch-test-action" }
func (d branchActionDialog) HandleMsg(tea.Msg) dialog.Action          { return d.action }
func (d branchActionDialog) Draw(uv.Screen, uv.Rectangle) *tea.Cursor { return nil }
func branchAction(m *UI, action dialog.Action) (tea.Model, tea.Cmd) {
	m.dialog.OpenDialog(branchActionDialog{action: action})
	model, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.dialog.CloseDialog("branch-test-action")
	return model, cmd
}

func TestBranchMutationsBlockKeysUntilRefresh(t *testing.T) {
	for _, kind := range []string{"navigate", "mcp", "reasoning", "model"} {
		t.Run(kind, func(t *testing.T) {
			f, m, ws, source := branchFixtureUI(t)
			var action dialog.Action
			switch kind {
			case "navigate":
				action = dialog.ActionNavigateTree{MessageID: source.ID, Role: message.Assistant}
			case "mcp":
				action = dialog.ActionToggleLazyMCP{ServerName: "test", Enabled: true}
			case "reasoning":
				action = dialog.ActionSelectReasoningEffort{Effort: "high"}
			case "model":
				selected := f.Config.Config().Models[config.SelectedModelTypeLarge]
				selected.Model = "claude-sonnet-4-20250514"
				action = dialog.ActionSelectModel{Model: selected, ModelType: config.SelectedModelTypeLarge}
			}
			_, cmd := branchAction(m, action)
			require.NotNil(t, cmd)
			require.True(t, m.mutationsPending())
			before, err := f.Messages.List(f.Context, m.session.ID)
			require.NoError(t, err)
			m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
			require.Nil(t, m.branchPreview)
			m.focus = uiFocusEditor
			m.textarea.SetValue("blocked prompt")
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			require.Equal(t, "blocked prompt", m.textarea.Value())
			require.Zero(t, ws.submissions)
			after, err := f.Messages.List(f.Context, m.session.ID)
			require.NoError(t, err)
			require.Len(t, after, len(before))
			results := collectMsgs(cmd)
			for _, result := range results {
				_, follow := m.Update(result)
				if _, ok := result.(mutationDoneMsg); ok {
					require.True(t, m.mutationsPending())
					require.NotNil(t, follow)
					dispatchMutationRefresh(t, m, follow)
				}
			}
			require.False(t, m.mutationsPending())
			persisted, err := f.Workspace.GetSession(f.Context, m.session.ID)
			require.NoError(t, err)
			require.Equal(t, persisted.LeafMessageID, m.session.LeafMessageID)
		})
	}
}

func dispatchMutationRefresh(t *testing.T, m *UI, cmd tea.Cmd) {
	t.Helper()
	require.NotNil(t, cmd)
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		require.NotEmpty(t, batch)
		msg = batch[0]()
	}
	result, ok := msg.(mutationRefreshResultMsg)
	require.True(t, ok)
	require.NoError(t, result.err)
	m.Update(result)
}

func TestBranchAcceptanceAfterEscape(t *testing.T) {
	f, m, ws, source := branchFixtureUI(t)
	ws.acceptedEntered, ws.acceptedRelease = make(chan struct{}), make(chan struct{})
	m.textarea.SetValue("original draft")
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	m.textarea.SetValue("replacement")
	_, submit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	batch := submit().(tea.BatchMsg)
	done := make(chan tea.Msg, 1)
	go func() { done <- batch[0]() }()
	<-ws.acceptedEntered
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Equal(t, 1, ws.cancels)
	require.NoError(t, m.branchRun.ctx.Err())
	close(ws.acceptedRelease)
	<-done
	accepted := batch[1]().(branchOutcomeMsg)
	require.Equal(t, branchOutcomeAccepted, accepted.outcome.kind)
	_, initialCmds := m.Update(accepted)
	initialBatch := initialCmds().(tea.BatchMsg)
	initialRead := initialBatch[0]
	require.Empty(t, m.textarea.Value())
	require.Equal(t, "original draft", m.branchReturn.originalDraft.text)
	finished := waitBranchOutcomeCmd(m.branchRun, m.branchRun.outcome)().(branchOutcomeMsg)
	m.Update(finished)
	_, final := m.Update(initialRead())
	require.NotNil(t, final)
	m.Update(final())
	require.False(t, m.branchActive())
	require.NotNil(t, m.branchReturn)
	require.Nil(t, m.branchPreview)
	users, err := f.Messages.ListUserMessages(f.Context, source.SessionID)
	require.NoError(t, err)
	require.Len(t, users, 2)
}

func TestBranchWatchdogReconcilesQueuedHandoffWithAllEventsDropped(t *testing.T) {
	f, m, _, _ := branchFixtureUI(t)
	require.NoError(t, f.Sessions.Rename(f.Context, m.session.ID, "custom", true))
	branchStarted, branchRelease := make(chan struct{}), make(chan struct{})
	queuedStarted, queuedRelease := make(chan struct{}), make(chan struct{})
	f.Provider.Enqueue(branchfixture.Response{Text: "branch answer", Started: branchStarted, Release: branchRelease}, branchfixture.Response{Text: "queued answer", Started: queuedStarted, Release: queuedRelease})
	var branchOnce, queuedOnce sync.Once
	t.Cleanup(func() {
		branchOnce.Do(func() { close(branchRelease) })
		queuedOnce.Do(func() { close(queuedRelease) })
		f.Coordinator.WaitBackgroundJobs()
	})
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	_, submit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	batch := submit().(tea.BatchMsg)
	done := make(chan tea.Msg, 1)
	go func() { done <- batch[0]() }()
	<-branchStarted
	accepted := batch[1]().(branchOutcomeMsg)
	_, acceptance := m.Update(accepted)
	acceptanceCmds := acceptance().(tea.BatchMsg)
	initial := acceptanceCmds[0]().(branchReadResultMsg)
	m.Update(initial)
	require.False(t, m.branchLoading)
	require.True(t, m.branchActive())
	require.NoError(t, f.Workspace.AgentRun(f.Context, m.session.ID, "queued prompt"))
	branchOnce.Do(func() { close(branchRelease) })
	<-done
	<-queuedStarted
	finished := waitBranchOutcomeCmd(m.branchRun, m.branchRun.outcome)().(branchOutcomeMsg)
	_, final := m.Update(finished)
	m.Update(final())
	require.False(t, m.branchActive())
	queuedOnce.Do(func() { close(queuedRelease) })
	f.Coordinator.WaitBackgroundJobs()
	_, poll := m.Update(branchPollMsg{run: m.branchRun})
	pollCmds := poll().(tea.BatchMsg)
	m.Update(pollCmds[1]())
	sess, err := f.Workspace.GetSession(f.Context, m.session.ID)
	require.NoError(t, err)
	require.Equal(t, sess.LeafMessageID, m.session.LeafMessageID)
	item := m.chat.MessageItem(sess.LeafMessageID).(chat.SourceMessageProvider)
	visible := item.SourceMessage()
	require.Equal(t, "queued answer", visible.Content().Text)
	require.Equal(t, message.FinishReasonEndTurn, visible.FinishReason())
}

func TestBranchPreviewRestoresViewport(t *testing.T) {
	f, m, _, _ := branchFixtureUI(t)
	for range 15 {
		sess, err := f.Workspace.GetSession(f.Context, m.session.ID)
		require.NoError(t, err)
		_, err = f.Messages.Create(f.Context, m.session.ID, message.CreateMessageParams{ParentMessageID: sess.LeafMessageID, Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "another message"}}})
		require.NoError(t, err)
	}
	persisted, err := f.Workspace.GetSession(f.Context, m.session.ID)
	require.NoError(t, err)
	msgs, err := f.Workspace.GetBranchPath(f.Context, persisted.LeafMessageID)
	require.NoError(t, err)
	m.session = &persisted
	m.installBranchSnapshot(msgs, nil, nil)
	m.width, m.height = 80, 25
	m.updateLayoutAndSize()
	m.chat.ScrollToTop()
	m.chat.ScrollBy(3)
	m.chat.SetSelected(2)
	original := m.chat.branchViewport()
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	require.NotNil(t, m.branchPreview)
	require.False(t, m.chat.Follow())
	m.chat.ScrollToBottom()
	m.chat.SelectLast()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Equal(t, original, m.chat.branchViewport())
}

func (w *branchGateWorkspace) WriteMetadataEntry(ctx context.Context, id string, params message.CreateMessageParams) error {
	w.mu.Lock()
	w.writes++
	entered, release := w.writeEntered, w.writeRelease
	w.writeEntered, w.writeRelease = nil, nil
	w.mu.Unlock()
	if entered != nil {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return w.Workspace.WriteMetadataEntry(ctx, id, params)
}

func TestBranchMutationReadFailureRetainsBarrier(t *testing.T) {
	_, m, ws, _ := branchFixtureUI(t)
	_, cmd := branchAction(m, dialog.ActionToggleLazyMCP{ServerName: "test", Enabled: true})
	ws.writeEntered, ws.writeRelease = make(chan struct{}), make(chan struct{})
	entered, release := ws.writeEntered, ws.writeRelease
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	<-entered
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	require.Nil(t, m.branchPreview)
	close(release)
	_, refresh := m.Update(<-result)
	ws.readErr = errors.New("refresh failed")
	m.Update(refresh())
	require.True(t, m.mutationsPending())
	require.NotNil(t, m.mutationReload)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	require.Nil(t, m.branchPreview)
	ws.readErr = nil
	_, retry := branchAction(m, dialog.ActionRetryBranchReload{})
	m.Update(retry())
	require.False(t, m.mutationsPending())
	require.Equal(t, 1, ws.writes)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	require.NotNil(t, m.branchPreview)
	require.Equal(t, m.session.LeafMessageID, m.branchPreview.expectedLeafID)
}

func TestBranchActiveRejectsMutationActions(t *testing.T) {
	f, m, ws, source := branchFixtureUI(t)
	selected := f.Config.Config().Models[config.SelectedModelTypeLarge]
	selected.Model = "other-model"
	actions := []dialog.Action{
		dialog.ActionToggleLazyMCP{ServerName: "test", Enabled: true},
		dialog.ActionSelectReasoningEffort{Effort: "high"},
		dialog.ActionSelectModel{Model: selected, ModelType: config.SelectedModelTypeLarge},
		dialog.ActionNavigateTree{MessageID: source.ID, Role: message.Assistant},
		dialog.ActionSummarize{SessionID: m.session.ID},
	}
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	for _, action := range actions {
		_, cmd := branchAction(m, action)
		require.NotNil(t, cmd)
		require.IsType(t, util.InfoMsg{}, cmd())
	}
	require.Zero(t, ws.writes)
	require.Zero(t, ws.moves)
	require.Empty(t, m.enabledLazyMCPs)
	_, submit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.NotNil(t, submit)
	for _, action := range actions {
		_, cmd := branchAction(m, action)
		require.NotNil(t, cmd)
		require.IsType(t, util.InfoMsg{}, cmd())
	}
	require.Zero(t, ws.writes)
	require.Zero(t, ws.moves)
}

func TestBranchModelAuthAdmissionAndCancel(t *testing.T) {
	f, m, _, _ := branchFixtureUI(t)
	selected := f.Config.Config().Models[config.SelectedModelTypeLarge]
	_, cmd := branchAction(m, dialog.ActionSelectModel{Model: selected, ModelType: config.SelectedModelTypeLarge, ReAuthenticate: true})
	_ = cmd
	require.True(t, m.mutationsPending())
	require.NotNil(t, m.branchAuthModel)
	m.dialog.CloseDialog(dialog.APIKeyInputID)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	require.Nil(t, m.branchPreview)
	_, cmd = branchAction(m, dialog.ActionSelectModel{Model: selected, ModelType: config.SelectedModelTypeLarge})
	require.NotNil(t, cmd)
	require.Nil(t, m.branchAuthModel)
	require.True(t, m.mutationsPending())
	for _, result := range collectMsgs(cmd) {
		_, refresh := m.Update(result)
		if _, ok := result.(mutationDoneMsg); ok {
			dispatchMutationRefresh(t, m, refresh)
		}
	}
	require.False(t, m.mutationsPending())
}

func TestBranchNavigationPartialFailureReadOnlyRetry(t *testing.T) {
	_, m, ws, source := branchFixtureUI(t)
	_, navigate := branchAction(m, dialog.ActionNavigateTree{MessageID: source.ID, Role: message.Assistant})
	ws.readErr = errors.New("reload unavailable")
	m.Update(navigate())
	require.True(t, m.mutationsPending())
	require.NotNil(t, m.mutationReload)
	require.Equal(t, 1, ws.moves)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	require.Nil(t, m.branchPreview)
	ws.readErr = nil
	_, retry := branchAction(m, dialog.ActionRetryBranchReload{})
	m.Update(retry())
	require.False(t, m.mutationsPending())
	require.Equal(t, 1, ws.moves)
}

func TestBranchReturnHiddenMetadataAndViewport(t *testing.T) {
	f, m, _, source := branchFixtureUI(t)
	require.NoError(t, f.Workspace.WriteMetadataEntry(f.Context, m.session.ID, message.CreateMessageParams{ParentMessageID: source.ID, MessageType: message.MessageTypeMCPToggle, Parts: []message.ContentPart{message.MCPToggleContent{ServerName: "hidden", Enabled: true}}}))
	sess, err := f.Workspace.GetSession(f.Context, m.session.ID)
	require.NoError(t, err)
	m.session = &sess
	m.chat.ScrollToTop()
	m.chat.SetSelected(0)
	before := m.chat.branchViewport()
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	driveAcceptedBranch(t, m)
	_, returning := branchAction(m, dialog.ActionReturnToPreBranch{})
	m.Update(returning())
	require.Equal(t, sess.LeafMessageID, m.session.LeafMessageID)
	require.NotEqual(t, source.ID, m.session.LeafMessageID)
	require.Equal(t, before, m.chat.branchViewport())
}

func TestBranchSnapshotUpsertsChangedItemsAndDeletesMissing(t *testing.T) {
	f, m, _, _ := branchFixtureUI(t)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	accepted := driveAcceptedBranch(t, m)
	userItem := m.chat.MessageItem(accepted)
	assistantID := m.session.LeafMessageID
	assistantItem := m.chat.MessageItem(assistantID)
	persisted, err := f.Messages.Get(f.Context, assistantID)
	require.NoError(t, err)
	persisted.Parts = []message.ContentPart{message.TextContent{Text: "new persisted answer"}, message.Finish{Reason: message.FinishReasonEndTurn}}
	require.NoError(t, f.Messages.Update(f.Context, persisted))
	require.NoError(t, f.Messages.FlushAll(f.Context))
	m.chat.ScrollToTop()
	m.chat.SetSelected(0)
	m.Update(m.scheduleBranchRead(m.branchRun)())
	require.Same(t, userItem, m.chat.MessageItem(accepted))
	require.Same(t, assistantItem, m.chat.MessageItem(assistantID))
	visible := assistantItem.(chat.SourceMessageProvider).SourceMessage()
	require.Equal(t, "new persisted answer", visible.Content().Text)
	require.False(t, m.chat.Follow())
	require.NoError(t, f.Workspace.MoveLeaf(f.Context, m.session.ID, accepted))
	m.Update(m.scheduleBranchRead(m.branchRun)())
	require.Nil(t, m.chat.MessageItem(assistantID))
	require.Same(t, userItem, m.chat.MessageItem(accepted))
}

func TestBranchReloadPaletteAccessibleWhileComposerFrozen(t *testing.T) {
	_, m, ws, _ := branchFixtureUI(t)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	driveAcceptedBranch(t, m)
	ws.readErr = errors.New("unavailable")
	for i := range 4 {
		var cmd tea.Cmd
		if i == 0 {
			cmd = m.scheduleBranchRead(m.branchRun)
		} else {
			_, cmd = m.Update(branchRetryMsg{run: m.branchRun})
		}
		m.Update(cmd())
	}
	require.Error(t, m.branchRun.reloadErr)
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	require.True(t, m.dialog.ContainsDialog(dialog.CommandsID))
}
