package model

import (
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestRenderHeaderDetails_BouncerIndicator(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Providers: csync.NewMap[string, config.ProviderConfig]()}

	t.Run("shown when enforce", func(t *testing.T) {
		t.Parallel()
		com := common.DefaultCommon(&testWorkspace{cfg: cfg, bouncerConfigured: true, bouncerMode: permission.BouncerEnforce})
		got := renderHeaderDetails(com, nil, 0, false, 200)
		require.Contains(t, got, "bouncer:enforce")
	})

	t.Run("hidden when off", func(t *testing.T) {
		t.Parallel()
		com := common.DefaultCommon(&testWorkspace{cfg: cfg, bouncerConfigured: false, bouncerMode: permission.BouncerOff})
		got := renderHeaderDetails(com, nil, 0, false, 200)
		require.NotContains(t, got, "bouncer:")
	})
}

func renderCompactHeader(t *testing.T, title string, width int) string {
	t.Helper()
	cfg := &config.Config{Providers: csync.NewMap[string, config.ProviderConfig]()}
	com := common.DefaultCommon(&testWorkspace{cfg: cfg})
	h := newHeader(com)
	sess := &session.Session{ID: "s1", Title: title}
	scr := uv.NewScreenBuffer(width, 1)
	h.drawHeader(scr, uv.Rect(0, 0, width, 1), sess, true, false, width)
	return strings.TrimRight(ansi.Strip(scr.Render()), " ")
}

func TestCompactHeader_SessionTitle(t *testing.T) {
	t.Parallel()

	t.Run("placed right after logo", func(t *testing.T) {
		t.Parallel()
		got := renderCompactHeader(t, "Add session title to compact header", 120)
		require.True(t, strings.HasPrefix(got, " ANVIL Add session title to compact header "), got)
		require.True(t, strings.HasSuffix(got, "ctrl+d open"), got)
	})

	t.Run("placeholder titles hidden", func(t *testing.T) {
		t.Parallel()
		for _, title := range []string{"", "  ", agent.DefaultSessionName, "New Session"} {
			got := renderCompactHeader(t, title, 120)
			require.NotContains(t, got, "Session", "title %q", title)
			require.True(t, strings.HasPrefix(got, " ANVIL "), got)
		}
	})

	t.Run("title truncated before metadata", func(t *testing.T) {
		t.Parallel()
		got := renderCompactHeader(t, strings.Repeat("long title ", 10), 80)
		require.Contains(t, got, "…")
		require.True(t, strings.HasSuffix(got, "ctrl+d open"), got)
		require.LessOrEqual(t, ansi.StringWidth(got), 80)
	})

	t.Run("title keeps minimum width when narrow", func(t *testing.T) {
		t.Parallel()
		got := renderCompactHeader(t, "Add session title to compact header", 40)
		require.Contains(t, got, " ANVIL Add session…")
		require.LessOrEqual(t, ansi.StringWidth(got), 40)
	})

	t.Run("newlines collapsed", func(t *testing.T) {
		t.Parallel()
		got := renderCompactHeader(t, "Fix\nthe   bug", 120)
		require.Contains(t, got, " ANVIL Fix the bug ")
	})
}
