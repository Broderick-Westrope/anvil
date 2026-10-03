package dialog

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/charmbracelet/x/ansi"
)

// assessorScoreGap separates score tokens on a line.
const assessorScoreGap = "  "

// renderAssessor renders the assessor's verdict as a key/value block that
// wraps to width, so every axis stays visible. Axes that crossed a routing
// threshold are highlighted by the effect they had.
func (p *Permissions) renderAssessor(width int) string {
	t := p.com.Styles
	sum := p.permission.Assessor
	if sum == nil {
		if p.permission.AssessorNote == "" {
			return ""
		}
		note := capitalizeFirst(p.permission.AssessorNote)
		return t.Dialog.Permissions.KeyText.Width(width).Render(note)
	}

	keyStr := t.Dialog.Permissions.KeyText.Render("Assessor")
	valueWidth := max(1, width-lipgloss.Width(keyStr)-1)

	head := p.assessorOutcomeStyle(sum.Outcome).Render(capitalizeFirst(sum.Outcome))
	if sum.Shadow {
		head += t.Dialog.Permissions.AssessorScore.Render(" (shadow)")
	}
	if sum.Detail != "" {
		head += t.Dialog.Permissions.AssessorScore.Render(" · " + sum.Detail)
	}
	lines := wrapStyled([]string{head}, valueWidth, " ")

	tokens := make([]string, len(sum.Scores))
	for i, sc := range sum.Scores {
		tokens[i] = p.assessorScoreStyle(sc.Trigger).Render(formatAssessorScore(sc))
	}
	lines = append(lines, wrapStyled(tokens, valueWidth, assessorScoreGap)...)

	value := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return lipgloss.JoinHorizontal(lipgloss.Top, keyStr, " ", value)
}

// formatAssessorScore renders one axis as "remote exec 0.91" or, for the
// severity scale, "severity 2.4/3".
func formatAssessorScore(sc permission.AssessorScore) string {
	name := strings.ReplaceAll(sc.Name, "_", " ")
	if sc.Max > 1 {
		return fmt.Sprintf("%s %.1f/%g", name, sc.Value, sc.Max)
	}
	return fmt.Sprintf("%s %.2f", name, sc.Value)
}

func (p *Permissions) assessorOutcomeStyle(outcome string) lipgloss.Style {
	s := p.com.Styles.Dialog.Permissions
	switch outcome {
	case "deny":
		return s.AssessorDeny
	case "escalate":
		return s.AssessorEscalate
	case "allow":
		return s.AssessorMitigate
	default:
		return s.AssessorScore
	}
}

func (p *Permissions) assessorScoreStyle(trigger string) lipgloss.Style {
	s := p.com.Styles.Dialog.Permissions
	switch trigger {
	case permission.TriggerDeny:
		return s.AssessorDeny
	case permission.TriggerEscalate:
		return s.AssessorEscalate
	case permission.TriggerMitigate:
		return s.AssessorMitigate
	default:
		return s.AssessorScore
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
