package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/agent/branchsummary"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
)

// The branch summary evaluation set: conversations modelled on real
// rewinds where the main thread, lacking a summary, missed or got wrong
// something the branch had done. Each fixture lists what a good summary
// must say, what it must not, and optionally how long it may be.
//
// TestBranchSummaryEvalFixturesReachThePrompt runs offline and checks the
// required facts reach the summarizer's input, so a failing live run
// points at the prompt or the model rather than the fixture.
// TestBranchSummaryEval runs the real summarizer against your configured
// model; it costs tokens, so it only runs when asked:
//
//	ANVIL_BRANCH_SUMMARY_EVAL=1 go test ./internal/agent -run TestBranchSummaryEval -v

const evalFixtureDir = "testdata/branch_summary_eval"

type evalFixture struct {
	name      string
	Why       string        `json:"why"`
	Trunk     []evalMessage `json:"trunk"`
	Branch    []evalMessage `json:"branch"`
	Required  []string      `json:"required"`
	Forbidden []string      `json:"forbidden"`
	// AbsentFromInput must not reach the summarizer at all, such as
	// secrets redaction removes.
	AbsentFromInput []string `json:"absent_from_input"`
	// MaxLines bounds the summary's content lines, not counting the
	// closing snapshot note or the appended file lists. Zero is no bound.
	MaxLines int `json:"max_lines"`
}

type evalMessage struct {
	Role  string `json:"role"`
	Text  string `json:"text"`
	Calls []struct {
		ID    string         `json:"id"`
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	} `json:"calls"`
	Results []struct {
		ID       string         `json:"id"`
		Content  string         `json:"content"`
		Error    bool           `json:"error"`
		Metadata map[string]any `json:"metadata"`
	} `json:"results"`
}

func loadEvalFixtures(t *testing.T) []evalFixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(evalFixtureDir, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	slices.Sort(paths)
	fixtures := make([]evalFixture, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		var f evalFixture
		require.NoError(t, json.Unmarshal(data, &f), p)
		f.name = strings.TrimSuffix(filepath.Base(p), ".json")
		require.NotEmpty(t, f.Trunk, f.name)
		require.NotEmpty(t, f.Branch, f.name)
		require.NotEmpty(t, f.Required, f.name)
		fixtures = append(fixtures, f)
	}
	return fixtures
}

// seed stores the fixture as a session whose conversation went trunk then
// branch, rewound to the end of the trunk. It returns the session and the
// leaves the rewind went from and to.
func (f evalFixture) seed(t *testing.T, env fakeEnv) (sessionID, fromLeaf, target string) {
	t.Helper()
	sess, err := env.sessions.Create(t.Context(), f.name, t.TempDir())
	require.NoError(t, err)
	names := map[string]string{}
	parent := ""
	for i, m := range slices.Concat(f.Trunk, f.Branch) {
		params := message.CreateMessageParams{ParentMessageID: parent}
		switch m.Role {
		case "user":
			params.Role = message.User
			params.Parts = []message.ContentPart{message.TextContent{Text: m.Text}}
		case "assistant":
			params.Role = message.Assistant
			if m.Text != "" {
				params.Parts = append(params.Parts, message.TextContent{Text: m.Text})
			}
			for _, c := range m.Calls {
				input, err := json.Marshal(c.Input)
				require.NoError(t, err)
				names[c.ID] = c.Name
				params.Parts = append(params.Parts, message.ToolCall{ID: c.ID, Name: c.Name, Input: string(input), Finished: true})
			}
			params.Parts = append(params.Parts, message.Finish{Reason: message.FinishReasonEndTurn})
		case "tool":
			params.Role = message.Tool
			for _, r := range m.Results {
				meta := ""
				if r.Metadata != nil {
					b, err := json.Marshal(r.Metadata)
					require.NoError(t, err)
					meta = string(b)
				}
				params.Parts = append(params.Parts, message.ToolResult{
					ToolCallID: r.ID, Name: names[r.ID], Content: r.Content, IsError: r.Error, Metadata: meta,
				})
			}
		default:
			t.Fatalf("%s: message %d has unknown role %q", f.name, i, m.Role)
		}
		created, err := env.messages.Create(t.Context(), sess.ID, params)
		require.NoError(t, err)
		parent = created.ID
		if i == len(f.Trunk)-1 {
			target = created.ID
		}
	}
	require.NoError(t, env.sessions.MoveLeaf(t.Context(), sess.ID, target))
	return sess.ID, parent, target
}

