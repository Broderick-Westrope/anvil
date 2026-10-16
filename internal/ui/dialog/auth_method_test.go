package dialog

import (
	"image"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

func newTestAuthMethod() *AuthMethod {
	s := styles.TokyoNight()
	return NewAuthMethod(
		&common.Common{Styles: &s},
		false,
		catwalk.Provider{ID: catwalk.InferenceProviderOpenAI, Name: "OpenAI"},
		config.SelectedModel{Provider: "openai", Model: "gpt-5.1"},
		config.SelectedModelTypeLarge,
	)
}

func TestAuthMethodDefaultsToOAuth(t *testing.T) {
	t.Parallel()

	m := newTestAuthMethod()
	require.Equal(t, AuthMethodID, m.ID())

	action := m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	selected, ok := action.(ActionSelectAuthMethod)
	require.True(t, ok)
	require.True(t, selected.UseOAuth)
	require.Equal(t, "openai", string(selected.Provider.ID))
	require.Equal(t, "gpt-5.1", selected.Model.Model)
	require.Equal(t, config.SelectedModelTypeLarge, selected.ModelType)
}

func TestAuthMethodTogglesToAPIKey(t *testing.T) {
	t.Parallel()

	m := newTestAuthMethod()
	m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})

	action := m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	selected, ok := action.(ActionSelectAuthMethod)
	require.True(t, ok)
	require.False(t, selected.UseOAuth)

	// Up toggles back to OAuth.
	m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
	action = m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	selected, ok = action.(ActionSelectAuthMethod)
	require.True(t, ok)
	require.True(t, selected.UseOAuth)

	// Left/right toggle as well, since the cards sit side by side.
	m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyRight})
	action = m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	selected, ok = action.(ActionSelectAuthMethod)
	require.True(t, ok)
	require.False(t, selected.UseOAuth)

	m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyLeft})
	action = m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	selected, ok = action.(ActionSelectAuthMethod)
	require.True(t, ok)
	require.True(t, selected.UseOAuth)
}

func TestAuthMethodClose(t *testing.T) {
	t.Parallel()

	m := newTestAuthMethod()
	action := m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	_, ok := action.(ActionClose)
	require.True(t, ok)
}

func TestAuthMethodVimKeysToggle(t *testing.T) {
	t.Parallel()

	m := newTestAuthMethod()
	m.HandleMsg(tea.KeyPressMsg{Code: 'l', Text: "l"})
	action := m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	selected, ok := action.(ActionSelectAuthMethod)
	require.True(t, ok)
	require.False(t, selected.UseOAuth)

	m = newTestAuthMethod()
	m.HandleMsg(tea.KeyPressMsg{Code: 'h', Text: "h"})
	action = m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	selected, ok = action.(ActionSelectAuthMethod)
	require.True(t, ok)
	require.False(t, selected.UseOAuth)
}

func TestAuthMethodMouseClickFocusesCard(t *testing.T) {
	t.Parallel()

	m := newTestAuthMethod()
	scr := uv.NewScreenBuffer(100, 30)
	m.Draw(scr, image.Rect(0, 0, 100, 30))
	require.False(t, m.oauthCardArea.Empty())
	require.False(t, m.apiKeyCardArea.Empty())

	action := m.HandleMsg(tea.MouseClickMsg(tea.Mouse{
		X:      m.apiKeyCardArea.Min.X + 1,
		Y:      m.apiKeyCardArea.Min.Y + 1,
		Button: tea.MouseLeft,
	}))
	require.Nil(t, action)
	require.Equal(t, 1, m.selected)

	// A click outside the cards changes nothing.
	action = m.HandleMsg(tea.MouseClickMsg(tea.Mouse{
		X:      0,
		Y:      0,
		Button: tea.MouseLeft,
	}))
	require.Nil(t, action)
	require.Equal(t, 1, m.selected)

	action = m.HandleMsg(tea.MouseClickMsg(tea.Mouse{
		X:      m.oauthCardArea.Min.X + 1,
		Y:      m.oauthCardArea.Min.Y + 1,
		Button: tea.MouseLeft,
	}))
	require.Nil(t, action)
	require.Equal(t, 0, m.selected)
}

