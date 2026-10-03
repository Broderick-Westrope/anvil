package dialog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/fantasy"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/fsext"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/permission/segment"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestPermissions_ViewRequestDisplaysFile(t *testing.T) {
	t.Parallel()
	for _, selector := range []string{"skill_name", "file_path"} {
		t.Run(selector, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			workingDir := filepath.Join(dir, "work")
			require.NoError(t, os.Mkdir(workingDir, 0o700))
			skillPath := filepath.Join(dir, "SKILL.md")
			require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: external\ndescription: External skill\n---\nbody"), 0o600))
			registry := []*skills.Skill{{Name: "external", SkillFilePath: skillPath}}
			permissions := permission.NewPermissionService(workingDir, config.YoloOff, nil, nil)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, tools.SessionIDContextKey, "test-session")
			requests := permissions.Subscribe(ctx)
			tool := tools.NewViewTool(nil, permissions, nil, skills.NewTracker(registry), registry, workingDir)
			params := tools.ViewParams{SkillName: "external"}
			want := tools.ViewPermissionsParams{SkillName: "external", FilePath: skillPath}
			if selector == "file_path" {
				params = tools.ViewParams{FilePath: filepath.Join("..", "SKILL.md"), Offset: 2, Limit: 10}
				want = tools.ViewPermissionsParams(params)
			}
			input, err := json.Marshal(params)
			require.NoError(t, err)
			call := fantasy.ToolCall{ID: "view-call", Name: tools.ViewToolName, Input: string(input)}
			type outcome struct {
				response fantasy.ToolResponse
				err      error
			}
			done := make(chan outcome, 1)
			go func() {
				response, runErr := tool.Run(ctx, call)
				done <- outcome{response, runErr}
			}()
			var request permission.PermissionRequest
			select {
			case event := <-requests:
				request = event.Payload
				permissions.Deny(request, "denied in test")
			case result := <-done:
				t.Fatalf("expected permission request, got %+v", result)
			case <-ctx.Done():
				t.Fatal("permission request timed out")
			}
			select {
			case result := <-done:
				require.NoError(t, result.err)
				require.True(t, result.response.IsError)
				require.Contains(t, result.response.Content, "denied in test")
			case <-ctx.Done():
				t.Fatal("view did not finish after denial")
			}
			sty := styles.TokyoNight()
			dialog := NewPermissions(&common.Common{Styles: &sty}, request)
			rendered := ansi.Strip(dialog.renderContent(len(skillPath) + 80))
			require.Contains(t, rendered, "File: "+fsext.PrettyPath(want.FilePath))
			require.Equal(t, want, request.Params)
			require.Equal(t, skillPath, request.Input)
			require.Equal(t, call.ID, request.ToolCallID)
			if selector == "file_path" {
				require.Contains(t, rendered, "Starting from line: 3")
				require.Contains(t, rendered, "Lines to read: 10")
			}
		})
	}
}

func newTestPermissions(t *testing.T) *Permissions {
	t.Helper()
	s := styles.TokyoNight()
	com := &common.Common{Styles: &s}
	perm := permission.PermissionRequest{
		ID:         "perm-test",
		ToolCallID: "tool-call-test",
		ToolName:   "bash",
		Input:      "git status",
	}
	return NewPermissions(com, perm)
}

// escalationSummary is an escalated verdict with every kind of trigger.
func escalationSummary() *permission.AssessmentSummary {
	return &permission.AssessmentSummary{
		Outcome: "escalate",
		Scores: []permission.AssessmentScore{
			{Name: "remote_exec", Value: 0.91, Max: 1, Trigger: permission.TriggerDeny},
			{Name: "destructive", Value: 0.62, Max: 1, Trigger: permission.TriggerEscalate},
			{Name: "exfiltration", Value: 0.1, Max: 1},
			{Name: "credentials", Value: 0.02, Max: 1},
			{Name: "shared_infra", Value: 0.04, Max: 1},
			{Name: permission.SeverityAxis, Value: 2.4, Max: 3, Trigger: permission.TriggerEscalate},
			{Name: permission.UserRequestedAxis, Value: 0.88, Max: 1, Trigger: permission.TriggerMitigate},
		},
	}
}

