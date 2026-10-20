package chat

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestDiagramDetailItemStates(t *testing.T) {
	t.Parallel()

	const src = "flowchart LR\n  A --> B\n"
	sty := styles.TokyoNight()
	tests := map[string]struct {
		view    DiagramView
		want    []string
		wantNot []string
	}{
		"pending says it is rendering": {
			view:    DiagramView{State: DiagramPending},
			want:    []string{"Rendering diagram"},
			wantNot: []string{"A --> B"},
		},
		"unsupported shows the code block": {
			view:    DiagramView{State: DiagramUnsupported},
			want:    []string{"A --> B"},
			wantNot: []string{"Rendering diagram", "not rendered"},
		},
		"failed shows the code block and why": {
			view:    DiagramView{State: DiagramFailed, Err: "bad syntax"},
			want:    []string{"A --> B", "Diagram not rendered: bad syntax"},
			wantNot: []string{"Rendering diagram"},
		},
		"ready shows the image": {
			view:    DiagramView{State: DiagramReady, Image: "[[FULL]]"},
			want:    []string{"[[FULL]]", "open in browser"},
			wantNot: []string{"Rendering diagram", "A --> B"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			item := NewDiagramDetailItem(&sty, src, fakeDiagrams{full: map[string]DiagramView{src: tc.view}})
			out := ansi.Strip(item.RawRender(80))
			for _, want := range tc.want {
				require.Contains(t, out, want)
			}
			for _, not := range tc.wantNot {
				require.NotContains(t, out, not)
			}
		})
	}
}

func TestDiagramDetailItemKeys(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	item := NewDiagramDetailItem(&sty, "A\n", fakeDiagrams{})
	handled, cmd := item.HandleKeyEvent(tea.KeyPressMsg{Code: 'o', Text: "o"})
	require.True(t, handled)
	require.Equal(t, DiagramOpenMsg{Source: "A\n", External: true}, cmdMsg(t, cmd))
	handled, _ = item.HandleKeyEvent(tea.KeyPressMsg{Code: 'x', Text: "x"})
	require.False(t, handled)
}

func TestDiagramDetailItemCopiesTheSource(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	item := NewDiagramDetailItem(&sty, "A\n", fakeDiagrams{})
	for _, key := range []string{"c", "y"} {
		handled, cmd := item.HandleKeyEvent(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		require.True(t, handled, key)
		require.NotNil(t, cmd, key)
	}
}
