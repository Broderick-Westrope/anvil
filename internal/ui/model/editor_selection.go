package model

import (
	"image"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
)

// editorSelectAllKey replaces the textarea's default ctrl+g, which is the
// global help key and handled before the textarea sees it.
const editorSelectAllKey = "ctrl+shift+a"

// editorAttachmentsRow is the row renderEditorView reserves above the
// textarea for attachment chips, drawn even when there are none.
const editorAttachmentsRow = 1

// newEditorTextarea builds the composer textarea. Its built-in copy binding
// is disabled because the UI copies through common.CopyToClipboard, which
// also writes OSC 52 (so it works over SSH) and reports success.
func newEditorTextarea(sty *styles.Styles) textarea.Model {
	ta := textarea.New()
	ta.SetStyles(sty.Editor.Textarea)
	ta.ShowLineNumbers = false
	ta.CharLimit = -1
	ta.SetVirtualCursor(false)
	ta.DynamicHeight = true
	ta.MinHeight = TextareaMinHeight
	ta.MaxHeight = TextareaMaxHeight
	ta.KeyMap.SelectAll = key.NewBinding(
		key.WithKeys(editorSelectAllKey),
		key.WithHelp(editorSelectAllKey, "select all"),
	)
	ta.KeyMap.CopySelection = key.NewBinding()
	return ta
}

// editorVisible reports whether the textarea is drawn, so mouse events in
// layout.editor can be routed to it.
func (m *UI) editorVisible() bool {
	if m.layout.editor.Dy() <= 0 || (m.isCompact && m.detailsOpen) {
		return false
	}
	switch m.state {
	case uiLanding:
		return true
	case uiChat:
		return !m.isDrilledIn()
	default:
		return false
	}
}

// textareaOrigin returns the screen cell of the textarea's top-left corner.
func (m *UI) textareaOrigin() image.Point {
	return image.Pt(m.layout.editor.Min.X, m.layout.editor.Min.Y+editorAttachmentsRow)
}

// handleTextareaMouse routes a mouse event to the textarea's selection. A
// left click inside the textarea starts a selection (and moves the cursor);
// while one is in progress, motion and release are routed to it wherever
// the pointer is, and the textarea clamps them, so dragging above the box
// selects to the start and below it to the end. It reports whether the
// event was consumed.
func (m *UI) handleTextareaMouse(msg tea.MouseMsg) bool {
	mouse := msg.Mouse()
	origin := m.textareaOrigin()
	rel := image.Pt(mouse.X-origin.X, mouse.Y-origin.Y)

	switch msg.(type) {
	case tea.MouseClickMsg:
		if mouse.Button != uv.MouseLeft || !m.editorVisible() {
			return false
		}
		area := image.Rect(0, 0, m.layout.editor.Dx(), m.textarea.Height())
		if !rel.In(area) {
			return false
		}
		if m.completionsOpen {
			m.closeCompletions()
		}
		if m.slashACOpen {
			m.closeSlashAC(false)
		}
		m.textareaMouseSelecting = true
		m.textarea.BeginSelection(rel.X, rel.Y)
		return true
	case tea.MouseMotionMsg:
		if !m.textareaMouseSelecting {
			return false
		}
		m.textarea.ExtendSelection(rel.X, rel.Y)
		return true
	case tea.MouseReleaseMsg:
		if !m.textareaMouseSelecting {
			return false
		}
		m.textarea.ExtendSelection(rel.X, rel.Y)
		m.textarea.EndSelection()
		m.textareaMouseSelecting = false
		return true
	}
	return false
}

// copyEditorSelection copies the textarea selection to the clipboard. With
// cut it also deletes the selection; otherwise it clears it.
func (m *UI) copyEditorSelection(cut bool) tea.Cmd {
	if !m.textarea.HasSelection() {
		return nil
	}
	text := m.textarea.SelectedText()
	if cut {
		prevHeight := m.textarea.Height()
		m.textarea.DeleteSelection()
		return tea.Batch(
			common.CopyToClipboard(text, "Selection cut to clipboard"),
			m.handleTextareaHeightChange(prevHeight),
		)
	}
	m.textarea.ClearSelection()
	return common.CopyToClipboard(text, "Selection copied to clipboard")
}

// pasteTextFromClipboard reads clipboard text and returns it as a
// tea.PasteMsg, so it takes the same path as a bracketed paste.
func (m *UI) pasteTextFromClipboard() tea.Msg {
	return clipboardTextPasteMsg(readClipboard)
}

func clipboardTextPasteMsg(read func(clipboardFormat) ([]byte, error)) tea.Msg {
	text, err := read(clipboardFormatText)
	if err != nil || len(text) == 0 {
		return util.InfoMsg{
			Type: util.InfoTypeError,
			Msg:  "Clipboard is empty or does not contain text",
		}
	}
	return tea.PasteMsg{Content: string(text)}
}