// TestPermissions_RenderHeaderShowsAssessmentSummary verifies the verdict is
// added under the header without changing the lines above it, and that
// every axis is shown.
func TestPermissions_RenderHeaderShowsAssessmentSummary(t *testing.T) {
	t.Parallel()

	const width = 100

	withoutNote := newTestPermissions(t)
	base := ansi.Strip(withoutNote.renderHeader(width))
	require.NotContains(t, base, "Bouncer")

	p := newTestPermissions(t)
	p.permission.Bouncer = escalationSummary()
	p.permission.BouncerNote = p.permission.Bouncer.Note()
	noted := ansi.Strip(p.renderHeader(width))

	baseLines := strings.Split(base, "\n")
	notedLines := strings.Split(noted, "\n")
	require.Equal(t, baseLines, notedLines[:len(baseLines)], "lines before the verdict must be unaffected")
	block := strings.Join(notedLines[len(baseLines):], "\n")
	require.Contains(t, block, "Bouncer Escalate")
	for _, want := range []string{"remote exec 0.91", "destructive 0.62", "exfiltration 0.10", "credentials 0.02", "shared infra 0.04", "severity 2.4/3", "user requested 0.88"} {
		require.Contains(t, block, want)
	}
}

// TestPermissions_AssessmentSummaryWrapsInsteadOfTruncating verifies a narrow
// dialog wraps the axes onto more lines, never cutting one off, and keeps
// every line within the content width.
func TestPermissions_AssessmentSummaryWrapsInsteadOfTruncating(t *testing.T) {
	t.Parallel()

	const width = 40
	p := newTestPermissions(t)
	p.permission.Bouncer = escalationSummary()
	block := p.renderBouncer(width)
	plain := ansi.Strip(block)

	lines := strings.Split(plain, "\n")
	require.Greater(t, len(lines), 2, "the axes should wrap at this width")
	for _, line := range lines {
		require.LessOrEqual(t, ansi.StringWidth(line), width, "line %q overflows", line)
	}
	require.NotContains(t, plain, "…")
	for _, sc := range p.permission.Bouncer.Scores {
		require.Contains(t, strings.Join(strings.Fields(plain), " "), formatAssessmentScore(sc))
	}
	// Continuation lines are indented under the value, not the key.
	require.True(t, strings.HasPrefix(lines[1], strings.Repeat(" ", len("Bouncer "))))
}

// TestPermissions_AssessmentSummaryHighlightsTriggers verifies each axis is
// styled by the effect it had, so the cause of an escalation stands out.
func TestPermissions_AssessmentSummaryHighlightsTriggers(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	ps := p.com.Styles.Dialog.Permissions
	for _, tt := range []struct {
		trigger string
		want    lipgloss.Style
	}{
		{permission.TriggerDeny, ps.BouncerDeny},
		{permission.TriggerEscalate, ps.BouncerEscalate},
		{permission.TriggerMitigate, ps.BouncerMitigate},
		{"", ps.BouncerScore},
	} {
		sc := permission.AssessmentScore{Name: "destructive", Value: 0.62, Max: 1, Trigger: tt.trigger}
		p.permission.Bouncer = &permission.AssessmentSummary{Outcome: "escalate", Scores: []permission.AssessmentScore{sc}}
		require.Contains(t, p.renderBouncer(100), tt.want.Render(formatAssessmentScore(sc)), "trigger %q", tt.trigger)
	}
	require.NotEqual(t, ps.BouncerScore.Render("x"), ps.BouncerEscalate.Render("x"), "escalating axes must look different")
	require.NotEqual(t, ps.BouncerEscalate.Render("x"), ps.BouncerDeny.Render("x"), "deny and escalate must look different")
}

// TestPermissions_BouncerSkippedAndShadow covers the verdicts without
// scores and the shadow label.
func TestPermissions_BouncerSkippedAndShadow(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.permission.Bouncer = &permission.AssessmentSummary{Outcome: "skipped", Detail: "protected path"}
	require.Equal(t, "Bouncer Skipped · protected path", strings.TrimSpace(ansi.Strip(p.renderBouncer(80))))

	p.permission.Bouncer = &permission.AssessmentSummary{Shadow: true, Outcome: "allow", Scores: []permission.AssessmentScore{{Name: "destructive", Value: 0.05, Max: 1}}}
	got := ansi.Strip(p.renderBouncer(80))
	require.Contains(t, got, "Allow (shadow)")
	require.Contains(t, got, "destructive 0.05")
}

// TestPermissions_BouncerNoteFallbackWraps verifies a plain note with no
// structured summary wraps instead of being truncated.
func TestPermissions_BouncerNoteFallbackWraps(t *testing.T) {
	t.Parallel()

	const width = 40
	p := newTestPermissions(t)
	p.permission.BouncerNote = "bouncer: escalate · " + strings.Repeat("axis=0.50 ", 10)
	plain := ansi.Strip(p.renderBouncer(width))
	require.NotContains(t, plain, "…")
	require.Greater(t, len(strings.Split(plain, "\n")), 1)
	require.Equal(t, 10, strings.Count(plain, "axis=0.50"))
}

