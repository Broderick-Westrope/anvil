package skills

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatSkillContentXML(t *testing.T) {
	t.Parallel()

	got := FormatContentXML("grilling", "Ask questions about features")
	want := "<skill_content name=\"grilling\">\nAsk questions about features\n</skill_content>"
	require.Equal(t, want, got)
}

func TestResolveSkillContent_Found(t *testing.T) {
	t.Parallel()

	lookup := func(name string) *Skill {
		if name == "grilling" {
			return &Skill{Name: "grilling", Instructions: "Ask deep questions"}
		}
		return nil
	}

	got := ResolveContent([]string{"grilling"}, lookup)
	require.Contains(t, got, `<skill_content name="grilling">`)
	require.Contains(t, got, "Ask deep questions")
}

func TestResolveSkillContent_NotFound(t *testing.T) {
	t.Parallel()

	lookup := func(_ string) *Skill { return nil }

	got := ResolveContent([]string{"missing"}, lookup)
	require.Empty(t, got)
}

func TestResolveSkillContent_Empty(t *testing.T) {
	t.Parallel()

	lookup := func(_ string) *Skill { return nil }

	got := ResolveContent(nil, lookup)
	require.Empty(t, got)
}

func TestCollapseContentXML(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		text string
		want string
	}{
		"no skill content": {
			text: "plain message",
			want: "plain message",
		},
		"only skill content": {
			text: FormatContentXML("grilling", "Ask questions"),
			want: "Using skill: grilling",
		},
		"skill content before the message": {
			text: FormatContentXML("grilling", "Ask questions") + "\n\nDesign the cache",
			want: "Using skill: grilling\n\nDesign the cache",
		},
		"several skills around the message": {
			text: FormatContentXML("a", "one") + "\n\nMiddle text\n\n" + FormatContentXML("b", "two\nlines"),
			want: "Using skill: a, b\n\nMiddle text",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, CollapseContentXML(tc.text))
		})
	}
}
