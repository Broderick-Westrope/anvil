package branchsummary_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent/branchsummary"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func msg(id string, role message.MessageRole, parts ...message.ContentPart) message.Message {
	return message.Message{ID: id, Role: role, MessageType: message.MessageTypeMessage, Parts: parts}
}

func text(s string) message.TextContent { return message.TextContent{Text: s} }

func call(id, name string, input any) message.ToolCall {
	b, _ := json.Marshal(input)
	return message.ToolCall{ID: id, Name: name, Input: string(b), Finished: true}
}

func result(id, content string, isErr bool) message.ToolResult {
	return message.ToolResult{ToolCallID: id, Content: content, IsError: isErr}
}

func ids(msgs []message.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}

func path(idList ...string) []message.Message {
	out := make([]message.Message, 0, len(idList))
	for _, id := range idList {
		out = append(out, msg(id, message.User))
	}
	return out
}

func TestAbandoned(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		from, target []message.Message
		want         []string
	}{
		"target is an ancestor": {from: path("a", "b", "c", "d"), target: path("a", "b"), want: []string{"c", "d"}},
		"target on a sibling":   {from: path("a", "b", "c"), target: path("a", "x", "y"), want: []string{"b", "c"}},
		"rewind to the root":    {from: path("a", "b"), target: nil, want: []string{"a", "b"}},
		"from is an ancestor":   {from: path("a"), target: path("a", "b"), want: []string{}},
		"same leaf":             {from: path("a", "b"), target: path("a", "b"), want: []string{}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, ids(branchsummary.Abandoned(tc.from, tc.target)))
		})
	}
}

func TestWorthwhile(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		rng  []message.Message
		want bool
	}{
		"empty":               {want: false},
		"user message only":   {rng: []message.Message{msg("u", message.User, text("hi"))}, want: false},
		"assistant reply":     {rng: []message.Message{msg("u", message.User, text("hi")), msg("a", message.Assistant, text("hello"))}, want: true},
		"assistant tool call": {rng: []message.Message{msg("a", message.Assistant, call("c", "bash", map[string]string{"command": "ls"}))}, want: true},
		"empty assistant":     {rng: []message.Message{msg("a", message.Assistant)}, want: false},
		"compaction only": {rng: []message.Message{{
			ID: "c", Role: message.Assistant, MessageType: message.MessageTypeCompaction,
			Parts: []message.ContentPart{message.CompactionContent{Summary: "s"}},
		}}, want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, branchsummary.Worthwhile(tc.rng))
		})
	}
}

func noChildren(string, message.ToolCall) []message.Message { return nil }

func TestLedger_FileOperations(t *testing.T) {
	t.Parallel()

	rng := []message.Message{
		msg("a1", message.Assistant,
			call("v1", "view", map[string]string{"file_path": "/r/read.go"}),
			call("v2", "view", map[string]string{"file_path": "/r/edited.go"}),
			call("e1", "edit", map[string]string{"file_path": "/r/edited.go"}),
			call("w1", "write", map[string]string{"file_path": "/r/new.go"}),
			call("m1", "multiedit", map[string]string{"file_path": "/r/failed.go"}),
		),
		msg("t1", message.Tool,
			result("v1", "ok", false), result("v2", "ok", false),
			result("e1", "ok", false), result("w1", "ok", false),
			result("m1", "old_string not found", true),
		),
	}

	got := branchsummary.Ledger(rng, noChildren)

	require.Equal(t, []string{"/r/edited.go", "/r/new.go"}, got.Modified)
	require.Equal(t, []string{"/r/read.go"}, got.Read)
}

func TestLedger_CommandStatus(t *testing.T) {
	t.Parallel()

	bash := func(id, cmd string) message.ToolCall {
		return call(id, "bash", map[string]string{"command": cmd, "working_dir": "/repo"})
	}
	rng := []message.Message{
		msg("a1", message.Assistant,
			bash("ok", `git commit -m "fix"`),
			bash("fail", "git push"),
			bash("deny", "gh pr merge 12"),
			bash("bg", "wtp add -b feat/x"),
			bash("none", "git rebase main"),
			bash("ignored", "go test ./..."),
		),
		msg("t1", message.Tool,
			result("ok", "[feat/x 3f2a9c1] fix", false),
			result("fail", "rejected", true),
			result("deny", "Permission denied", true),
			message.ToolResult{ToolCallID: "bg", Content: "moved to background", Metadata: `{"background":true,"shell_id":"019"}`},
			result("ignored", "ok", false),
		),
	}

	got := branchsummary.Ledger(rng, noChildren)

	require.Equal(t, []message.LedgerEvent{
		{Tool: "bash", Command: `git commit -m "fix"`, Dir: "/repo", Status: message.LedgerSucceeded, Refs: []string{"3f2a9c1"}},
		{Tool: "bash", Command: "git push", Dir: "/repo", Status: message.LedgerFailed},
		{Tool: "bash", Command: "gh pr merge 12", Dir: "/repo", Status: message.LedgerDenied},
		{Tool: "bash", Command: "wtp add -b feat/x", Dir: "/repo", Status: message.LedgerUnknown},
		{Tool: "bash", Command: "git rebase main", Dir: "/repo", Status: message.LedgerUnknown},
	}, got.Events)
}

