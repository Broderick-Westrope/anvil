package skills

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

var skillContentRe = regexp.MustCompile(`(?s)<skill_content name="([^"]+)">\n(.*?)\n</skill_content>`)

type ContentXML struct {
	Name         string
	Instructions string
}

func ParseContentXML(text string) ([]ContentXML, string) {
	var content []ContentXML
	for {
		match := skillContentRe.FindStringSubmatchIndex(text)
		if match == nil || match[0] != 0 {
			return content, text
		}
		content = append(content, ContentXML{
			Name: text[match[2]:match[3]], Instructions: text[match[4]:match[5]],
		})
		text = strings.TrimPrefix(text[match[1]:], "\n\n")
	}
}

func StripContentXML(text string) ([]ContentXML, string) {
	var content []ContentXML
	for _, match := range skillContentRe.FindAllStringSubmatch(text, -1) {
		content = append(content, ContentXML{Name: match[1], Instructions: match[2]})
	}
	return content, skillContentRe.ReplaceAllString(text, "")
}

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