func TestWrapStyled(t *testing.T) {
	t.Parallel()
	require.Equal(t, []string{"aa  bb", "cc"}, wrapStyled([]string{"aa", "bb", "cc"}, 6, "  "))
	require.Equal(t, []string{"abcd…"}, wrapStyled([]string{"abcdefgh"}, 5, "  "))
	require.Nil(t, wrapStyled(nil, 10, "  "))
}

// TestPermissions_ActionKeysResolve verifies that action keys produce the
// correct permission response.
func TestPermissions_ActionKeysResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key    tea.KeyPressMsg
		action PermissionAction
	}{
		{keyMsg('a'), PermissionAllow},
		{keyMsg('A'), PermissionAllow},
		{keyMsg('s'), PermissionAllowForSession},
		{keyMsg('S'), PermissionAllowForSession},
	}

	for _, tc := range tests {
		p := newTestPermissions(t)
		action := p.HandleMsg(tc.key)
		resp, ok := action.(ActionPermissionResponse)
		require.Truef(t, ok, "key %q should produce ActionPermissionResponse", tc.key.Text)
		require.Equal(t, tc.action, resp.Action)
	}
}

// TestPermissions_DenyKeyEntersDenyReasonState verifies that d/D keys
// enter the deny-reason input state instead of immediately denying.
func TestPermissions_DenyKeyEntersDenyReasonState(t *testing.T) {
	t.Parallel()

	for _, r := range []rune{'d', 'D'} {
		p := newTestPermissions(t)
		action := p.HandleMsg(keyMsg(r))
		require.Nil(t, action, "key %q should not produce an immediate response", string(r))
		require.True(t, p.denyReasonVisible, "key %q should enter deny-reason state", string(r))
	}
}

// TestPermissions_DenyReasonEnterConfirms verifies that enter in deny-reason
// state emits a deny response with the typed reason.
func TestPermissions_DenyReasonEnterConfirms(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.denyReasonVisible = true
	p.denyReasonInput = "not safe"

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, PermissionDeny, resp.Action)
	require.Equal(t, "not safe", resp.Reason)
}

// TestPermissions_DenyReasonEscapeReturnsToDefault verifies that escape
// in deny-reason state returns to the default button state without denying.
func TestPermissions_DenyReasonEscapeReturnsToDefault(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.denyReasonVisible = true
	p.denyReasonInput = "partial"

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Nil(t, action, "escape should not produce a response")
	require.False(t, p.denyReasonVisible, "escape should exit deny-reason state")
	require.Empty(t, p.denyReasonInput, "escape should clear the reason input")
	require.Equal(t, 3, p.selectedOption, "selection should be on Deny button")
}

// TestPermissions_ForeverKeyExpandsSubChoice verifies that f key enters
// the forever-expanded state.
func TestPermissions_ForeverKeyExpandsSubChoice(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	action := p.HandleMsg(keyMsg('f'))
	require.Nil(t, action, "f should not produce an immediate response")
	require.True(t, p.foreverExpanded, "f should enter forever-expanded state")
	require.Equal(t, 0, p.selectedOption, "selection should reset to 0")
}

// TestPermissions_ForeverProjectAndUserKeys verifies that p/u keys in
// forever-expanded state emit the correct scope.
func TestPermissions_ForeverProjectAndUserKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key   rune
		scope config.Scope
	}{
		{'p', config.ScopeWorkspace},
		{'P', config.ScopeWorkspace},
		{'u', config.ScopeGlobal},
		{'U', config.ScopeGlobal},
	}

	for _, tc := range tests {
		p := newTestPermissions(t)
		p.foreverExpanded = true
		action := p.HandleMsg(keyMsg(tc.key))
		resp, ok := action.(ActionPermissionResponse)
		require.Truef(t, ok, "key %q in forever state should produce response", string(tc.key))
		require.Equal(t, PermissionAllowForever, resp.Action)
		require.Equal(t, tc.scope, resp.Scope)
	}
}

// TestPermissions_ForeverEscapeReturnsToDefault verifies that escape in
// forever-expanded state returns to the default state.
func TestPermissions_ForeverEscapeReturnsToDefault(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.foreverExpanded = true

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Nil(t, action, "escape in forever state should not emit a response")
	require.False(t, p.foreverExpanded, "should return to default state")
	require.Equal(t, 2, p.selectedOption, "should select Forever button")
}