func TestLedger_RecordsPullRequestURLs(t *testing.T) {
	t.Parallel()

	rng := []message.Message{
		msg("a1", message.Assistant, call("c", "bash", map[string]string{"command": "gh pr create --fill"})),
		msg("t1", message.Tool, result("c", "https://github.com/acme/app/pull/42\n", false)),
	}

	got := branchsummary.Ledger(rng, noChildren)

	require.Equal(t, []string{"https://github.com/acme/app/pull/42"}, got.Events[0].Refs)
}

func TestLedger_TraversesDelegatedSessions(t *testing.T) {
	t.Parallel()

	child := []message.Message{
		msg("c1", message.Assistant,
			call("ce", "edit", map[string]string{"file_path": "/r/child.go"}),
			call("cg", "bash", map[string]string{"command": "git commit -m child"}),
		),
		msg("c2", message.Tool, result("ce", "ok", false), result("cg", "[main abc1234] child", false)),
	}
	rng := []message.Message{
		msg("a1", message.Assistant,
			call("b1", "bash", map[string]string{"command": "git checkout -b feat/y"}),
			call("task1", "task", map[string]string{"prompt": "do it"}),
			call("b2", "bash", map[string]string{"command": "git push -u origin feat/y"}),
		),
		msg("t1", message.Tool, result("b1", "", false), result("task1", "done", false), result("b2", "", false)),
	}
	children := func(_ string, c message.ToolCall) []message.Message {
		if c.ID == "task1" {
			return child
		}
		return nil
	}

	got := branchsummary.Ledger(rng, children)

	require.Equal(t, []string{"/r/child.go"}, got.Modified)
	commands := make([]string, 0, len(got.Events))
	for _, e := range got.Events {
		commands = append(commands, e.Command)
	}
	require.Equal(t, []string{"git checkout -b feat/y", "git commit -m child", "git push -u origin feat/y"}, commands)
}

func TestLedger_CarriesForwardEarlierSummaries(t *testing.T) {
	t.Parallel()

	earlier := message.Message{
		ID: "s", Role: message.User, MessageType: message.MessageTypeBranchSummary,
		Parts: []message.ContentPart{message.BranchSummaryContent{Summary: "x", Ledger: message.BranchLedger{
			Modified: []string{"/r/old.go"},
			Read:     []string{"/r/new.go"},
			Events:   []message.LedgerEvent{{Tool: "bash", Command: "git commit -m old", Status: message.LedgerSucceeded}},
		}}},
	}
	rng := []message.Message{
		earlier,
		msg("a1", message.Assistant, call("w", "write", map[string]string{"file_path": "/r/new.go"})),
		msg("t1", message.Tool, result("w", "ok", false)),
	}

	got := branchsummary.Ledger(rng, noChildren)

	require.Equal(t, []string{"/r/new.go", "/r/old.go"}, got.Modified)
	require.Empty(t, got.Read)
	require.Equal(t, "git commit -m old", got.Events[0].Command)
}

func TestPrompt_SerialisesConversationAndLedger(t *testing.T) {
	t.Parallel()

	rng := []message.Message{
		msg("u1", message.User, text("fix the login bug")),
		msg("a1", message.Assistant,
			message.ReasoningContent{Thinking: "secret plan"},
			text("Fixing it."),
			call("b", "bash", map[string]string{"command": "git commit -m fix"}),
		),
		msg("t1", message.Tool, result("b", "[main abc1234] fix", false)),
	}
	ledger := message.BranchLedger{Modified: []string{"/r/login.go"}}

	got := branchsummary.Prompt(rng, ledger, "focus on the PR", 10_000)

	require.Contains(t, got, "[User]: fix the login bug")
	require.Contains(t, got, "[Assistant]: Fixing it.")
	require.Contains(t, got, `[Tool call] bash: {"command":"git commit -m fix"}`)
	require.Contains(t, got, "[Tool result] [main abc1234] fix")
	require.Contains(t, got, "/r/login.go")
	require.Contains(t, got, "focus on the PR")
	require.NotContains(t, got, "secret plan")
}

