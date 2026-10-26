package chat

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// UserMessageItem represents a user message in the chat UI.
type UserMessageItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	attachments *attachments.Renderer
	message     *message.Message
	sty         *styles.Styles
}

// NewUserMessageItem creates a new UserMessageItem.
func NewUserMessageItem(sty *styles.Styles, message *message.Message, attachments *attachments.Renderer) MessageItem {
	v := list.NewVersioned()
	return &UserMessageItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		attachments:              attachments,
		message:                  message,
		sty:                      sty,
	}
}

// Finished implements list.Item. User messages are immutable once
// submitted, so the entry is always safe to freeze.
func (m *UserMessageItem) Finished() bool {
	return true
}

// RawRender implements [MessageItem].
func (m *UserMessageItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)

	content, height, ok := m.getCachedRender(cappedWidth)
	// cache hit
	if ok {
		return m.renderHighlighted(content, cappedWidth, height)
	}

	msgContent := m.displayContent()
	renderer := common.UserMarkdownRenderer(m.sty, cappedWidth)
	mu := common.LockMarkdownRenderer(renderer)

	mu.Lock()
	result, err := renderer.Render(msgContent)
	mu.Unlock()

	if err != nil {
		content = msgContent
	} else {
		content = strings.TrimSuffix(result, "\n")
	}

	if len(m.message.BinaryContent()) > 0 {
		attachmentsStr := m.renderAttachments(cappedWidth)
		if content == "" {
			content = attachmentsStr
		} else {
			content = strings.Join([]string{content, "", attachmentsStr}, "\n")
		}
	}

	height = lipgloss.Height(content)
	m.setCachedRender(content, cappedWidth, height)
	return m.renderHighlighted(content, cappedWidth, height)
}

// displayContent returns the markdown source shown for the message. An
// injected message shows each injection under a label naming its source,
// instead of the tagged text the model sees.
func (m *UserMessageItem) displayContent() string {
	if injections := m.message.Injections(); injections != nil {
		var blocks []string
		for _, inj := range injections {
			blocks = append(blocks, injectionBlock(inj))
		}
		return strings.Join(blocks, "\n\n")
	}
	msgContent := skills.CollapseContentXML(strings.TrimSpace(m.message.Content().Text))
	return commands.CollapseExpansionXML(msgContent)
}

// SelectionSource implements [list.SourceSelectable].
func (m *UserMessageItem) SelectionSource() string {
	return m.displayContent()
}

// Render implements MessageItem.
func (m *UserMessageItem) Render(width int) string {
	// Bypass the prefix cache while a highlight range is active so
	// selection drags reflect immediately without invalidating the
	// cache. Highlight changes are intentionally applied "above" the
	// prefix cache.
	useCache := !m.isHighlighted()
	var key uint64
	if m.focused {
		key = 1
	}
	if useCache {
		if cached, ok := m.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}
	var prefix string
	if m.focused {
		prefix = m.sty.Messages.UserFocused.Render()
	} else {
		prefix = m.sty.Messages.UserBlurred.Render()
	}
	lines := strings.Split(m.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	out := strings.Join(lines, "\n")
	if useCache {
		m.setCachedPrefixedRender(out, width, key)
	}
	return out
}

// ID implements MessageItem.
func (m *UserMessageItem) ID() string {
	return m.message.ID
}

func (m *UserMessageItem) SourceMessage() message.Message {
	return m.message.Clone()
}

// renderAttachments renders attachments.
func (m *UserMessageItem) renderAttachments(width int) string {
	var attachments []message.Attachment
	for _, at := range m.message.BinaryContent() {
		attachments = append(attachments, message.Attachment{
			FileName: at.Path,
			MimeType: at.MIMEType,
		})
	}
	// This message is already posted, so the attachment can't be removed;
	// don't render the remove button.
	return m.attachments.Render(attachments, nil, false, false, width)
}

// HandleKeyEvent implements KeyEventHandler.
func (m *UserMessageItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
	if k := key.String(); k == "c" || k == "y" {
		text := m.message.Content().Text
		return true, common.CopyToClipboard(text, "Message copied to clipboard")
	}
	return false, nil
}

// injectionBlock renders one injection as a bold source label, followed by
// its text unless the harness wrote it.
func injectionBlock(inj message.InjectionContent) string {
	switch inj.Kind {
	case message.InjectionKindRetry:
		return fmt.Sprintf("**Harness · retry %d/%d after %s**", inj.Attempt, inj.MaxAttempts, inj.Reason)
	case message.InjectionKindContinue:
		return "**Harness · continue after interrupt**"
	case message.InjectionKindDiagramFix:
		return "**Harness · fix diagrams that don't render**"
	case message.InjectionKindResume:
		return "**Orchestrator · resume**\n\n" + inj.Text
	case message.InjectionKindInterruptSteer:
		return "**You · interrupt**\n\n" + inj.Text
	case message.InjectionKindSteer:
		return "**You · steer**\n\n" + inj.Text
	default:
		// An unknown kind is labelled by its source, so nothing passes as
		// the user's own message.
		return fmt.Sprintf("**%s**\n\n%s", injectionSourceLabels[inj.Source], inj.Text)
	}
}

var injectionSourceLabels = map[message.InjectionSource]string{
	message.InjectionSourceHuman:        "You",
	message.InjectionSourceOrchestrator: "Orchestrator",
	message.InjectionSourceHarness:      "Harness",
}
