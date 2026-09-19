package chat

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestViewSkillCopyStates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input, want string
	}{
		{"hook prepared", `{"skill_name":"euc-go","file_path":"/resolved/euc-go/SKILL.md"}`, "**Skill:** euc-go"},
		{"invalid JSON", `{"skill_name":"euc-go"`, ""},
		{"rejected pagination", `{"skill_name":"euc-go","limit":10,"offset":2}`, "**Skill:** euc-go"},
		{"neither selector", `{}`, ""},
		{"neither selector with pagination", `{"limit":10,"offset":2}`, ""},
		{"path pagination", `{"file_path":"main.go","limit":10,"offset":2}`, "**File:** main.go\n**Limit:** 10\n**Offset:** 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			item := &baseToolMessageItem{toolCall: message.ToolCall{Name: tools.ViewToolName, Input: tc.input}}
			want := "## View Tool Call\n\n"
			if tc.want != "" {
				want += "### Parameters:\n\n" + tc.want + "\n\n"
			}
			want += "### Status:\n\nPending..."
			require.Equal(t, want, item.formatToolForCopy())
		})
	}
}