// TestPermissions_CtrlFTogglesFullscreen verifies that ctrl+f toggles
// fullscreen (previously bound to f).
func TestPermissions_CtrlFTogglesFullscreen(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	// Make it a diff view tool so fullscreen toggle is allowed.
	p.permission.ToolName = "edit"

	require.False(t, p.fullscreen)
	p.HandleMsg(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	require.True(t, p.fullscreen, "ctrl+f should toggle fullscreen on")
	p.HandleMsg(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	require.False(t, p.fullscreen, "ctrl+f should toggle fullscreen off")
}

func newLongBashPermissions(t *testing.T, lines int) *Permissions {
	t.Helper()
	script := make([]string, lines)
	for i := range script {
		script[i] = "echo line " + strconv.Itoa(i)
	}
	p := newTestPermissions(t)
	p.permission.Params = tools.BashPermissionsParams{Command: strings.Join(script, "\n")}
	return p
}

// drawnSize draws the dialog onto a screen of the given size and returns
// the bounding box of the non-blank cells.
func drawnSize(t *testing.T, p *Permissions, width, height int) (int, int) {
	t.Helper()
	scr := uv.NewScreenBuffer(width, height)
	p.Draw(scr, uv.Rect(0, 0, width, height))
	minX, minY, maxX, maxY := width, height, -1, -1
	for y := range height {
		for x := range width {
			if cell := scr.CellAt(x, y); cell != nil && cell.Content != "" && cell.Content != " " {
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x), max(maxY, y)
			}
		}
	}
	return maxX - minX + 1, maxY - minY + 1
}

// TestPermissions_LongContentGrowsDialog verifies that simple prompts with
// long content, like multi-line scripts, get the larger dialog size while
// short prompts stay compact.
func TestPermissions_LongContentGrowsDialog(t *testing.T) {
	t.Parallel()

	const screenW, screenH = 200, 60
	shortW, shortH := drawnSize(t, newTestPermissions(t), screenW, screenH)
	longW, longH := drawnSize(t, newLongBashPermissions(t, 200), screenW, screenH)

	require.LessOrEqual(t, shortH, int(screenH*simpleHeightRatio))
	require.Greater(t, longH, int(screenH*simpleHeightRatio), "long content should use more height")
	require.Greater(t, longW, shortW, "long content should use more width")
	require.LessOrEqual(t, longH, screenH-expandedVerticalMargin)
	require.Greater(t, longH, int(screenH*diffSizeRatio), "long content should use nearly the full height")
}

// TestPermissions_FullscreenForSimpleContent verifies that ctrl+f makes a
// non-diff prompt fill the screen.
func TestPermissions_FullscreenForSimpleContent(t *testing.T) {
	t.Parallel()

	const screenW, screenH = 200, 60
	p := newLongBashPermissions(t, 200)
	p.HandleMsg(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	require.True(t, p.fullscreen)

	w, h := drawnSize(t, p, screenW, screenH)
	require.Equal(t, screenW, w)
	require.GreaterOrEqual(t, h, screenH-1, "fullscreen should use the full height")
}

// TestPermissions_PageKeysScrollContent verifies page and home/end keys
// move the content viewport.
func TestPermissions_PageKeysScrollContent(t *testing.T) {
	t.Parallel()

	p := newLongBashPermissions(t, 200)
	drawnSize(t, p, 200, 60)
	require.Zero(t, p.viewport.YOffset())

	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgDown})
	afterPage := p.viewport.YOffset()
	require.Positive(t, afterPage)

	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnd})
	require.True(t, p.viewport.AtBottom())

	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgUp})
	require.True(t, !p.viewport.AtBottom())

	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyHome})
	require.Zero(t, p.viewport.YOffset())
}

// TestPermissions_BashCommandIsHighlighted verifies the bash command is
// syntax highlighted while keeping its text intact.
func TestPermissions_BashCommandIsHighlighted(t *testing.T) {
	t.Parallel()

	const command = "for f in *.go; do\n  echo \"$f\"\ndone"
	p := newTestPermissions(t)
	p.permission.Params = tools.BashPermissionsParams{Command: command}

	rendered := p.renderContent(80)
	plain := newTestPermissions(t).renderContentPanel(command, 80)
	require.NotEqual(t, plain, rendered, "command should be highlighted")
	for line := range strings.SplitSeq(command, "\n") {
		require.Contains(t, ansi.Strip(rendered), line)
	}
}

