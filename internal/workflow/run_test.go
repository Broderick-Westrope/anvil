package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/workflow"
)

func TestMainReceivesRawInputAndReturnsValue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	got, err := h.run(t, `function main(input) { return {echo: input.raw} }`, "plans/x.md")
	require.NoError(t, err)
	require.Equal(t, map[string]any{"echo": "plans/x.md"}, got)
}

func TestBashReturnsOutput(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	got, err := h.run(t, `function main() { return bash("echo hi", {cwd: "/wt"}).stdout }`, "")
	require.NoError(t, err)
	require.Equal(t, "ran echo hi", got)
}

func TestBashNonZeroExitThrowsBashError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.bashFn = func(context.Context, workflow.BashCall) (workflow.BashResult, error) {
		return workflow.BashResult{Stdout: "out", Stderr: "bad", ExitCode: 2}, nil
	}
	got, err := h.run(t, `function main() {
  try { bash("false") } catch (e) {
    return {isBash: e instanceof BashError, code: e.exitCode, stderr: e.stderr}
  }
}`, "")
	require.NoError(t, err)
	require.Equal(t, map[string]any{"isBash": true, "code": float64(2), "stderr": "bad"}, got)
}

func TestBashCheckFalseReturnsFailure(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.bashFn = func(context.Context, workflow.BashCall) (workflow.BashResult, error) {
		return workflow.BashResult{ExitCode: 1}, nil
	}
	got, err := h.run(t, `function main() { return bash("false", {check: false}).exitCode }`, "")
	require.NoError(t, err)
	require.Equal(t, float64(1), got)
}

func TestRunExecutesOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := h.newRun(t, `function main() { bash("once") }`)
	_, err := run.Execute(t.Context(), workflow.Input{})
	require.NoError(t, err)
	_, err = run.Execute(t.Context(), workflow.Input{})
	require.ErrorContains(t, err, "already executed")
	require.Equal(t, []string{"once"}, h.backend.bashCmds())
}

func TestScriptsSeeOnlyErrorClassesTheRuntimeThrows(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	got, err := h.run(t, `function main() {
  return [typeof BashError, typeof SchemaError, typeof Cancelled, typeof JobsRunning, typeof Interrupted]
}`, "")
	require.NoError(t, err)
	require.Equal(t, []any{"function", "function", "function", "function", "undefined"}, got)
}

func TestMainReturningNothingHasNoResult(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"undefined": `function main() {}`,
		"null":      `function main() { return null }`,
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, err := newHarness(t).newRun(t, src).Execute(t.Context(), workflow.Input{})
			require.NoError(t, err)
			require.Nil(t, out)
		})
	}
}

func TestUncaughtErrorFailsRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.run(t, `function main() { throw new Error("nope") }`, "")
	require.ErrorContains(t, err, "nope")
}

func TestPrimitiveAtTopLevelThrows(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.run(t, `bash("rm -rf /"); function main() {}`, "")
	require.ErrorContains(t, err, "top level")
	require.Empty(t, h.recorder.events)
}

func TestRequiresIsANoOpAtRuntime(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	got, err := h.run(t, `requires({agents: ["reviewer"]}); function main() { return 1 }`, "")
	require.NoError(t, err)
	require.Equal(t, float64(1), got)
}

func TestSessionTurnReturnsItsResult(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.turnFn = func(_ context.Context, call workflow.TurnCall) (string, error) {
		return "reply to " + call.Prompt, nil
	}
	got, err := h.run(t, `function main() { return session.new().turn("build it") }`, "")
	require.NoError(t, err)
	require.Equal(t, "reply to build it", got)
	require.Equal(t, "new-1", h.backend.turns[0].SessionID)
}

func TestTurnSkillsArePassedAndUndeclaredWarns(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.run(t, `function main() { session.new().turn("x", {skills: ["go-style"]}) }`, "")
	require.NoError(t, err)
	require.Equal(t, []string{"go-style"}, h.backend.turns[0].Skills)
	require.Equal(t, []string{"undeclared: add skill \"go-style\" to requires()"}, h.backend.warnings)
}

