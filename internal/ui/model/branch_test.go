package model

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

type branchTestWorkspace struct {
	workspace.Workspace
	cfg         *config.Config
	agentReady  bool
	agentBusy   bool
	queuedCount int
}

func (w *branchTestWorkspace) Config() *config.Config        { return w.cfg }
func (w *branchTestWorkspace) AgentIsReady() bool            { return w.agentReady }
func (w *branchTestWorkspace) AgentIsBusy() bool             { return w.agentBusy }
func (w *branchTestWorkspace) AgentQueuedPrompts(string) int { return w.queuedCount }

func newBranchTestConfig() *config.Config {
	providers := csync.NewMap[string, config.ProviderConfig]()
	return &config.Config{
		Providers: providers,
		Agents: map[string]config.Agent{
			config.AgentOrchestrator: {Model: string(config.SelectedModelTypeLarge)},
		},
		Models: map[config.SelectedModelType]config.SelectedModel{},
	}
}

func newBranchTestUI(t *testing.T, ws *branchTestWorkspace) *UI {
	t.Helper()
	com := common.DefaultCommon(ws)
	m := &UI{
		com:     com,
		keyMap:  DefaultKeyMap(),
		chat:    NewChat(com),
		focus:   uiFocusMain,
		session: &session.Session{ID: "sess-1", LeafMessageID: "leaf-1"},
	}
	m.textarea = textarea.New()
	m.attachments = attachments.New(
		attachments.NewRenderer(
			com.Styles.Attachments.Normal,
			com.Styles.Attachments.Deleting,
			com.Styles.Attachments.Image,
			com.Styles.Attachments.Text,
			com.Styles.Attachments.Skill,
			com.Styles.Attachments.Remove,
		),
		attachments.Keymap{
			DeleteMode: m.keyMap.Editor.AttachmentDeleteMode,
			DeleteAll:  m.keyMap.Editor.DeleteAllAttachments,
			Escape:     m.keyMap.Editor.Escape,
		},
	)
	m.chat.Focus()
	m.status = NewStatus(com, m)
	return m
}

func newTestSty() styles.Styles {
	return styles.TokyoNight()
}

func setChatSelection(m *UI, item chat.MessageItem) {
	m.chat.SetMessages(item)
	m.chat.SelectLast()
}

func TestTryStartBranchPreview_UserTarget(t *testing.T) {
	t.Parallel()

	sty := newTestSty()
	ws := &branchTestWorkspace{cfg: newBranchTestConfig(), agentReady: true}
	m := newBranchTestUI(t, ws)

	rawText := "<skill_content name=\"foo\">\ninstr\n</skill_content>\n\n/review please"
	msg := &message.Message{
		ID:   "user-1",
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: rawText},
			message.BinaryContent{Path: "/tmp/x.txt", MIMEType: "text/plain", Data: []byte("hello")},
		},
	}
	item := chat.NewUserMessageItem(&sty, msg, m.attachments.Renderer())
	setChatSelection(m, item)

	m.textarea.SetValue("original draft")

	m.tryStartBranchPreview()
	require.NotNil(t, m.branchPreview, "eligible preview must be installed")
	require.Equal(t, "user-1", m.branchPreview.targetID)
	require.Equal(t, message.User, m.branchPreview.targetRole)
	require.Equal(t, rawText, m.textarea.Value(), "raw text including skill/command XML must be preserved verbatim")
	require.Equal(t, "original draft", m.branchPreview.originalDraft.text)
	require.Len(t, m.attachments.List(), 1)
	require.Equal(t, "text/plain", m.attachments.List()[0].MimeType)
}

func TestTryStartBranchPreview_AssistantTargetIsBlank(t *testing.T) {
	t.Parallel()

	sty := newTestSty()
	ws := &branchTestWorkspace{cfg: newBranchTestConfig(), agentReady: true}
	m := newBranchTestUI(t, ws)

	msg := &message.Message{
		ID:   "asst-1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: "final answer"},
			message.Finish{Reason: message.FinishReasonEndTurn},
		},
	}
	item := chat.NewAssistantMessageItem(&sty, msg)
	setChatSelection(m, item)

	m.tryStartBranchPreview()
	require.NotNil(t, m.branchPreview)
	require.Equal(t, message.Assistant, m.branchPreview.targetRole)
	require.Empty(t, m.textarea.Value(), "assistant continuation starts with a blank composer")
}

func TestTryStartBranchPreview_RejectsAssistantWithToolCalls(t *testing.T) {
	t.Parallel()

	sty := newTestSty()
	ws := &branchTestWorkspace{cfg: newBranchTestConfig(), agentReady: true}
	m := newBranchTestUI(t, ws)

	msg := &message.Message{
		ID:   "asst-2",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "t1", Name: "bash", Finished: true},
			message.Finish{Reason: message.FinishReasonToolUse},
		},
	}
	item := chat.NewAssistantMessageItem(&sty, msg)
	setChatSelection(m, item)

	cmd := m.tryStartBranchPreview()
	require.NotNil(t, cmd, "assistant with tool calls must be rejected")
	require.Nil(t, m.branchPreview)
}