func TestAuthMethodMouseDoubleClickConfirms(t *testing.T) {
	t.Parallel()

	m := newTestAuthMethod()
	scr := uv.NewScreenBuffer(100, 30)
	m.Draw(scr, image.Rect(0, 0, 100, 30))

	click := tea.MouseClickMsg(tea.Mouse{
		X:      m.apiKeyCardArea.Min.X + 1,
		Y:      m.apiKeyCardArea.Min.Y + 1,
		Button: tea.MouseLeft,
	})
	require.Nil(t, m.HandleMsg(click))
	action := m.HandleMsg(click)
	selected, ok := action.(ActionSelectAuthMethod)
	require.True(t, ok)
	require.False(t, selected.UseOAuth)

	oauthClick := leftClick(m.oauthCardArea.Min.X, m.oauthCardArea.Min.Y)
	require.Nil(t, m.HandleMsg(oauthClick))
	action = m.HandleMsg(oauthClick)
	selected, ok = action.(ActionSelectAuthMethod)
	require.True(t, ok)
	require.True(t, selected.UseOAuth)
}

func TestAuthMethodMouseDoubleClickThreshold(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		gap  time.Duration
		want bool
	}{
		"exactly at the threshold": {gap: authMethodDoubleClickThreshold, want: true},
		"just past the threshold":  {gap: authMethodDoubleClickThreshold + time.Nanosecond},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := newTestAuthMethod()
			scr := uv.NewScreenBuffer(100, 30)
			m.Draw(scr, image.Rect(0, 0, 100, 30))
			now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			m.now = func() time.Time { return now }
			click := leftClick(m.apiKeyCardArea.Min.X+1, m.apiKeyCardArea.Min.Y+1)

			require.Nil(t, m.HandleMsg(click))
			now = now.Add(tc.gap)
			_, confirmed := m.HandleMsg(click).(ActionSelectAuthMethod)

			require.Equal(t, tc.want, confirmed)
		})
	}
}

func TestAuthMethodMouseClicksOnDifferentCardsDoNotConfirm(t *testing.T) {
	t.Parallel()

	m := newTestAuthMethod()
	scr := uv.NewScreenBuffer(100, 30)
	m.Draw(scr, image.Rect(0, 0, 100, 30))
	oauthClick := leftClick(m.oauthCardArea.Min.X, m.oauthCardArea.Min.Y)
	apiKeyClick := leftClick(m.apiKeyCardArea.Min.X, m.apiKeyCardArea.Min.Y)

	require.Nil(t, m.HandleMsg(oauthClick))
	require.Nil(t, m.HandleMsg(apiKeyClick))
	require.Equal(t, 1, m.selected)

	// A click outside the cards in between breaks up a double click.
	require.Nil(t, m.HandleMsg(leftClick(0, 0)))
	require.Nil(t, m.HandleMsg(apiKeyClick))
	require.Equal(t, 1, m.selected)
}

// drawnBoxes returns the rounded boxes drawn on scr, in the order their
// top-left corners appear row by row.
func drawnBoxes(scr uv.ScreenBuffer) []image.Rectangle {
	bounds := scr.Bounds()
	content := func(x, y int) string {
		if cell := scr.CellAt(x, y); cell != nil {
			return cell.Content
		}
		return ""
	}
	var boxes []image.Rectangle
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if content(x, y) != "╭" {
				continue
			}
			right := x + 1
			for content(right, y) == "─" {
				right++
			}
			bottom := y + 1
			for content(x, bottom) == "│" {
				bottom++
			}
			if content(right, y) == "╮" && content(x, bottom) == "╰" && content(right, bottom) == "╯" {
				boxes = append(boxes, image.Rect(x, y, right+1, bottom+1))
			}
		}
	}
	return boxes
}

// boxText returns the text drawn inside box, one string per row.
func boxText(scr uv.ScreenBuffer, box image.Rectangle) string {
	var rows []string
	for y := box.Min.Y + 1; y < box.Max.Y-1; y++ {
		var row strings.Builder
		for x := box.Min.X + 1; x < box.Max.X-1; x++ {
			if cell := scr.CellAt(x, y); cell != nil {
				row.WriteString(cell.Content)
			}
		}
		rows = append(rows, strings.TrimSpace(row.String()))
	}
	return strings.Join(rows, "\n")
}