func TestPrompt_KeepsNewestMessagesWithinBudget(t *testing.T) {
	t.Parallel()

	var rng []message.Message
	for i := range 50 {
		rng = append(rng, msg(fmt.Sprint(i), message.User, text(fmt.Sprintf("message-%02d %s", i, strings.Repeat("x ", 200)))))
	}

	got := branchsummary.Prompt(rng, message.BranchLedger{}, "", 2_000)

	require.Contains(t, got, "message-49")
	require.NotContains(t, got, "message-00")
	require.Contains(t, got, "earlier messages omitted")
	require.LessOrEqual(t, len(got)/3, 2_000)
}

func TestPrompt_AlwaysIncludesEarlierSummaries(t *testing.T) {
	t.Parallel()

	rng := []message.Message{
		{
			ID: "c", Role: message.Assistant, MessageType: message.MessageTypeCompaction,
			Parts: []message.ContentPart{message.CompactionContent{Summary: "COMPACTED-HISTORY"}},
		},
	}
	for i := range 50 {
		rng = append(rng, msg(fmt.Sprint(i), message.User, text(strings.Repeat("y ", 200))))
	}

	got := branchsummary.Prompt(rng, message.BranchLedger{}, "", 2_000)

	require.Contains(t, got, "COMPACTED-HISTORY")
	require.Contains(t, got, "may predate the branch")
}

func TestPrompt_IsStable(t *testing.T) {
	t.Parallel()

	rng := []message.Message{
		msg("a1", message.Assistant, call("c", "bash", map[string]any{"command": "ls", "working_dir": "/r", "description": "list"})),
	}
	first := branchsummary.Prompt(rng, message.BranchLedger{}, "", 10_000)
	for range 20 {
		require.Equal(t, first, branchsummary.Prompt(rng, message.BranchLedger{}, "", 10_000))
	}
}

func TestPrompt_RedactsSecrets(t *testing.T) {
	t.Parallel()

	secret := "ghp_" + strings.Repeat("a", 36)
	rng := []message.Message{
		msg("u", message.User, text("token is "+secret)),
		msg("a1", message.Assistant, call("c", "bash", map[string]string{"command": "psql postgresql://u:hunter2@db/x"})),
		msg("t1", message.Tool, result("c", "API_KEY="+strings.Repeat("k", 10), false)),
	}

	got := branchsummary.Prompt(rng, message.BranchLedger{Events: []message.LedgerEvent{{Command: "export TOKEN=abcdefgh"}}}, "", 10_000)

	require.NotContains(t, got, secret)
	require.NotContains(t, got, "hunter2")
	require.NotContains(t, got, strings.Repeat("k", 10))
	require.NotContains(t, got, "abcdefgh")
}

func TestFinalize_AppendsCappedFileLists(t *testing.T) {
	t.Parallel()

	var modified []string
	for i := range 60 {
		modified = append(modified, fmt.Sprintf("/r/f%02d.go", i))
	}

	got := branchsummary.Finalize("## Outcome\nDone.", message.BranchLedger{Modified: modified, Read: []string{"/r/read.go"}})

	require.True(t, strings.HasPrefix(got, "## Outcome\nDone."))
	require.Contains(t, got, "<modified-files>\n/r/f00.go\n")
	require.Contains(t, got, "/r/f49.go\n+10 more\n</modified-files>")
	require.NotContains(t, got, "/r/f50.go")
	require.Contains(t, got, "<read-files>\n/r/read.go\n</read-files>")
}

func TestFinalize_CapsAndRedactsText(t *testing.T) {
	t.Parallel()

	secret := "ghp_" + strings.Repeat("b", 36)
	got := branchsummary.Finalize(secret+" "+strings.Repeat("word ", 10_000), message.BranchLedger{})

	require.NotContains(t, got, secret)
	require.Less(t, len(got), 20_000)
	require.Contains(t, got, "[summary truncated]")
}

func TestLedger_UnderstandsGitDirectoryOption(t *testing.T) {
	t.Parallel()

	rng := []message.Message{
		msg("a1", message.Assistant,
			call("c", "bash", map[string]string{"command": `git -C "/wt/feat x" commit -m fix`, "working_dir": "/repo"}),
			call("p", "bash", map[string]string{"command": "git -C /wt/feat push -u origin feat"}),
			call("l", "bash", map[string]string{"command": "git -C /wt/feat log -5"}),
		),
		msg("t1", message.Tool, result("c", "[feat 9ab12cd] fix", false), result("p", "", false), result("l", "", false)),
	}

	got := branchsummary.Ledger(rng, noChildren)

	require.Len(t, got.Events, 2)
	require.Equal(t, "/wt/feat x", got.Events[0].Dir)
	require.Equal(t, "/wt/feat", got.Events[1].Dir)
}

