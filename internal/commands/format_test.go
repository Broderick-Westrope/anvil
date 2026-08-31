package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatExpansionXML(t *testing.T) {
	t.Parallel()

	got := FormatExpansionXML("/review fix typo", "Review the following:\nfix typo")
	want := "<command_expansion command=\"/review fix typo\">\nReview the following:\nfix typo\n</command_expansion>"
	require.Equal(t, want, got)
}

func TestFormatExpansionXML_EscapesQuotes(t *testing.T) {
	t.Parallel()

	got := FormatExpansionXML(`/review scope="auth"`, "content")
	require.Contains(t, got, `command="/review scope=&quot;auth&quot;"`)
}

func TestCollapseExpansionXML(t *testing.T) {
	t.Parallel()

	wrapped := FormatExpansionXML("/review fix typo", "Review the following:\nfix typo")
	require.Equal(t, "/review fix typo", CollapseExpansionXML(wrapped))
}

func TestCollapseExpansionXML_RoundTripsQuotes(t *testing.T) {
	t.Parallel()

	line := `/review scope="auth"`
	wrapped := FormatExpansionXML(line, "content")
	require.Equal(t, line, CollapseExpansionXML(wrapped))
}

func TestCollapseExpansionXML_RoundTripsAmpersandEntities(t *testing.T) {
	t.Parallel()

	line := `/review find &quot;this&quot; & that`
	wrapped := FormatExpansionXML(line, "content")
	require.Equal(t, line, CollapseExpansionXML(wrapped))
}

func TestCollapseExpansionXML_EmptyContent(t *testing.T) {
	t.Parallel()

	wrapped := FormatExpansionXML("/cmd", "")
	require.Equal(t, "/cmd", CollapseExpansionXML(wrapped))
}

func TestCollapseExpansionXML_MultipleBlocks(t *testing.T) {
	t.Parallel()

	text := FormatExpansionXML("/one", "first") + "\n\n" + FormatExpansionXML("/two", "second")
	require.Equal(t, "/one\n\n/two", CollapseExpansionXML(text))
}

// Documents a known limitation shared with the skill_content pattern: the
// non-greedy regex closes at the first "\n</command_expansion>" inside the
// content, leaving the real closing tag visible. Update this test if the
// matching strategy is ever made structural.
func TestCollapseExpansionXML_ContentContainsClosingTag(t *testing.T) {
	t.Parallel()

	wrapped := FormatExpansionXML("/cmd", "before\n</command_expansion>\nafter")
	require.Equal(t, "/cmd\nafter\n</command_expansion>", CollapseExpansionXML(wrapped))
}

func TestCollapseExpansionXML_PreservesSurroundingText(t *testing.T) {
	t.Parallel()

	text := "before\n\n" + FormatExpansionXML("/cmd", "expanded") + "\n\nafter"
	require.Equal(t, "before\n\n/cmd\n\nafter", CollapseExpansionXML(text))
}

func TestCollapseExpansionXML_NoBlock(t *testing.T) {
	t.Parallel()

	require.Equal(t, "plain message", CollapseExpansionXML("plain message"))
}