// TestPermissions_NavigationCyclesOptions verifies that tab and arrow keys
// cycle through the four permission options.
func TestPermissions_NavigationCyclesOptions(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	require.Equal(t, 0, p.selectedOption)

	// Tab cycles forward.
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 1, p.selectedOption)

	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 2, p.selectedOption)

	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 3, p.selectedOption)

	// Wrap around.
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, 0, p.selectedOption)

	// Left cycles backward.
	p.HandleMsg(keyMsg('h'))
	require.Equal(t, 3, p.selectedOption)
}

// TestPermissions_EnterConfirmsSelection verifies that enter confirms the
// currently selected option.
func TestPermissions_EnterConfirmsSelection(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.selectedOption = 1 // Session.

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, PermissionAllowForSession, resp.Action)
}

// TestPermissions_EnterOnForeverExpandsSubChoice verifies that enter on
// the Forever button expands the sub-choice instead of emitting a response.
func TestPermissions_EnterOnForeverExpandsSubChoice(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.selectedOption = 2 // Forever.

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Nil(t, action, "enter on Forever should expand, not emit")
	require.True(t, p.foreverExpanded)
}

// TestPermissions_EnterOnDenyEntersDenyReasonState verifies that enter on
// the Deny button enters the deny-reason input state.
func TestPermissions_EnterOnDenyEntersDenyReasonState(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	p.selectedOption = 3 // Deny.

	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Nil(t, action, "enter on Deny should enter reason state, not emit")
	require.True(t, p.denyReasonVisible)
}

// TestPermissions_EscapeDenies verifies that escape denies the request.
func TestPermissions_EscapeDenies(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	action := p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, PermissionDeny, resp.Action)
}

// TestPermissions_RespondIncludesPattern verifies that the response includes
// the permission input as the pattern (from the text input).
func TestPermissions_RespondIncludesPattern(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	// Pattern input should be pre-filled with the permission input.
	require.Equal(t, "git status", p.patternInput.Value())

	action := p.HandleMsg(keyMsg('a'))
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, "git status", resp.Pattern)
}

// TestPermissions_PatternEditable verifies that the pattern can be edited
// and the modified value is used in the response.
func TestPermissions_PatternEditable(t *testing.T) {
	t.Parallel()

	p := newTestPermissions(t)
	// The pattern input should be pre-filled with the permission input.
	require.Equal(t, p.permission.Input, p.patternInput.Value())

	// Focus the pattern input.
	action := p.HandleMsg(keyMsg('e'))
	require.Nil(t, action)
	require.True(t, p.patternFocused)

	// Clear and type a glob pattern.
	p.patternInput.SetValue("internal/*.go")

	// Unfocus.
	p.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.False(t, p.patternFocused)

	// Session grant should use the edited pattern.
	action = p.HandleMsg(keyMsg('s'))
	resp, ok := action.(ActionPermissionResponse)
	require.True(t, ok)
	require.Equal(t, "internal/*.go", resp.Pattern)
}

// TestPermissions_SegmentsPrefillGeneralizedPatterns verifies that when a
// permission request carries input segments, the pattern input is pre-filled
// with the generalized pattern for each segment joined by " && ".
func TestPermissions_SegmentsPrefillGeneralizedPatterns(t *testing.T) {
	t.Parallel()

	s := styles.TokyoNight()
	com := &common.Common{Styles: &s}
	perm := permission.PermissionRequest{
		ID:            "perm-test",
		ToolCallID:    "tool-call-test",
		ToolName:      "bash",
		Input:         "cd /foo && go test ./...",
		InputSegments: []string{"cd /foo", "go test ./..."},
	}
	p := NewPermissions(com, perm)

	require.Equal(t, "cd * && go test *", p.patternInput.Value())
}

// TestPermissions_SegmentsPrefillDedupesPatterns verifies that normalised
// segment variants which generalise to the same pattern appear once.
func TestPermissions_SegmentsPrefillDedupesPatterns(t *testing.T) {
	t.Parallel()

	s := styles.TokyoNight()
	com := &common.Common{Styles: &s}
	command := `git commit -m "hello world"`
	perm := permission.PermissionRequest{
		ID:            "perm-test",
		ToolCallID:    "tool-call-test",
		ToolName:      "bash",
		Input:         command,
		InputSegments: segment.Split(command),
	}
	p := NewPermissions(com, perm)

	require.Len(t, perm.InputSegments, 2)
	require.Equal(t, "git commit *", p.patternInput.Value())
}