// grade reports what a summary got wrong against the fixture's rubric.
func (f evalFixture) grade(summary string) []string {
	var problems []string
	for _, pattern := range f.Required {
		if !regexp.MustCompile("(?i)" + pattern).MatchString(summary) {
			problems = append(problems, "missing: "+pattern)
		}
	}
	for _, pattern := range f.Forbidden {
		if loc := regexp.MustCompile("(?i)" + pattern).FindString(summary); loc != "" {
			problems = append(problems, fmt.Sprintf("forbidden %s: %q", pattern, loc))
		}
	}
	if n := contentLines(summary); f.MaxLines > 0 && n > f.MaxLines {
		problems = append(problems, fmt.Sprintf("%d lines, more than %d", n, f.MaxLines))
	}
	return problems
}

// contentLines counts a stored summary's non-blank lines, leaving out the
// snapshot note the template ends with and the appended file lists.
func contentLines(summary string) int {
	body, _, _ := strings.Cut(summary, "<modified-files>")
	body, _, _ = strings.Cut(body, "<read-files>")
	n := 0
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(strings.TrimLeft(line, "_*"), "Snapshot as of") {
			n++
		}
	}
	return n
}

func TestBranchSummaryEvalFixturesReachThePrompt(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	for _, f := range loadEvalFixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			_, from, target := f.seed(t, env)
			fromPath, err := env.messages.GetBranchPath(t.Context(), from)
			require.NoError(t, err)
			targetPath, err := env.messages.GetBranchPath(t.Context(), target)
			require.NoError(t, err)
			rng := branchsummary.Abandoned(fromPath, targetPath)
			require.True(t, branchsummary.Worthwhile(rng), "a fixture must leave something to summarize")
			prompt := branchsummary.Prompt(rng, branchsummary.Ledger(rng, func(string, message.ToolCall) []message.Message { return nil }), "", 50_000)

			for _, pattern := range f.Required {
				require.Regexp(t, "(?i)"+pattern, prompt, "a required fact must be in the summarizer's input")
			}
			for _, text := range f.AbsentFromInput {
				require.NotContains(t, prompt, text, "redaction must keep this out of the summarizer's input")
			}
		})
	}
}

func TestContentLinesSkipsTheSnapshotNoteAndFileLists(t *testing.T) {
	t.Parallel()
	summary := "Looked at the API; nothing changed.\n\n_Snapshot as of the branch's last message; check live git and gh state before relying on it._\n\n<read-files>\n/a.go\n</read-files>"
	require.Equal(t, 1, contentLines(summary))
}

func TestBranchSummaryEval(t *testing.T) {
	if os.Getenv("ANVIL_BRANCH_SUMMARY_EVAL") == "" {
		t.Skip("set ANVIL_BRANCH_SUMMARY_EVAL=1 to run the live branch summary evaluation")
	}

	// Like a fresh install: no user config or plugins, but your real
	// provider credentials, so it evaluates the built-in summarizer.
	t.Setenv("ANVIL_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("ANVIL_GLOBAL_DATA", t.TempDir())
	t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")
	env := testEnv(t)
	cfg, err := config.Load(env.workingDir, t.TempDir(), false)
	require.NoError(t, err)
	perms := permission.NewPermissionService(env.workingDir, config.YoloFull, nil, nil)
	coord, err := NewCoordinator(t.Context(), cfg, env.sessions, env.messages, perms, *env.filetracker,
		nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	c := coord.(*coordinator)
	require.Contains(t, c.BranchSummarizers(), BuiltinBranchSummarizer)

	for _, f := range loadEvalFixtures(t) {
		// Sequential: t.Setenv above rules out parallel subtests.
		t.Run(f.name, func(t *testing.T) {
			sessionID, from, target := f.seed(t, env)
			stored, err := c.SummarizeBranch(t.Context(), BranchSummaryRequest{
				SessionID: sessionID, FromLeafID: from, TargetID: target, Agent: BuiltinBranchSummarizer,
			})
			require.NoError(t, err)
			summary := stored.Parts[0].(message.BranchSummaryContent).Summary
			t.Logf("%s\n\n%s", f.Why, summary)
			if problems := f.grade(summary); len(problems) > 0 {
				t.Errorf("%s:\n- %s", f.name, strings.Join(problems, "\n- "))
			}
		})
	}
}
