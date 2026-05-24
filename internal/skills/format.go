package skills

import (
	"fmt"
	"log/slog"
	"strings"
)

// FormatContentXML formats a single skill's instructions as a
// <skill_content> XML block for inclusion in a user message.
func FormatContentXML(name, instructions string) string {
	return fmt.Sprintf("<skill_content name=%q>\n%s\n</skill_content>", name, instructions)
}

// ResolveContent resolves skill names to their XML content blocks using
// the given lookup function. Unknown skills are logged and skipped. Returns
// the joined XML blocks or an empty string if none resolved.
func ResolveContent(skillNames []string, lookup func(string) *Skill) string {
	var parts []string
	for _, name := range skillNames {
		skill := lookup(name)
		if skill == nil {
			slog.Warn("Command references unknown skill", "skill", name)
			continue
		}
		parts = append(parts, FormatContentXML(skill.Name, skill.Instructions))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}
