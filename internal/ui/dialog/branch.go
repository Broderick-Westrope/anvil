package dialog

import (
	"context"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	uv "github.com/charmbracelet/ultraviolet"
)

var _ Dialog = (*Branch)(nil)

// Branch is a branch picker dialog that shows user messages on the current
// branch path for quick branching.
type Branch struct {
	com   *common.Common
	list  *list.FilterableList
	input textinput.Model

	keyMap struct {
		Select   key.Binding
		Next     key.Binding
		Previous key.Binding
		Close    key.Binding
	}
}

// NewBranch creates a new Branch dialog for the given session's branch path.
func NewBranch(com *common.Common, sessionID string, leafMessageID string) (*Branch, error) {
	branchPath, err := com.Workspace.GetBranchPath(context.TODO(), leafMessageID)
	if err != nil {
		return nil, err
	}

	// Filter to only user messages.
	var userMsgs []message.Message
	for _, msg := range branchPath {
		if msg.MessageType == message.MessageTypeMessage && msg.Role == message.User {
			userMsgs = append(userMsgs, msg)
		}
	}

	b := &Branch{
		com: com,
	}

	// Build list items from filtered messages.
	items := make([]list.FilterableItem, len(userMsgs))
	for i, msg := range userMsgs {
		items[i] = NewBranchItem(com.Styles, msg)
	}

	b.list = list.NewFilterableList(items...)
	b.list.Focus()

	b.input = textinput.New()
	b.input.SetVirtualCursor(false)
	b.input.Placeholder = "Filter messages..."
	b.input.SetStyles(com.Styles.TextInput)
	b.input.Focus()

	b.keyMap.Select = key.NewBinding(
		key.WithKeys("enter", "tab", "ctrl+y"),
		key.WithHelp("enter", "choose"),
	)
	b.keyMap.Next = key.NewBinding(
		key.WithKeys("down", "ctrl+n"),
		key.WithHelp("↓", "next item"),
	)
	b.keyMap.Previous = key.NewBinding(
		key.WithKeys("up", "ctrl+p"),
		key.WithHelp("↑", "previous item"),
	)
	b.keyMap.Close = CloseKey

	return b, nil
}

// ID implements [Dialog].
func (b *Branch) ID() string {
	return BranchID
}

// HandleMsg implements [Dialog].
func (b *Branch) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, b.keyMap.Close):
			return ActionClose{}

		case key.Matches(msg, b.keyMap.Select):
			if item := b.list.SelectedItem(); item != nil {
				branchItem := item.(*BranchItem)
				return ActionNavigateTree{
					MessageID:       branchItem.msg.ID,
					ParentMessageID: branchItem.msg.ParentMessageID,
					Role:            message.User,
					Content:         messageTextContent(branchItem.msg),
				}
			}

		case key.Matches(msg, b.keyMap.Previous):
			b.list.Focus()
			if b.list.IsSelectedFirst() {
				b.list.SelectLast()
			} else {
				b.list.SelectPrev()
			}
			b.list.ScrollToSelected()

		case key.Matches(msg, b.keyMap.Next):
			b.list.Focus()
			if b.list.IsSelectedLast() {
				b.list.SelectFirst()
			} else {
				b.list.SelectNext()
			}
			b.list.ScrollToSelected()

		default:
			var cmd tea.Cmd
			b.input, cmd = b.input.Update(msg)
			value := b.input.Value()
			b.list.SetFilter(value)
			b.list.ScrollToTop()
			b.list.SetSelected(0)
			return ActionCmd{cmd}
		}
	}
	return nil
}

// Cursor returns the cursor position relative to the dialog.
func (b *Branch) Cursor() *tea.Cursor {
	return InputCursor(b.com.Styles, b.input.Cursor())
}

// Draw implements [Dialog].
func (b *Branch) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := b.com.Styles
	width := max(0, min(defaultDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	height := max(0, min(defaultDialogHeight, area.Dy()-t.Dialog.View.GetVerticalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()
	heightOffset := t.Dialog.Title.GetVerticalFrameSize() + titleContentHeight +
		t.Dialog.InputPrompt.GetVerticalFrameSize() + inputContentHeight +
		t.Dialog.View.GetVerticalFrameSize()
	b.input.SetWidth(max(0, innerWidth-t.Dialog.InputPrompt.GetHorizontalFrameSize()-1)) // (1) cursor padding
	b.list.SetSize(innerWidth, height-heightOffset)

	rc := NewRenderContext(t, width)
	rc.Title = "Branch From"

	inputView := t.Dialog.InputPrompt.Render(b.input.View())
	cur := b.Cursor()
	rc.AddPart(inputView)

	listView := t.Dialog.List.Height(b.list.Height()).Render(b.list.Render())
	rc.AddPart(listView)

	view := rc.Render()
	DrawCenterCursor(scr, area, view, cur)
	return cur
}