func TestPrompt_BoundsTheLedger(t *testing.T) {
	t.Parallel()

	huge := message.BranchLedger{Events: []message.LedgerEvent{{Command: "git commit -m " + strings.Repeat("w ", 20_000), Status: message.LedgerSucceeded}}}

	got := branchsummary.Prompt(nil, huge, "", 2_000)

	require.LessOrEqual(t, len(got)/4, 2_000)
}

func TestFinalize_CapsTheWholeSummary(t *testing.T) {
	t.Parallel()

	var modified []string
	for i := range 50 {
		modified = append(modified, "/"+strings.Repeat("d", 1_000)+fmt.Sprint(i))
	}

	got := branchsummary.Finalize(strings.Repeat("word ", 10_000), message.BranchLedger{Modified: modified})

	require.LessOrEqual(t, len(got), 20_000)
}

func TestLedger_ReadsBashOutcomes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		result message.ToolResult
		want   message.LedgerStatus
	}{
		"exit zero": {
			result: message.ToolResult{Content: "ok", Metadata: `{"output":"ok"}`},
			want:   message.LedgerSucceeded,
		},
		"non-zero exit": {
			result: message.ToolResult{Content: "rejected\nExit code 1", Metadata: `{"exit_code":1}`},
			want:   message.LedgerFailed,
		},
		"interrupted": {
			result: message.ToolResult{Content: "Command was aborted before completion", Metadata: `{"interrupted":true}`},
			want:   message.LedgerFailed,
		},
		"non-zero exit before exit codes were recorded": {
			result: message.ToolResult{Content: "out\n\nexit status 3\nExit code 3\n\n<cwd>/r</cwd>", Metadata: `{"output":"out"}`},
			want:   message.LedgerFailed,
		},
		"still running as a job": {
			result: message.ToolResult{Content: "moved to background", Metadata: `{"background":true,"shell_id":"019"}`},
			want:   message.LedgerUnknown,
		},
		"background command that finished at once": {
			result: message.ToolResult{Content: "done", Metadata: `{"background":true}`},
			want:   message.LedgerSucceeded,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tc.result.ToolCallID = "c"
			rng := []message.Message{
				msg("a1", message.Assistant, call("c", "bash", map[string]string{"command": "git push"})),
				msg("t1", message.Tool, tc.result),
			}

			got := branchsummary.Ledger(rng, noChildren)

			require.Equal(t, tc.want, got.Events[0].Status)
		})
	}
}

func TestRedaction_KeepsLedgerCommitSHAs(t *testing.T) {
	t.Parallel()

	sha := "3f2a9c1e5b7d4a6f8e0c2b4d6a8f0e2c4b6d8a0f"
	other := "9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d"
	ledger := message.BranchLedger{Events: []message.LedgerEvent{{Command: "git commit -m x", Status: message.LedgerSucceeded, Refs: []string{sha}}}}
	rng := []message.Message{msg("a1", message.Assistant, text("Committed "+sha+"; unrelated "+other))}

	prompt := branchsummary.Prompt(rng, ledger, "", 10_000)
	stored := branchsummary.Finalize("Commit "+sha+" is pushed. Token "+other, ledger)

	require.Equal(t, 2, strings.Count(prompt, sha), "in the ledger and the conversation")
	require.NotContains(t, prompt, other)
	require.Contains(t, stored, sha)
	require.NotContains(t, stored, other)
}

func TestLedger_TakesSHAsOnlyFromGitRecords(t *testing.T) {
	t.Parallel()

	token := "0123456789abcdef0123456789abcdef01234567"
	rng := []message.Message{
		msg("a1", message.Assistant, call("c", "bash", map[string]string{"command": "git commit -m fix && cat token.txt"})),
		msg("t1", message.Tool, result("c", "[main 1a2b3c4] fix\n 1 file changed\n"+token, false)),
	}

	ledger := branchsummary.Ledger(rng, noChildren)
	prompt := branchsummary.Prompt(rng, ledger, "", 10_000)

	require.Equal(t, []string{"1a2b3c4"}, ledger.Events[0].Refs)
	require.NotContains(t, prompt, token)
}