func TestTryStartBranchPreview_RejectsWhenAgentBusy(t *testing.T) {
	t.Parallel()

	sty := newTestSty()
	ws := &branchTestWorkspace{cfg: newBranchTestConfig(), agentReady: true, agentBusy: true}
	m := newBranchTestUI(t, ws)

	msg := &message.Message{ID: "user-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}
	item := chat.NewUserMessageItem(&sty, msg, m.attachments.Renderer())
	setChatSelection(m, item)

	cmd := m.tryStartBranchPreview()
	require.NotNil(t, cmd)
	require.Nil(t, m.branchPreview)
}

func TestTryStartBranchPreview_RejectsWhenQueued(t *testing.T) {
	t.Parallel()

	sty := newTestSty()
	ws := &branchTestWorkspace{cfg: newBranchTestConfig(), agentReady: true, queuedCount: 1}
	m := newBranchTestUI(t, ws)

	msg := &message.Message{ID: "user-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}
	item := chat.NewUserMessageItem(&sty, msg, m.attachments.Renderer())
	setChatSelection(m, item)

	cmd := m.tryStartBranchPreview()
	require.NotNil(t, cmd)
	require.Nil(t, m.branchPreview)
}

func TestTryStartBranchPreview_RejectsSubSession(t *testing.T) {
	t.Parallel()

	sty := newTestSty()
	ws := &branchTestWorkspace{cfg: newBranchTestConfig(), agentReady: true}
	m := newBranchTestUI(t, ws)
	m.session.ParentSessionID = "parent-1"

	msg := &message.Message{ID: "user-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}
	item := chat.NewUserMessageItem(&sty, msg, m.attachments.Renderer())
	setChatSelection(m, item)

	cmd := m.tryStartBranchPreview()
	require.NotNil(t, cmd)
	require.Nil(t, m.branchPreview)
}

func TestTryStartBranchPreview_BlockedByPendingMutation(t *testing.T) {
	t.Parallel()

	sty := newTestSty()
	ws := &branchTestWorkspace{cfg: newBranchTestConfig(), agentReady: true}
	m := newBranchTestUI(t, ws)
	m.beginMutation(mutationTreeNav)

	msg := &message.Message{ID: "user-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}
	item := chat.NewUserMessageItem(&sty, msg, m.attachments.Renderer())
	setChatSelection(m, item)

	cmd := m.tryStartBranchPreview()
	require.NotNil(t, cmd)
	require.Nil(t, m.branchPreview)

	m.endMutation(mutationTreeNav)
	require.False(t, m.mutationsPending())
	m.tryStartBranchPreview()
	require.NotNil(t, m.branchPreview)
}

func TestCancelBranchPreview_RestoresExactOriginalState(t *testing.T) {
	t.Parallel()

	sty := newTestSty()
	ws := &branchTestWorkspace{cfg: newBranchTestConfig(), agentReady: true}
	m := newBranchTestUI(t, ws)

	m.textarea.SetValue("my draft")
	m.attachments.Update(message.Attachment{FileName: "a.txt", MimeType: "text/plain", Content: []byte("x")})
	m.attachments.Update(attachments.SkillAttachment{Name: "skill-a", Instructions: "do things"})
	m.promptHistory.messages = []string{"one", "two"}
	m.promptHistory.index = 1
	m.promptHistory.draft = "pending"
	m.focus = uiFocusMain

	msg := &message.Message{ID: "user-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "target text"}}}
	item := chat.NewUserMessageItem(&sty, msg, m.attachments.Renderer())
	setChatSelection(m, item)

	m.tryStartBranchPreview()
	require.NotNil(t, m.branchPreview)
	require.Equal(t, "target text", m.textarea.Value())

	m.cancelBranchPreview()

	require.Nil(t, m.branchPreview)
	require.Equal(t, "my draft", m.textarea.Value())
	require.Len(t, m.attachments.List(), 1)
	require.Equal(t, "a.txt", m.attachments.List()[0].FileName)
	require.Len(t, m.attachments.SkillList(), 1)
	require.Equal(t, "skill-a", m.attachments.SkillList()[0].Name)
	require.Equal(t, []string{"one", "two"}, m.promptHistory.messages)
	require.Equal(t, 1, m.promptHistory.index)
	require.Equal(t, "pending", m.promptHistory.draft)
	require.Equal(t, uiFocusMain, m.focus)
}

func TestTryStartBranchPreview_RejectsSecondPreview(t *testing.T) {
	t.Parallel()

	sty := newTestSty()
	ws := &branchTestWorkspace{cfg: newBranchTestConfig(), agentReady: true}
	m := newBranchTestUI(t, ws)

	msg := &message.Message{ID: "user-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}}
	item := chat.NewUserMessageItem(&sty, msg, m.attachments.Renderer())
	setChatSelection(m, item)

	m.tryStartBranchPreview()
	first := m.branchPreview
	require.NotNil(t, m.tryStartBranchPreview())
	require.Same(t, first, m.branchPreview, "a second B must not replace an active preview")
}

func TestTrackMutation_BeginsAndEndsAroundCommandResult(t *testing.T) {
	t.Parallel()

	ws := &branchTestWorkspace{cfg: newBranchTestConfig(), agentReady: true}
	m := newBranchTestUI(t, ws)

	inner := func() tea.Msg { return util.NewInfoMsg("done") }
	cmd := m.trackMutation("op-1", inner)
	require.True(t, m.mutationsPending())

	result := cmd()
	done, ok := result.(mutationDoneMsg)
	require.True(t, ok)
	require.Equal(t, "op-1", done.id)

	m.endMutation(done.id)
	require.False(t, m.mutationsPending())
}