func TestAuthMethodCardAreasMatchDrawnCards(t *testing.T) {
	t.Parallel()

	padded := styles.TokyoNight()
	padded.Dialog.View = padded.Dialog.View.Margin(1, 2).Padding(1, 3)

	for name, tc := range map[string]struct {
		area       image.Rectangle
		onboarding bool
		styles     *styles.Styles
		sideBySide bool
	}{
		"centered dialog":                   {area: image.Rect(3, 2, 103, 42), sideBySide: true},
		"onboarding":                        {area: image.Rect(3, 2, 103, 42), onboarding: true, sideBySide: true},
		"padded dialog frame":               {area: image.Rect(3, 2, 103, 42), styles: &padded, sideBySide: true},
		"side by side at the minimum width": {area: image.Rect(3, 2, 50, 42), sideBySide: true},
		"stacked below the minimum width":   {area: image.Rect(3, 2, 49, 52)},
		"stacked in onboarding":             {area: image.Rect(3, 2, 47, 52), onboarding: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m := newTestAuthMethod()
			if tc.styles != nil {
				m.com = &common.Common{Styles: tc.styles}
			}
			m.isOnboarding = tc.onboarding
			scr := uv.NewScreenBuffer(tc.area.Max.X+3, tc.area.Max.Y+3)
			m.Draw(scr, tc.area)

			boxes := drawnBoxes(scr)
			if tc.onboarding {
				require.Len(t, boxes, 2)
			} else {
				require.Len(t, boxes, 3, "the dialog frame and both cards")
				frame := boxes[0]
				require.True(t, frame.In(tc.area), "frame %v outside %v", frame, tc.area)
				require.Equal(t, min(defaultAuthMethodDialogMaxWidth, tc.area.Dx()-2), frame.Dx())
				require.Equal(t, frame.Min.X+frame.Max.X, tc.area.Min.X+tc.area.Max.X, "dialog is centered")
				boxes = boxes[1:]
			}
			require.Equal(t, boxes[0], m.oauthCardArea)
			require.Equal(t, boxes[1], m.apiKeyCardArea)
			require.Equal(t, "ChatGPT Account\nwith Subscription", strings.Trim(boxText(scr, boxes[0]), "\n"))
			require.Equal(t, "API Key", strings.Trim(boxText(scr, boxes[1]), "\n"))
			if tc.sideBySide {
				require.Equal(t, boxes[0].Min.Y, boxes[1].Min.Y)
				require.Equal(t, boxes[0].Max.X+authMethodCardGap, boxes[1].Min.X)
			} else {
				require.Equal(t, boxes[0].Min.X, boxes[1].Min.X)
				require.Equal(t, boxes[0].Max.Y+1, boxes[1].Min.Y)
			}

			// The bottom-right cell of a card is still on it.
			last := boxes[1].Max.Sub(image.Pt(1, 1))
			require.Nil(t, m.HandleMsg(leftClick(last.X, last.Y)))
			require.Equal(t, 1, m.selected)
		})
	}
}

func TestAuthMethodDrawsPromptAndFocusedCard(t *testing.T) {
	t.Parallel()

	m := newTestAuthMethod()
	sty := m.com.Styles
	scr := uv.NewScreenBuffer(100, 40)
	area := image.Rect(0, 0, 100, 40)
	focusedBorder := sty.Dialog.AuthMethod.CardFocused.GetBorderTopForeground()
	blurredBorder := sty.Dialog.AuthMethod.CardBlurred.GetBorderTopForeground()
	borderColor := func(box image.Rectangle) any {
		return scr.CellAt(box.Min.X, box.Min.Y).Style.Fg
	}

	m.Draw(scr, area)
	findOnScreen(t, scr, "How would you like to authenticate?")
	boxes := drawnBoxes(scr)
	require.Len(t, boxes, 3)
	require.Equal(t, focusedBorder, borderColor(boxes[1]))
	require.Equal(t, blurredBorder, borderColor(boxes[2]))

	m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyRight})
	scr = uv.NewScreenBuffer(100, 40)
	m.Draw(scr, area)
	boxes = drawnBoxes(scr)
	require.Equal(t, blurredBorder, borderColor(boxes[1]))
	require.Equal(t, focusedBorder, borderColor(boxes[2]))
}

func TestAuthMethodOAuthCardNamesTheProviderAccount(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		provider catwalk.Provider
		label    string
	}{
		"openai": {catwalk.Provider{ID: catwalk.InferenceProviderOpenAI, Name: "OpenAI"}, "ChatGPT Account"},
		"xai":    {catwalk.Provider{ID: catwalk.InferenceProviderXAI, Name: "xAI"}, "Grok Account"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m := newTestAuthMethod()
			m.provider = tc.provider
			scr := uv.NewScreenBuffer(100, 40)
			m.Draw(scr, image.Rect(0, 0, 100, 40))
			boxes := drawnBoxes(scr)
			require.Len(t, boxes, 3)
			require.Equal(t, tc.label+"\nwith Subscription", strings.Trim(boxText(scr, boxes[1]), "\n"))
		})
	}
}