func TestTurnWithMissingSkillThrows(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.run(t, `function main() { session.new().turn("x", {skills: ["nope"]}) }`, "")
	require.ErrorContains(t, err, `skill "nope" not found`)
	require.Empty(t, h.backend.turns)
}

func TestTurnWithSchemaReturnsValidatedObject(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.turnFn = func(_ context.Context, call workflow.TurnCall) (string, error) {
		require.True(t, strings.HasPrefix(call.Prompt, "judge\n\nEnd your reply with a single ```json"), call.Prompt)
		require.Contains(t, call.Prompt, `"kind"`)
		return "Here you go:\n```json\n{\"kind\": \"clean\"}\n```", nil
	}
	got, err := h.run(t, `const S = {type: "object", properties: {kind: {type: "string"}}, required: ["kind"]}
function main() { return session.new().turn("judge", {schema: S}).kind }`, "")
	require.NoError(t, err)
	require.Equal(t, "clean", got)
}

func TestTurnWithSchemaReadsTheBlockThatEndsTheReply(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		reply string
		want  string
	}{
		"fence inside a string": {
			reply: "Draft:\n```json\n{\"code\": \"```go\\nx := 1\\n```\"}\n```",
			want:  "```go\nx := 1\n```",
		},
		"json fence inside a string": {
			reply: "```json\n{\"code\": \"see ```json\\n{}\\n```\"}\n```\n",
			want:  "see ```json\n{}\n```",
		},
		"earlier example block": {
			reply: "Example:\n```json\n{\"code\": \"old\"}\n```\nFinal:\n```json\n{\"code\": \"new\"}\n```",
			want:  "new",
		},
		"text after the blocks": {
			reply: "```json\n{\"code\": \"old\"}\n```\n```json\n{\"code\": \"new\"}\n```\nDone.",
			want:  "new",
		},
		"plain fence after the block": {
			reply: "```json\n{\"code\": \"new\"}\n```\nRun:\n```\ngo test\n```",
			want:  "new",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			h.backend.turnFn = func(context.Context, workflow.TurnCall) (string, error) { return tc.reply, nil }
			got, err := h.run(t, `const S = {type: "object", properties: {code: {type: "string"}}, required: ["code"]}
function main() { return session.new().turn("x", {schema: S}).code }`, "")
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.Len(t, h.backend.turns, 1, "no corrective turn")
		})
	}
}

func TestTurnWithSchemaRetriesOnceVisibly(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	replies := []string{"no json here", "```json\n{\"kind\": \"changes\"}\n```"}
	h.backend.turnFn = func(context.Context, workflow.TurnCall) (string, error) {
		reply := replies[0]
		replies = replies[1:]
		return reply, nil
	}
	got, err := h.run(t, `const S = {type: "object", required: ["kind"]}
function main() { return session.new().turn("judge", {schema: S}).kind }`, "")
	require.NoError(t, err)
	require.Equal(t, "changes", got)
	require.Len(t, h.backend.turns, 2)
	require.Equal(t, "new-1", h.backend.turns[1].SessionID)
}

func TestTurnWithSchemaThrowsSchemaErrorAfterRetry(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.turnFn = func(context.Context, workflow.TurnCall) (string, error) {
		return "```json\n{\"other\": 1}\n```", nil
	}
	got, err := h.run(t, `const S = {type: "object", required: ["kind"]}
function main() {
  try { session.new().turn("judge", {schema: S}) }
  catch (e) { return {schema: e instanceof SchemaError, raw: e.raw} }
}`, "")
	require.NoError(t, err)
	require.Equal(t, map[string]any{"schema": true, "raw": "```json\n{\"other\": 1}\n```"}, got)
	require.Len(t, h.backend.turns, 2)
}

