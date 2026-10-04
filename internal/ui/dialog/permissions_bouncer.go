package dialog

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/charmbracelet/x/ansi"
)

// bouncerScoreGap separates score tokens on a line.
const bouncerScoreGap = "  "

// renderBouncer renders the bouncer's verdict as a key/value block that
// wraps to width, so every axis stays visible. Axes that crossed a routing
// threshold are highlighted by the effect they had.
func (p *Permissions) renderBouncer(width int) string {
	t := p.com.Styles
	sum := p.permission.Bouncer
	if sum == nil {
		if p.permission.BouncerNote == "" {
			return ""
		}
		note := capitalizeFirst(p.permission.BouncerNote)
		return t.Dialog.Permissions.KeyText.Width(width).Render(note)
	}

	keyStr := t.Dialog.Permissions.KeyText.Render("Bouncer")
	valueWidth := max(1, width-lipgloss.Width(keyStr)-1)

	head := p.bouncerOutcomeStyle(sum.Outcome).Render(capitalizeFirst(sum.Outcome))
	if sum.Shadow {
		head += t.Dialog.Permissions.BouncerScore.Render(" (shadow)")
	}
	if sum.Detail != "" {
		head += t.Dialog.Permissions.BouncerScore.Render(" · " + sum.Detail)
	}
	if p.bouncerDenied() {
		head += t.Dialog.Permissions.BouncerScore.Render(" · blocked unless you allow it")
	}
	lines := wrapStyled([]string{head}, valueWidth, " ")

	tokens := make([]string, len(sum.Scores))
	for i, sc := range sum.Scores {
		tokens[i] = p.bouncerScoreStyle(sc.Trigger).Render(formatAssessmentScore(sc))
	}
	lines = append(lines, wrapStyled(tokens, valueWidth, bouncerScoreGap)...)

	value := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return lipgloss.JoinHorizontal(lipgloss.Top, keyStr, " ", value)
}

// formatAssessmentScore renders one axis as "remote exec 0.91" or, for the
// severity scale, "severity 2.4/3".
func formatAssessmentScore(sc permission.AssessmentScore) string {
	name := strings.ReplaceAll(sc.Name, "_", " ")
	if sc.Max > 1 {
		return fmt.Sprintf("%s %.1f/%g", name, sc.Value, sc.Max)
	}
	return fmt.Sprintf("%s %.2f", name, sc.Value)
}

func (p *Permissions) bouncerOutcomeStyle(outcome string) lipgloss.Style {
	s := p.com.Styles.Dialog.Permissions
	switch outcome {
	case "deny":
		return s.BouncerDeny
	case "escalate":
		return s.BouncerEscalate
	case "allow":
		return s.BouncerMitigate
	default:
		return s.BouncerScore
	}
}

func (p *Permissions) bouncerScoreStyle(trigger string) lipgloss.Style {
	s := p.com.Styles.Dialog.Permissions
	switch trigger {
	case permission.TriggerDeny:
		return s.BouncerDeny
	case permission.TriggerEscalate:
		return s.BouncerEscalate
	case permission.TriggerMitigate:
		return s.BouncerMitigate
	default:
		return s.BouncerScore
	}
}

// wrapStyled packs already-styled tokens into lines no wider than width,
// never splitting a token. A token wider than width is truncated.
func wrapStyled(tokens []string, width int, gap string) []string {
	var lines []string
	var cur strings.Builder
	curWidth := 0
	gapWidth := ansi.StringWidth(gap)
	for _, tok := range tokens {
		if w := ansi.StringWidth(tok); w > width {
			tok = ansi.Truncate(tok, width, "…")
		}
		w := ansi.StringWidth(tok)
		if curWidth > 0 && curWidth+gapWidth+w > width {
			lines = append(lines, cur.String())
			cur.Reset()
			curWidth = 0
		}
		if curWidth > 0 {
			cur.WriteString(gap)
			curWidth += gapWidth
		}
		cur.WriteString(tok)
		curWidth += w
	}
	if curWidth > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}
