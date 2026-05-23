package attachments

import (
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

// testAttachments creates an Attachments instance with plain styles for use in tests.
func testAttachments() *Attachments {
	r := NewRenderer(
		lipgloss.NewStyle(),
		lipgloss.NewStyle(),
		lipgloss.NewStyle(),
		lipgloss.NewStyle(),
		lipgloss.NewStyle(),
	)
	km := Keymap{
		DeleteMode: key.NewBinding(key.WithKeys("ctrl+r")),
		DeleteAll:  key.NewBinding(key.WithKeys("a")),
		Escape:     key.NewBinding(key.WithKeys("escape")),
	}

	return New(r, km)
}

func TestUpdate_SkillAttachmentDedup(t *testing.T) {
	t.Parallel()

	a := testAttachments()
	skill := SkillAttachment{Name: "grilling"}

	consumed1 := a.Update(skill)
	consumed2 := a.Update(skill)

	require.True(t, consumed1)
	require.True(t, consumed2)
	require.Len(t, a.SkillList(), 1)
}

func TestUpdate_SkillAttachmentAppendsDifferent(t *testing.T) {
	t.Parallel()

	a := testAttachments()

	consumed1 := a.Update(SkillAttachment{Name: "grilling"})
	consumed2 := a.Update(SkillAttachment{Name: "baking"})

	require.True(t, consumed1)
	require.True(t, consumed2)
	require.Len(t, a.SkillList(), 2)
}

func TestReset_ClearsBothLists(t *testing.T) {
	t.Parallel()

	a := testAttachments()
	a.Update(message.Attachment{FileName: "main.go"})
	a.Update(SkillAttachment{Name: "grilling"})

	a.Reset()

	require.Empty(t, a.List())
	require.Empty(t, a.SkillList())
}

func TestUpdate_FileAttachment(t *testing.T) {
	t.Parallel()

	a := testAttachments()

	consumed := a.Update(message.Attachment{FileName: "test.go"})

	require.True(t, consumed)
	require.Len(t, a.List(), 1)
}

func TestUpdate_UnhandledMessage(t *testing.T) {
	t.Parallel()

	a := testAttachments()

	consumed := a.Update(tea.WindowSizeMsg{})

	require.False(t, consumed)
}

func TestUpdate_DeleteDigit_DeletesFileByIndex(t *testing.T) {
	t.Parallel()

	a := testAttachments()
	a.Update(message.Attachment{FileName: "first.go"})
	a.Update(message.Attachment{FileName: "second.go"})
	a.Update(SkillAttachment{Name: "grilling"})

	// Enter delete mode via ctrl+r.
	a.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})

	// Delete the first file (index 0).
	a.Update(tea.KeyPressMsg{Code: '0'})

	require.Len(t, a.List(), 1)
	require.Equal(t, "second.go", a.List()[0].FileName)
	require.Len(t, a.SkillList(), 1)
}

func TestUpdate_DeleteDigit_DeletesSkillByIndex(t *testing.T) {
	t.Parallel()

	a := testAttachments()
	a.Update(message.Attachment{FileName: "main.go"})
	a.Update(SkillAttachment{Name: "grilling"})
	a.Update(SkillAttachment{Name: "baking"})

	// Enter delete mode via ctrl+r.
	a.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})

	// fileCount=1, so index 1 maps to skill index 0 ("grilling").
	a.Update(tea.KeyPressMsg{Code: '1'})

	require.Len(t, a.List(), 1)
	require.Len(t, a.SkillList(), 1)
	require.Equal(t, "baking", a.SkillList()[0].Name)
}

func TestUpdate_DeleteDigit_OutOfRange(t *testing.T) {
	t.Parallel()

	a := testAttachments()
	a.Update(message.Attachment{FileName: "only.go"})

	// Enter delete mode via ctrl+r.
	a.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})

	// Send an out-of-range digit — should not panic and should not remove the file.
	require.NotPanics(t, func() {
		a.Update(tea.KeyPressMsg{Code: '5'})
	})

	require.Len(t, a.List(), 1)
}