func TestCancelledTurnThrowsCancelled(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.turnFn = func(context.Context, workflow.TurnCall) (string, error) {
		return "", context.Canceled
	}
	got, err := h.run(t, `function main() {
  try { session.new().turn("x") } catch (e) { return e instanceof Cancelled }
}`, "")
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestNewSessionSeedsOnlyItsFirstTurn(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.run(t, `requires({skills: ["go-style"]})
function main() {
  const s = session.new({seed: "SEED", skills: ["go-style"]})
  s.turn("first")
  s.turn("second")
  return s.id
}`, "")
	require.NoError(t, err)
	require.Len(t, h.backend.turns, 2)
	require.Equal(t, "new-1", h.backend.turns[0].SessionID)
	require.Equal(t, "SEED\n\nfirst", h.backend.turns[0].Prompt)
	require.Equal(t, []string{"go-style"}, h.backend.turns[0].Skills)
	require.Equal(t, "second", h.backend.turns[1].Prompt)
	require.Empty(t, h.backend.turns[1].Skills)
}

func TestNewSessionKeepsItsSeedUntilATurnSucceeds(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	var calls atomic.Int32
	h.backend.turnFn = func(context.Context, workflow.TurnCall) (string, error) {
		if calls.Add(1) == 1 {
			return "", errBoom
		}
		return "ok", nil
	}
	_, err := h.run(t, `function main() {
  const s = session.new({seed: "SEED"})
  try { s.turn("first") } catch (e) {}
  s.turn("retry")
}`, "")
	require.NoError(t, err)
	require.Len(t, h.backend.turns, 2)
	require.Equal(t, "SEED\n\nretry", h.backend.turns[1].Prompt)
}

func TestNewSessionSeedsOnlyWhenGivenASeedOrSkills(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		opts       string
		wantPrompt string
		wantSkills []string
	}{
		"nothing":     {opts: `{}`, wantPrompt: "first"},
		"skills only": {opts: `{skills: ["go-style"]}`, wantPrompt: "\n\nfirst", wantSkills: []string{"go-style"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			_, err := h.run(t, `requires({skills: ["go-style"]})
function main() { session.new(`+tc.opts+`).turn("first") }`, "")
			require.NoError(t, err)
			require.Len(t, h.backend.turns, 1)
			require.Equal(t, tc.wantPrompt, h.backend.turns[0].Prompt)
			require.Equal(t, tc.wantSkills, h.backend.turns[0].Skills)
		})
	}
}

func TestAgentReturnsTextOrSchemaObject(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.agentFn = func(_ context.Context, call workflow.AgentCall) (workflow.AgentResult, error) {
		if call.Schema != nil {
			return workflow.AgentResult{Output: []byte(`{"kind":"clean"}`)}, nil
		}
		return workflow.AgentResult{Text: "plain"}, nil
	}
	got, err := h.run(t, `requires({agents: ["reviewer"]})
function main() {
  return [agent("reviewer", "look"), agent("reviewer", "look", {schema: {type: "object"}}).kind]
}`, "")
	require.NoError(t, err)
	require.Equal(t, []any{"plain", "clean"}, got)
}

func TestUnencodableResultIsRecordedAsFailed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.agentFn = func(context.Context, workflow.AgentCall) (workflow.AgentResult, error) {
		return workflow.AgentResult{Output: []byte(`not json`)}, nil
	}
	got, err := h.run(t, `requires({agents: ["reviewer"]})
function main() {
  try { agent("reviewer", "x", {schema: {type: "object"}}) } catch (e) { return e.message }
}`, "")
	require.NoError(t, err)
	require.Contains(t, got, "encode agent result")
	require.Equal(t, []string{"agent"}, h.recorder.primitives(workflow.PhaseFailed))
}

func TestAgentMissingThrowsAndUndeclaredWarns(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.run(t, `function main() { agent("reviewer", "x"); agent("ghost", "x") }`, "")
	require.ErrorContains(t, err, `agent "ghost" not found`)
	require.Equal(t, []string{`undeclared: add agent "reviewer" to requires()`}, h.backend.warnings)
}

func TestAgentSchemaErrorIsThrown(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.agentFn = func(context.Context, workflow.AgentCall) (workflow.AgentResult, error) {
		return workflow.AgentResult{}, &workflow.SchemaError{Raw: "prose", Reason: "submit_result never called"}
	}
	got, err := h.run(t, `requires({agents: ["reviewer"]})
function main() {
  try { agent("reviewer", "x", {schema: {}}) } catch (e) { return [e instanceof SchemaError, e.raw] }
}`, "")
	require.NoError(t, err)
	require.Equal(t, []any{true, "prose"}, got)
}

