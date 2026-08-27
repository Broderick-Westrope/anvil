package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func newTestQuitPinned(t *testing.T) *QuitPinned {
	t.Helper()
	s := styles.TokyoNight()
	com := &common.Common{Styles: &s}
	sess := session.Session{
		ID:      "sess-1",
		Title:   "Pinned session",
		Pinned:  true,
		PinNote: "waiting on upstream",
	}
	return NewQuitPinned(com, sess)
}

func enterKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter}
}

func ctrlCKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
}

func escKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEscape}
}

func tabKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyTab}
}

// TestQuitPinned_EnterKeepsPinAndQuits verifies that Enter on the default
// choice settles with no unpin and no note change (no DB write).
func TestQuitPinned_EnterKeepsPinAndQuits(t *testing.T) {
	t.Parallel()

	q := newTestQuitPinned(t)
	action := q.HandleMsg(enterKey())
	settled, ok := action.(ActionQuitSettled)
	require.True(t, ok, "enter should emit ActionQuitSettled")
	require.Equal(t, "sess-1", settled.SessionID)
	require.False(t, settled.Unpin)
	require.False(t, settled.NoteChanged)
}

// TestQuitPinned_CtrlCSettlesNeverQuitAction verifies that ctrl+c emits
// ActionQuitSettled (keep pin & quit), never ActionQuit, which would
// bounce off the pinned-quit backstop and re-open the dialog in a loop.
func TestQuitPinned_CtrlCSettlesNeverQuitAction(t *testing.T) {
	t.Parallel()

	q := newTestQuitPinned(t)
	action := q.HandleMsg(ctrlCKey())
	settled, ok := action.(ActionQuitSettled)
	require.True(t, ok, "ctrl+c should emit ActionQuitSettled, never ActionQuit")
	require.False(t, settled.Unpin)
	require.False(t, settled.NoteChanged)
}

// TestQuitPinned_EscCancelsQuit verifies that Esc cancels the quit.
func TestQuitPinned_EscCancelsQuit(t *testing.T) {
	t.Parallel()

	q := newTestQuitPinned(t)
	action := q.HandleMsg(escKey())
	_, ok := action.(ActionClose)
	require.True(t, ok, "esc should emit ActionClose")
}

// TestQuitPinned_UnpinChoiceSettlesWithUnpin verifies that selecting the
// unpin choice and confirming emits ActionQuitSettled with Unpin set.
func TestQuitPinned_UnpinChoiceSettlesWithUnpin(t *testing.T) {
	t.Parallel()

	q := newTestQuitPinned(t)
	require.Nil(t, q.HandleMsg(tabKey()))
	action := q.HandleMsg(enterKey())
	settled, ok := action.(ActionQuitSettled)
	require.True(t, ok)
	require.True(t, settled.Unpin)
	require.Equal(t, "sess-1", settled.SessionID)
}

// TestQuitPinned_EditNoteUnchangedNoWrite verifies that saving an
// unchanged note settles with NoteChanged false (no DB write).
func TestQuitPinned_EditNoteUnchangedNoWrite(t *testing.T) {
	t.Parallel()

	q := newTestQuitPinned(t)
	// Navigate to "Edit note" and confirm to enter editing mode.
	q.HandleMsg(tabKey())
	q.HandleMsg(tabKey())
	require.Nil(t, q.HandleMsg(enterKey()))
	require.True(t, q.editing)

	// Enter without modifying the pre-filled note.
	action := q.HandleMsg(enterKey())
	settled, ok := action.(ActionQuitSettled)
	require.True(t, ok)
	require.False(t, settled.Unpin)
	require.False(t, settled.NoteChanged)
	require.Equal(t, "waiting on upstream", settled.Note)
}

// TestQuitPinned_EditNoteChangedSettlesWithNote verifies that a modified
// note settles with NoteChanged true and the new note.
func TestQuitPinned_EditNoteChangedSettlesWithNote(t *testing.T) {
	t.Parallel()

	q := newTestQuitPinned(t)
	q.HandleMsg(tabKey())
	q.HandleMsg(tabKey())
	q.HandleMsg(enterKey())
	require.True(t, q.editing)

	q.HandleMsg(keyMsg('!'))
	action := q.HandleMsg(enterKey())
	settled, ok := action.(ActionQuitSettled)
	require.True(t, ok)
	require.False(t, settled.Unpin)
	require.True(t, settled.NoteChanged)
	require.Equal(t, "waiting on upstream!", settled.Note)
}

// TestQuitPinned_CtrlCWhileEditingStillSettles verifies that ctrl+c keeps
// pin & quits even while editing the note.
func TestQuitPinned_CtrlCWhileEditingStillSettles(t *testing.T) {
	t.Parallel()

	q := newTestQuitPinned(t)
	q.HandleMsg(tabKey())
	q.HandleMsg(tabKey())
	q.HandleMsg(enterKey())
	require.True(t, q.editing)

	action := q.HandleMsg(ctrlCKey())
	settled, ok := action.(ActionQuitSettled)
	require.True(t, ok)
	require.False(t, settled.Unpin)
	require.False(t, settled.NoteChanged)
}