func TestLedger_ReadsPushAndResetSHAs(t *testing.T) {
	t.Parallel()

	rng := []message.Message{
		msg("a1", message.Assistant,
			call("p", "bash", map[string]string{"command": "git push"}),
			call("r", "bash", map[string]string{"command": "git reset --hard origin/main"}),
		),
		msg("t1", message.Tool,
			result("p", "To github.com:acme/app.git\n   1a2b3c4..5d6e7f8  main -> main", false),
			result("r", "HEAD is now at 9a8b7c6 fix", false),
		),
	}

	got := branchsummary.Ledger(rng, noChildren)

	require.Equal(t, []string{"1a2b3c4", "5d6e7f8"}, got.Events[0].Refs)
	require.Equal(t, []string{"9a8b7c6"}, got.Events[1].Refs)
}

func TestPrompt_RedactsQuotedCredentialsInToolInput(t *testing.T) {
	t.Parallel()

	rng := []message.Message{
		msg("a1", message.Assistant, call("c", "bash", map[string]string{"command": `TOKEN="short-cred" git push`})),
		msg("t1", message.Tool, result("c", "", false)),
	}
	ledger := branchsummary.Ledger(rng, noChildren)

	prompt := branchsummary.Prompt(rng, ledger, "", 10_000)
	stored := branchsummary.Finalize("done", ledger)

	require.NotContains(t, prompt, "short-cred")
	require.NotContains(t, stored, "short-cred")
	require.NotContains(t, ledger.Events[0].Command, "short-cred", "the ledger is stored")
}

func TestPrompt_KeepsTheNewestCommandsWhenThereAreTooMany(t *testing.T) {
	t.Parallel()

	var events []message.LedgerEvent
	for i := range 60 {
		events = append(events, message.LedgerEvent{Command: fmt.Sprintf("git commit -m step-%02d", i), Status: message.LedgerSucceeded})
	}
	events = append(events, message.LedgerEvent{Command: "git merge feat", Status: message.LedgerSucceeded})

	got := branchsummary.Prompt(nil, message.BranchLedger{Events: events}, "", 100_000)

	require.Contains(t, got, "git merge feat")
	require.NotContains(t, got, "step-00")
	require.Contains(t, got, "11 earlier commands omitted")
	require.Less(t, strings.Index(got, "step-50"), strings.Index(got, "git merge feat"))
}

func TestPrompt_RedactsBeforeTruncating(t *testing.T) {
	t.Parallel()

	output := strings.Repeat("x ", 992) + "x" + `TOKEN="hunter2hunter2"`
	rng := []message.Message{
		msg("a1", message.Assistant, call("c", "bash", map[string]string{"command": "env"})),
		msg("t1", message.Tool, result("c", output, false)),
	}

	got := branchsummary.Prompt(rng, message.BranchLedger{}, "", 10_000)

	require.NotContains(t, got, "hunter2")
}

func TestPrompt_AnOversizedNewestMessageDoesNotHideTheRest(t *testing.T) {
	t.Parallel()

	rng := []message.Message{
		msg("u1", message.User, text("EARLIER-CONTEXT")),
		msg("a1", message.Assistant, text("FINAL-REPORT "+strings.Repeat("word ", 20_000))),
	}

	got := branchsummary.Prompt(rng, message.BranchLedger{}, "", 6_000)

	require.Contains(t, got, "FINAL-REPORT")
	require.Contains(t, got, "EARLIER-CONTEXT")
	require.LessOrEqual(t, len(got)/3, 6_000)
}

func TestLedger_CompoundCommandOutcomeIsUnknown(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		command string
		result  message.ToolResult
		want    message.LedgerStatus
	}{
		"failed chain": {
			command: "git commit -m x && git push",
			result:  message.ToolResult{Content: "[main 1a2b3c4] x\nrejected\nExit code 1", Metadata: `{"exit_code":1}`},
			want:    message.LedgerUnknown,
		},
		"status hidden by a later command": {
			command: "git push; true",
			result:  message.ToolResult{Content: "rejected", Metadata: `{}`},
			want:    message.LedgerUnknown,
		},
		"whole chain succeeded": {
			command: "git add . && git commit -m x",
			result:  message.ToolResult{Content: "[main 1a2b3c4] x", Metadata: `{}`},
			want:    message.LedgerSucceeded,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tc.result.ToolCallID = "c"
			rng := []message.Message{
				msg("a1", message.Assistant, call("c", "bash", map[string]string{"command": tc.command})),
				msg("t1", message.Tool, tc.result),
			}

			got := branchsummary.Ledger(rng, noChildren)

			require.Equal(t, tc.want, got.Events[0].Status)
		})
	}
}