func TestParallelSettlesInOrderConcurrently(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	var inFlight atomic.Int32
	both := make(chan struct{})
	h.backend.agentFn = func(_ context.Context, call workflow.AgentCall) (workflow.AgentResult, error) {
		if inFlight.Add(1) == 2 {
			close(both)
		}
		select {
		case <-both:
		case <-time.After(5 * time.Second):
			return workflow.AgentResult{}, errors.New("not concurrent")
		}
		if call.Prompt == "fail" {
			return workflow.AgentResult{}, errBoom
		}
		return workflow.AgentResult{Text: call.Agent}, nil
	}
	got, err := h.run(t, `requires({agents: ["reviewer", "devils-advocate"]})
function main() {
  const rs = parallel([{agent: "reviewer", prompt: "ok"}, {agent: "devils-advocate", prompt: "fail"}])
  return [rs[0].ok, rs[0].value, rs[1].ok, rs[1].error.message]
}`, "")
	require.NoError(t, err)
	require.Equal(t, []any{true, "reviewer", false, "boom"}, got)
}

func TestParallelRecordsChildEvents(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.run(t, `requires({agents: ["reviewer"]})
function main() { parallel([{agent: "reviewer", prompt: "a"}, {agent: "reviewer", prompt: "b"}]) }`, "")
	require.NoError(t, err)
	var parentSeq int
	children := map[int]bool{}
	for _, ev := range h.recorder.events {
		if ev.Primitive == "parallel" {
			parentSeq = ev.Seq
		}
	}
	for _, ev := range h.recorder.events {
		if ev.Parent == parentSeq && ev.Phase == workflow.PhaseCompleted && ev.Primitive == "agent" {
			children[ev.Index] = true
		}
	}
	require.Equal(t, map[int]bool{0: true, 1: true}, children)
}

func TestEventsLogStartedThenCompletedPerPrimitive(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.bashFn = func(context.Context, workflow.BashCall) (workflow.BashResult, error) {
		return workflow.BashResult{ExitCode: 3}, nil
	}
	_, err := h.run(t, `function main() { status("build", "1/3"); try { bash("x") } catch (e) {} }`, "")
	require.NoError(t, err)
	require.Equal(t, []string{"status", "bash"}, h.recorder.primitives(workflow.PhaseStarted))
	require.Equal(t, []string{"status"}, h.recorder.primitives(workflow.PhaseCompleted))
	require.Equal(t, []string{"bash"}, h.recorder.primitives(workflow.PhaseFailed))
	first := h.recorder.events[0]
	require.Equal(t, 1, first.Seq)
	require.JSONEq(t, `{"step":"build","detail":"1/3"}`, string(first.Args))
	require.Len(t, first.ArgsHash, 64)
}

func TestAskReturnsAnswer(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.askFn = func(_ context.Context, q string, options []string) (string, error) {
		return q + ":" + strings.Join(options, ","), nil
	}
	got, err := h.run(t, `function main() { return ask("go?", {options: ["a", "b"]}) }`, "")
	require.NoError(t, err)
	require.Equal(t, "go?:a,b", got)
}

func TestReferenceServesTheApprovedFiles(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.references = map[string]string{filepath.Join("references", "da.md"): "DA PROMPT"}
	writeFile(t, h.dir, "references/da.md", "EDITED SINCE APPROVAL")
	writeFile(t, h.dir, "unapproved.md", "NOT IN THE BUNDLE")
	got, err := h.run(t, `requires({references: ["references/da.md"]})
function main() { return reference("references/da.md") }`, "")
	require.NoError(t, err)
	require.Equal(t, "DA PROMPT", got)

	_, err = h.run(t, `function main() { return reference("unapproved.md") }`, "")
	require.ErrorContains(t, err, `reference "unapproved.md" not found`)

	_, err = h.run(t, `function main() { return reference("../secret") }`, "")
	require.ErrorContains(t, err, "outside the workflow directory")
}

func TestReadFileThrowsOnMissingFile(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	writeFile(t, h.dir, "note.md", "NOTE")
	got, err := h.run(t, `function main(input) { return readFile(input.raw) }`, h.dir+"/note.md")
	require.NoError(t, err)
	require.Equal(t, "NOTE", got)

	_, err = h.run(t, `function main(input) { return readFile(input.raw) }`, h.dir+"/missing.md")
	require.Error(t, err)
}

