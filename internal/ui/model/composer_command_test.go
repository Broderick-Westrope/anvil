package model

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
)

type composerSkillWorkspace struct {
	workspace.Workspace
	active map[string]*skills.Skill
}

func (w *composerSkillWorkspace) ActiveSkillByName(name string) *skills.Skill {
	return w.active[name]
}

func TestCommandComposerSkillRoundTrip(t *testing.T) {
	for _, entry := range []string{"history", "shift+b", dialog.BranchID, dialog.TreeID} {
		for _, tt := range []struct {
			name     string
			declared []string
			chips    []string
			display  string
			unknown  bool
		}{
			{name: "declared and chips", declared: []string{"command-one", "missing", "command-two"}, chips: []string{"one", "two"}},
			{name: "no declared skills", chips: []string{"one"}},
			{name: "display name", declared: []string{"command-one"}, chips: []string{"one", "two"}, display: "plugin:review"},
			{name: "overlapping names", declared: []string{"command-one"}, chips: []string{"command-one", "two"}},
			{name: "unknown command", declared: []string{"command-one"}, chips: []string{"one"}, unknown: true},
		} {
			t.Run(entry+"/"+tt.name, func(t *testing.T) {
				m, f, _ := branchUI(t)
				w := &composerSkillWorkspace{Workspace: m.com.Workspace, active: map[string]*skills.Skill{
					"command-one": {Name: "command-one", Instructions: "first declared", Source: "user"},
					"command-two": {Name: "command-two", Instructions: "second declared"},
				}}
				m.com.Workspace = w
				m.customCommands = []commands.CustomCommand{{Name: "review", Content: "Review $ARGUMENTS", Skills: tt.declared, DisplayName: tt.display}}
				var want []attachments.SkillAttachment
				for _, name := range tt.chips {
					chip := attachments.SkillAttachment{Name: name, Instructions: "attached " + name}
					if active := w.active[name]; active != nil {
						chip.Source = active.Source
					}
					want = append(want, chip)
					m.attachments.Update(chip)
				}
				line := "/review args"
				if tt.display != "" {
					line = "/" + tt.display + " args"
				}
				collectMsgs(m.tryExecuteSlashCommand(line))
				stored, err := f.Workspace.ListUserMessages(f.Context, m.session.ID)
				require.NoError(t, err)
				require.Len(t, stored, 2)
				var source message.Message
				for _, msg := range stored {
					if strings.Contains(msg.Content().Text, "<command_expansion") {
						source = msg
					}
				}
				require.NotEmpty(t, source.ID)
				require.NoError(t, f.Workspace.MoveLeaf(f.Context, m.session.ID, source.ID))
				m.session.LeafMessageID = source.ID
				if tt.unknown {
					m.customCommands = nil
					want = nil
				}
				w.active = nil
				for i := range want {
					want[i].Source = ""
				}
				m.attachments.Reset()
				switch entry {
				case "history":
					m.Update(m.loadPromptHistory()())
					for m.textarea.Value() != line && m.historyPrev() {
					}
				default:
					m.installBranchSnapshot([]message.Message{source}, nil)
					m.chat.SelectLast()
					if entry == "shift+b" {
						pressBranch(t, m)
					} else {
						require.Nil(t, m.openDialog(entry))
						_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
						completeNavigation(t, m, cmd)
					}
				}
				require.Equal(t, line, m.textarea.Value())
				require.Equal(t, want, m.attachments.SkillList())
				if !tt.unknown {
					w.active = map[string]*skills.Skill{
						"command-one": {Name: "command-one", Instructions: "first declared"},
						"command-two": {Name: "command-two", Instructions: "second declared"},
					}
					collectMsgs(m.tryExecuteSlashCommand(m.textarea.Value()))
					resent, err := f.Workspace.ListUserMessages(f.Context, m.session.ID)
					require.NoError(t, err)
					require.Len(t, resent, 3)
					for _, msg := range resent {
						if msg.ID != stored[0].ID && msg.ID != stored[1].ID {
							require.Equal(t, source.Content().Text, msg.Content().Text)
						}
					}
				}
			})
		}
	}
}

func TestDialogCommandComposerSkills(t *testing.T) {
	m, f, _ := branchUI(t)
	m.com.Workspace = &composerSkillWorkspace{Workspace: m.com.Workspace, active: map[string]*skills.Skill{
		"declared": {Name: "declared", Instructions: "command instructions"},
	}}
	m.customCommands = []commands.CustomCommand{{Name: "review", Skills: []string{"missing", "declared", "also-missing"}}}
	m.attachments.Update(attachments.SkillAttachment{Name: "chip", Instructions: "not sent by dialog"})
	m.dialog.OpenDialog(dialog.NewArguments(m.com, "Arguments", "", []commands.Argument{{ID: rawArgumentsID}}, dialog.ActionRunCustomCommand{
		Name: "review", Content: "Review $ARGUMENTS", Skills: m.customCommands[0].Skills,
	}))
	m.handleDialogMsg(tea.KeyPressMsg{Code: 'a', Text: "args"})
	for _, msg := range collectMsgs(m.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})) {
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, cmd := range batch {
				collectMsgs(cmd)
			}
		}
	}
	stored, err := f.Workspace.ListUserMessages(f.Context, m.session.ID)
	require.NoError(t, err)
	require.Len(t, stored, 2)
	for _, source := range stored {
		if !strings.Contains(source.Content().Text, "<command_expansion") {
			continue
		}
		require.NotContains(t, source.Content().Text, "not sent by dialog")
		require.Equal(t, composerSnapshot{text: "/review args"}, composerFromMessage(source, nil, m.customCommands))
		return
	}
	t.Fatal("dialog command was not persisted")
}

func TestComposerDraftSharesBytesAndSurvivesDeletion(t *testing.T) {
	m, _ := composerRestoreUI(t, "text")
	m.attachments.Update(message.Attachment{FileName: "second.txt", Content: []byte("second")})
	m.attachments.Update(attachments.SkillAttachment{Name: "second", Instructions: "second"})
	m.updateHistoryDraft("")
	draft := m.promptHistory.draft
	require.Same(t, &m.attachments.List()[0].Content[0], &draft.attachments[0].Content[0])
	m.attachments.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m.attachments.Update(tea.KeyPressMsg{Code: '0'})
	require.Len(t, m.attachments.List(), 1)
	m.attachments.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	m.attachments.Update(tea.KeyPressMsg{Code: '1'})
	require.Len(t, m.attachments.SkillList(), 1)
	m.attachments.Reset()
	m.restoreComposer(draft)
	require.Equal(t, []string{"draft.png", "second.txt"}, []string{m.attachments.List()[0].FileName, m.attachments.List()[1].FileName})
	require.Equal(t, []byte("draft bytes"), m.attachments.List()[0].Content)
	require.Equal(t, []string{"draft", "second"}, []string{m.attachments.SkillList()[0].Name, m.attachments.SkillList()[1].Name})
}

func TestComposerResourceLabels(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ path, want string }{
		{"file:///x/y.txt", "y.txt"},
		{"mcp://server/res", "res"},
		{"mcp://server/res?query=x/y#part", "res"},
		{"mcp://server/", "server"},
		{"file:///", "attachment"},
		{"", "attachment"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			state := composerFromMessage(message.Message{Parts: []message.ContentPart{message.BinaryContent{Path: tt.path}}}, nil, nil)
			require.Equal(t, tt.want, state.attachments[0].FileName)
			require.Equal(t, tt.path, state.attachments[0].FilePath)
		})
	}
}
