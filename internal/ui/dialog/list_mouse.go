package dialog

import (
	"image"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
)

// listDoubleClickThreshold is the longest gap between two clicks on the
// same row that still counts as a double click.
const listDoubleClickThreshold = 400 * time.Millisecond

// mouseList is the part of a dialog list the mouse needs.
type mouseList interface {
	ScrollBy(lines int)
	ItemIndexAtPosition(x, y int) (int, int)
	SetSelected(index int)
}

// listMouse gives a dialog's list wheel scrolling, click-to-select and
// double-click-to-confirm. The dialog records the list's screen area on
// every Draw with setArea and routes mouse messages through wheel and
// click.
type listMouse struct {
	// area is the list's cells on screen, from the last Draw.
	area image.Rectangle
	// scrolled is set by the wheel and cleared by the keyboard, so a
	// dialog that keeps its selection in view on Draw stops snapping
	// back while the user is scrolling with the mouse.
	scrolled bool

	lastClick    time.Time
	lastClickIdx int
	now          func() time.Time
}

// setArea records where the list was drawn. bounds is relative to the
// dialog (RenderContext.ListBounds); dialogRect is where the dialog was
// drawn on screen. The area is clipped to the drawn dialog.
func (lm *listMouse) setArea(bounds image.Rectangle, dialogRect uv.Rectangle) {
	if bounds.Empty() {
		lm.area = image.Rectangle{}
		return
	}
	lm.area = bounds.Add(dialogRect.Min).Intersect(dialogRect)
}

// wheel scrolls l when the pointer is over the list. It reports whether
// the event was used.
func (lm *listMouse) wheel(msg common.CoalescedWheelMsg, l mouseList) bool {
	if !image.Pt(msg.Mouse.X, msg.Mouse.Y).In(lm.area) || msg.DeltaY == 0 {
		return false
	}
	l.ScrollBy(int(msg.DeltaY))
	lm.scrolled = true
	lm.resetClick()
	return true
}

// click selects the row under a left click. selectable, when non-nil,
// rejects rows that can't be selected, such as group headers. It reports
// whether this click completed a double click on the row.
func (lm *listMouse) click(msg tea.MouseClickMsg, l mouseList, selectable func(int) bool) bool {
	pt := image.Pt(msg.X, msg.Y)
	if msg.Button != tea.MouseLeft || !pt.In(lm.area) {
		lm.resetClick()
		return false
	}
	idx, _ := l.ItemIndexAtPosition(pt.X-lm.area.Min.X, pt.Y-lm.area.Min.Y)
	if idx < 0 || (selectable != nil && !selectable(idx)) {
		lm.resetClick()
		return false
	}
	now := lm.clock()
	double := idx == lm.lastClickIdx && !lm.lastClick.IsZero() &&
		now.Sub(lm.lastClick) <= listDoubleClickThreshold
	l.SetSelected(idx)
	if double {
		lm.resetClick()
		return true
	}
	lm.lastClick = now
	lm.lastClickIdx = idx
	return false
}

// keyboard records keyboard input: the selection should be kept in view
// again, and a pending first click no longer starts a double click.
func (lm *listMouse) keyboard() {
	lm.scrolled = false
	lm.resetClick()
}

// resetClick forgets the pending first click. lastClickIdx is left as
// is: with no first click recorded it can't complete a double click.
func (lm *listMouse) resetClick() {
	lm.lastClick = time.Time{}
}

func (lm *listMouse) clock() time.Time {
	if lm.now != nil {
		return lm.now()
	}
	return time.Now()
}

// handle routes msg for a dialog's list and reports whether it consumed
// it. Key presses are never consumed: they only mark the keyboard as in
// use. onClick, when non-nil, runs before a click inside the list is
// applied (to leave a filter mode, say). confirm runs on a double click.
func (lm *listMouse) handle(msg tea.Msg, l mouseList, selectable func(int) bool, onClick func(), confirm func() Action) (Action, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		lm.keyboard()
	case common.CoalescedWheelMsg:
		return nil, lm.wheel(msg, l)
	case tea.MouseClickMsg:
		if onClick != nil && msg.Button == tea.MouseLeft && image.Pt(msg.X, msg.Y).In(lm.area) {
			onClick()
		}
		if lm.click(msg, l, selectable) {
			return confirm(), true
		}
		return nil, true
	}
	return nil, false
}

// confirmKey is what a double click sends: every list dialog confirms its
// selection with Enter.
var confirmKey = tea.KeyPressMsg{Code: tea.KeyEnter}