func TestSkillReturnsBodyAndLocation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	got, err := h.run(t, `requires({skills: ["go-style"]})
function main() { return skill("go-style") }`, "")
	require.NoError(t, err)
	require.Equal(t, map[string]any{"body": "body of go-style", "location": "/skills/go-style"}, got)
}

func TestTmpdirIsCreated(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	got, err := h.run(t, `function main() { return workflow.tmpdir() }`, "")
	require.NoError(t, err)
	require.DirExists(t, got.(string))
}

func TestExclusiveHoldsRunSessionsAndReleasesOnThrow(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.run(t, `function main() {
  session.new({})
  try { workflow.exclusive(() => { throw new Error("x") }) } catch (e) {}
  return workflow.exclusive(() => 5)
}`, "")
	require.NoError(t, err)
	require.Equal(t, [][]string{{"new-1"}, {"new-1"}}, h.backend.holds, "the invoking session is the user's, so it's never held")
	require.Equal(t, 2, h.backend.released)
}

func TestNowAndRandomAreRecorded(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	got, err := h.run(t, `function main() {
  const r = Math.random()
  return [typeof now(), r >= 0 && r < 1, typeof Date.now()]
}`, "")
	require.NoError(t, err)
	require.Equal(t, []any{"number", true, "number"}, got)
	require.Equal(t, []string{"random", "now", "now"}, h.recorder.primitives(workflow.PhaseStarted))
}

func TestNewDateWithoutArgumentsThrows(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.run(t, `function main() { return new Date() }`, "")
	require.ErrorContains(t, err, "now()")
}

func TestPauseHoldsNextPrimitiveUntilResume(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := h.newRun(t, `function main() { bash("one"); bash("two") }`)
	ran := make(chan string, 2)
	release := make(chan struct{})
	h.backend.bashFn = func(_ context.Context, call workflow.BashCall) (workflow.BashResult, error) {
		ran <- call.Cmd
		if call.Cmd == "one" {
			<-release
		}
		return workflow.BashResult{}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := run.Execute(t.Context(), workflow.Input{})
		done <- err
	}()
	require.Equal(t, "one", <-ran)
	run.Pause()
	close(release)
	select {
	case cmd := <-ran:
		t.Fatalf("%s ran while paused", cmd)
	case <-time.After(100 * time.Millisecond):
	}
	run.Resume()
	require.Equal(t, "two", <-ran)
	require.NoError(t, <-done)
}

func TestPauseInsideExclusiveStillReleasesTheHold(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := h.newRun(t, `function main() { workflow.exclusive(() => bash("one")); bash("two") }`)
	ran := make(chan string, 2)
	release := make(chan struct{})
	h.backend.bashFn = func(_ context.Context, call workflow.BashCall) (workflow.BashResult, error) {
		ran <- call.Cmd
		if call.Cmd == "one" {
			<-release
		}
		return workflow.BashResult{}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := run.Execute(t.Context(), workflow.Input{})
		done <- err
	}()
	require.Equal(t, "one", <-ran)
	run.Pause()
	close(release)
	require.Eventually(t, func() bool { return h.backend.releases() == 1 }, 5*time.Second, time.Millisecond,
		"a paused run kept its sessions held")
	require.Contains(t, h.recorder.primitives(workflow.PhaseCompleted), "workflow.release")
	require.Empty(t, h.backend.bashCmds()[1:], "bash ran while paused")
	run.Resume()
	require.Equal(t, "two", <-ran)
	require.NoError(t, <-done)
}

func TestAbortCancelsInFlightWork(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := h.newRun(t, `function main() { try { bash("slow") } catch (e) {} ; bash("after") }`)
	started := make(chan struct{})
	h.backend.bashFn = func(ctx context.Context, call workflow.BashCall) (workflow.BashResult, error) {
		if call.Cmd == "after" {
			t.Error("ran after abort")
		}
		close(started)
		<-ctx.Done()
		return workflow.BashResult{}, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := run.Execute(t.Context(), workflow.Input{})
		done <- err
	}()
	<-started
	run.Abort()
	require.ErrorIs(t, <-done, workflow.ErrAborted)
}

func TestAbortBeforeExecuteRunsNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := h.newRun(t, `function main() { bash("never") }`)
	run.Abort()
	_, err := run.Execute(t.Context(), workflow.Input{})
	require.ErrorIs(t, err, workflow.ErrAborted)
	require.Empty(t, h.backend.bashCmds())
}

func TestAbortInterruptsPureJSLoop(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := h.newRun(t, `function main() { status("spin", ""); while (true) {} }`)
	done := make(chan error, 1)
	go func() {
		_, err := run.Execute(t.Context(), workflow.Input{})
		done <- err
	}()
	require.Eventually(t, func() bool { return len(h.recorder.primitives(workflow.PhaseCompleted)) == 1 },
		5*time.Second, 10*time.Millisecond)
	run.Abort()
	select {
	case err := <-done:
		require.ErrorIs(t, err, workflow.ErrAborted)
	case <-time.After(5 * time.Second):
		t.Fatal("abort did not interrupt the loop")
	}
}

func TestTurnInsideExclusiveThrows(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.run(t, `function main() { workflow.exclusive(() => session.new().turn("x")) }`, "")
	require.ErrorContains(t, err, "exclusive")
	require.Empty(t, h.backend.turns)
	require.Equal(t, 1, h.backend.released)
}

func TestWorkflowDirIsTheScriptDirectory(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	got, err := h.run(t, `function main() { return workflow.dir() }`, "")
	require.NoError(t, err)
	require.Equal(t, h.dir, got)
}

func TestExclusiveRefusesWhileBackgroundJobsRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.holdErr = &workflow.JobsRunningError{Jobs: []workflow.Job{{ID: "J1", Command: "npm run dev", SessionID: "invoker"}}}
	got, err := h.run(t, `function main() {
  try { workflow.exclusive(() => bash("rm -rf wt")) }
  catch (e) { return [e instanceof JobsRunning, e.jobs[0].id, e.jobs[0].command] }
}`, "")
	require.NoError(t, err)
	require.Equal(t, []any{true, "J1", "npm run dev"}, got)
	require.Empty(t, h.backend.bashCmds())
}

func TestExclusiveCanAskToStopJobs(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.run(t, `function main() { workflow.exclusive(() => 1, {stopJobs: true}) }`, "")
	require.NoError(t, err)
	require.Equal(t, []bool{true}, h.backend.stopJobs)
}

func TestCancellingExecuteContextInterruptsPureJSLoop(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := h.newRun(t, `function main() { status("spin", ""); while (true) {} }`)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := run.Execute(ctx, workflow.Input{})
		done <- err
	}()
	require.Eventually(t, func() bool { return len(h.recorder.primitives(workflow.PhaseCompleted)) == 1 },
		5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.EqualError(t, err, workflow.ErrAborted.Error())
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the context did not stop the script")
	}
}

func TestCancelledContextStopsTopLevelLoop(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := h.newRun(t, `while (true) {}
function main() {}`)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := run.Execute(ctx, workflow.Input{})
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, workflow.ErrAborted)
	case <-time.After(5 * time.Second):
		t.Fatal("top-level loop outlived its context")
	}
}

func TestUncaughtBashErrorNamesStderrAndScriptLine(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.backend.bashFn = func(context.Context, workflow.BashCall) (workflow.BashResult, error) {
		return workflow.BashResult{ExitCode: 1, Stderr: "no 'origin' remote found\n"}, nil
	}
	_, err := h.run(t, "function setup() {\n  bash(\"wtp add\")\n}\nfunction main() {\n  setup()\n}", "")
	require.EqualError(t, err, filepath.Join(h.dir, "workflow.js")+":2:7: BashError: command exited 1: wtp add: no 'origin' remote found")
}

func TestScriptErrorAfterItsContextEndsCountsAsAborted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	h.backend.askFn = func(context.Context, string, []string) (string, error) {
		// The run's context ends (shutdown) while the script waits here.
		cancel()
		return "", context.Canceled
	}
	run := h.newRun(t, `function main() { try { ask("wait") } catch (e) {} ; throw new Error("carried on") }`)
	_, err := run.Execute(ctx, workflow.Input{})
	require.ErrorIs(t, err, workflow.ErrAborted)
}

