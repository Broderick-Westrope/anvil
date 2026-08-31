package commands

import (
	"fmt"
	"regexp"
	"strings"
)

// FormatExpansionXML wraps expanded command content in a
// <command_expansion> XML block that records the command line the user
// invoked (e.g. "/review fix typo"). The full content is still sent to
// the LLM; the chat UI displays only the command line.
func FormatExpansionXML(commandLine, content string) string {
	escaped := strings.ReplaceAll(commandLine, "&", "&amp;")
	escaped = strings.ReplaceAll(escaped, `"`, "&quot;")
	return fmt.Sprintf("<command_expansion command=\"%s\">\n%s\n</command_expansion>", escaped, content)
}

// expansionRe matches <command_expansion command="...">...</command_expansion>
// blocks and captures the command line. Like the skill_content pattern it
// mirrors, the non-greedy body match closes at the first
// "\n</command_expansion>" — content containing that literal sequence
// collapses incorrectly. See TestCollapseExpansionXML_ContentContainsClosingTag.
var expansionRe = regexp.MustCompile(`(?s)<command_expansion command="([^"]*)">\n.*?\n</command_expansion>`)

// CollapseExpansionXML replaces <command_expansion> XML blocks with the
// compact command line the user invoked (e.g. "/review fix typo"). Used
// for display in the chat thread and prompt history; the full expansion
// is still sent to the LLM.
func CollapseExpansionXML(text string) string {
	return expansionRe.ReplaceAllStringFunc(text, func(match string) string {
		line := expansionRe.FindStringSubmatch(match)[1]
		line = strings.ReplaceAll(line, "&quot;", `"`)
		return strings.ReplaceAll(line, "&amp;", "&")
	})
}