func TestScriptErrorAfterAbortCountsAsAborted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := h.newRun(t, `function main() { try { bash("slow") } catch (e) {} ; throw new Error("carried on") }`)
	h.backend.bashFn = func(ctx context.Context, _ workflow.BashCall) (workflow.BashResult, error) {
		run.Abort()
		return workflow.BashResult{}, ctx.Err()
	}
	_, err := run.Execute(t.Context(), workflow.Input{})
	require.ErrorIs(t, err, workflow.ErrAborted)
}

func TestSessionAndAgentCallsCarryTheSeqOfTheirOwnEvent(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	var mu sync.Mutex
	agentSeqs := map[string]int{}
	h.backend.agentFn = func(_ context.Context, call workflow.AgentCall) (workflow.AgentResult, error) {
		mu.Lock()
		defer mu.Unlock()
		agentSeqs[call.Prompt] = call.Seq
		return workflow.AgentResult{Text: "ok"}, nil
	}
	_, err := h.run(t, `requires({agents: ["reviewer"]})
function main() {
  session.new({title: "builder"})
  agent("reviewer", "solo")
  parallel([{agent: "reviewer", prompt: "a"}, {agent: "reviewer", prompt: "b"}])
}`, "")
	require.NoError(t, err)

	started := map[string]int{}
	for _, ev := range h.recorder.events {
		if ev.Phase != workflow.PhaseStarted {
			continue
		}
		switch ev.Primitive {
		case "session.new":
			started["session"] = ev.Seq
		case "agent":
			var args struct{ Prompt string }
			require.NoError(t, json.Unmarshal(ev.Args, &args))
			started[args.Prompt] = ev.Seq
		}
	}
	require.Len(t, h.backend.sessionCalls, 1)
	require.Equal(t, workflow.SessionCall{Title: "builder", Seq: started["session"]}, h.backend.sessionCalls[0])
	require.Equal(t, map[string]int{"solo": started["solo"], "a": started["a"], "b": started["b"]}, agentSeqs)
	require.Len(t, map[int]bool{started["solo"]: true, started["a"]: true, started["b"]: true}, 3)
}

func TestTheInvokingSessionIsOnlyForForking(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	got, err := h.run(t, `function main() {
  let refused
  try { session.current().turn("hi") } catch (e) { refused = e.message }
  const fork = session.current().fork({title: "builder", seed: "SEED"})
  fork.turn("go")
  const again = fork.fork()
  return {refused, fork: fork.id, again: again.id}
}`, "")
	require.NoError(t, err)
	out := got.(map[string]any)
	require.Contains(t, out["refused"], "fork it with session.current().fork()")
	require.Equal(t, []workflow.ForkCall{
		{Source: "invoker", FromInvoker: true, SessionCall: workflow.SessionCall{Title: "builder", Seq: h.backend.forks[0].Seq}},
		{Source: "new-1", SessionCall: workflow.SessionCall{Seq: h.backend.forks[1].Seq}},
	}, h.backend.forks)
	require.Positive(t, h.backend.forks[0].Seq)
	require.Len(t, h.backend.turns, 1)
	require.Equal(t, "new-1", h.backend.turns[0].SessionID)
	require.Equal(t, "SEED\n\ngo", h.backend.turns[0].Prompt)
}

func TestForkingARunSessionInsideExclusiveThrowsInsteadOfWaitingForever(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	got, err := h.run(t, `function main() {
  const s = session.new({})
  return workflow.exclusive(() => {
    let refused
    try { s.fork() } catch (e) { refused = e.message }
    const fromUser = session.current().fork()
    return {refused, fromUser: fromUser.id}
  })
}`, "")
	require.NoError(t, err)
	out := got.(map[string]any)
	require.Equal(t, "fork() of a run's session can't run inside workflow.exclusive", out["refused"])
	require.Len(t, h.backend.forks, 1, "forking the user's session doesn't wait on the hold")
	require.True(t, h.backend.forks[0].FromInvoker)
}
