package herdr

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func paneJSON(tabID string) string {
	return fmt.Sprintf(`{"result":{"pane":{"pane_id":"w1:p2","tab_id":%q}}}`, tabID)
}

func tabJSON(tabID, label string, number, panes int) string {
	return fmt.Sprintf(`{"result":{"tab":{"tab_id":%q,"label":%q,"number":%d,"pane_count":%d}}}`, tabID, label, number, panes)
}

// tabFake returns a runner whose pane sits alone in tab w1:t3 with label.
func tabFake(label string, panes int) *fakeRunner {
	f := newFakeRunner()
	f.out["pane get"] = paneJSON("w1:t3")
	f.out["tab get w1:t3"] = tabJSON("w1:t3", label, 3, panes)
	return f
}

func TestTabLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		title string
		want  string
	}{
		{name: "empty", title: "", want: ""},
		{name: "plain", title: "Fix auth", want: "Fix auth"},
		{name: "newline and escape stripped", title: "Fix\nauth\x1b now", want: "Fix auth now"},
		{name: "whitespace collapsed", title: "  Fix \t  auth  ", want: "Fix auth"},
		{name: "only control characters", title: "\n\r\t", want: ""},
		{name: "exactly thirty", title: strings.Repeat("a", 30), want: strings.Repeat("a", 30)},
		{name: "capped", title: strings.Repeat("a", 31), want: strings.Repeat("a", 29) + "…"},
		{name: "multi-byte not split", title: strings.Repeat("é", 40), want: strings.Repeat("é", 29) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := TabLabel(tt.title)
			require.Equal(t, tt.want, got)
			require.LessOrEqual(t, len([]rune(got)), maxTabLabel)
		})
	}
}

func TestTabRenamesUnnamedTab(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := tabFake("3", 1)
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "Fix auth"})
		synctest.Wait()

		calls := f.recorded()
		require.Len(t, calls, 4)
		require.Equal(t, "report-agent", calls[0][1])
		require.Equal(t, []string{"pane", "get", "w1:p2"}, calls[1])
		require.Equal(t, []string{"tab", "get", "w1:t3"}, calls[2])
		require.Equal(t, []string{"tab", "rename", "w1:t3", "Fix auth"}, calls[3])
	})
}

func TestTabRenamesOurLabelOnTitleChange(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := tabFake("3", 1)
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "Fix auth"})
		synctest.Wait()
		f.set(func(f *fakeRunner) { f.out["tab get w1:t3"] = tabJSON("w1:t3", "Fix auth", 3, 1) })

		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "Fix login"})
		settle()

		require.Equal(t, [][]string{
			{"tab", "rename", "w1:t3", "Fix auth"},
			{"tab", "rename", "w1:t3", "Fix login"},
		}, f.commands("tab rename"))
	})
}

func TestTabLeavesUserNamedTab(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := tabFake("api work", 1)
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "Fix auth"})
		synctest.Wait()
		require.Empty(t, f.commands("tab rename"))
		require.Len(t, f.commands("tab get"), 1)

		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "Fix login"})
		settle()
		require.Empty(t, f.commands("tab rename"))
		require.Len(t, f.commands("tab get"), 1)
		require.Len(t, f.commands("pane get"), 2)
	})
}

func TestTabSkipsMultiPaneTab(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := tabFake("3", 2)
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "Fix auth"})
		synctest.Wait()
		require.Len(t, f.commands("tab get"), 1)
		require.Empty(t, f.commands("tab rename"))
	})
}

func TestTabLabelWithLeadingDashIsOneArg(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := tabFake("3", 1)
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "--help me"})
		synctest.Wait()
		require.Equal(t, [][]string{{"tab", "rename", "w1:t3", "--help me"}}, f.commands("tab rename"))
	})
}

func TestTabRenamedAfterPaneMove(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := tabFake("3", 1)
		r := newTestReporter(f)
		defer r.Close()

		s := State{Status: StatusIdle, SessionID: "s1", SessionTitle: "Fix auth"}
		r.Update(s)
		synctest.Wait()

		f.set(func(f *fakeRunner) {
			f.out["pane get"] = paneJSON("w1:t5")
			f.out["tab get w1:t5"] = tabJSON("w1:t5", "5", 5, 1)
		})
		s.Status = StatusWorking
		r.Update(s)
		settle()
		require.Len(t, f.commands("pane get"), 1)

		s.Status = StatusIdle
		r.Update(s)
		settle()
		require.Equal(t, [][]string{
			{"tab", "rename", "w1:t3", "Fix auth"},
			{"tab", "rename", "w1:t5", "Fix auth"},
		}, f.commands("tab rename"))
	})
}

func TestTabNoSubprocessWhenNothingToDo(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := tabFake("3", 1)
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle, SessionID: "s1"})
		synctest.Wait()
		r.Update(State{Status: StatusWorking, SessionID: "s1"})
		settle()
		r.Update(State{Status: StatusIdle, SessionID: "s1"})
		settle()
		require.Len(t, f.recorded(), 3)

		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "Fix auth"})
		settle()
		require.Len(t, f.commands("tab rename"), 1)
		before := len(f.recorded())

		r.Update(State{Status: StatusWorking, SessionID: "s1", SessionTitle: "Fix auth"})
		settle()
		r.Update(State{Status: StatusWorking, SessionID: "s2", SessionTitle: "Fix auth"})
		settle()
		calls := f.recorded()
		require.Len(t, calls, before+2)
		for _, c := range calls[before:] {
			require.Equal(t, "report-agent", c[1])
		}
	})
}

func TestTabSyncRunsAfterStateReport(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := tabFake("3", 1)
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusBlocked, Message: "bash", SessionID: "s1", SessionTitle: "Fix auth"})
		synctest.Wait()

		calls := f.recorded()
		require.NotEmpty(t, calls)
		require.Equal(t, []string{"pane", "report-agent"}, calls[0][:2])
		for _, c := range calls[1:] {
			require.NotEqual(t, "report-agent", c[1])
		}
	})
}

func TestTabRenameRetried(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := tabFake("3", 1)
		f.fail["tab rename"] = 1
		r := newTestReporter(f)
		defer r.Close()

		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "Fix auth"})
		synctest.Wait()
		require.Len(t, f.commands("tab rename"), 1)

		time.Sleep(retryDelay + time.Millisecond)
		synctest.Wait()
		require.Len(t, f.commands("tab rename"), 2)
		require.Len(t, f.commands("pane report-agent"), 1)

		before := len(f.recorded())
		time.Sleep(3 * retryDelay)
		synctest.Wait()
		require.Len(t, f.recorded(), before)
	})
}

func TestTabRestore(t *testing.T) {
	t.Parallel()

	t.Run("our label restored to number", func(t *testing.T) {
		t.Parallel()
		f := tabFake("Fix auth", 1)
		n := newTabNamer("w1:p2")
		n.ours["w1:t3"] = "Fix auth"
		n.restore(context.Background(), f)
		require.Equal(t, [][]string{{"tab", "rename", "w1:t3", "3"}}, f.commands("tab rename"))
	})

	t.Run("user rename left alone", func(t *testing.T) {
		t.Parallel()
		f := tabFake("api work", 1)
		n := newTabNamer("w1:p2")
		n.ours["w1:t3"] = "Fix auth"
		n.restore(context.Background(), f)
		require.Empty(t, f.commands("tab rename"))
	})

	t.Run("missing tab skipped", func(t *testing.T) {
		t.Parallel()
		f := tabFake("Fix auth", 1)
		f.fail["tab get"] = -1
		n := newTabNamer("w1:p2")
		n.ours["w1:t3"] = "Fix auth"
		n.restore(context.Background(), f)
		require.Empty(t, f.commands("tab rename"))
	})
}

func TestCloseRestoresTabAfterRelease(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := tabFake("3", 1)
		r := newTestReporter(f)

		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "Fix auth"})
		synctest.Wait()
		f.set(func(f *fakeRunner) { f.out["tab get w1:t3"] = tabJSON("w1:t3", "Fix auth", 3, 1) })

		r.Close()
		calls := f.recorded()
		require.Equal(t, "release-agent", calls[len(calls)-3][1])
		require.Equal(t, []string{"tab", "get", "w1:t3"}, calls[len(calls)-2])
		require.Equal(t, []string{"tab", "rename", "w1:t3", "3"}, calls[len(calls)-1])
	})
}

func TestCloseWithHungRestoreStillReleases(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := tabFake("3", 1)
		r := newTestReporter(f)

		r.Update(State{Status: StatusIdle, SessionID: "s1", SessionTitle: "Fix auth"})
		synctest.Wait()
		f.set(func(f *fakeRunner) { f.block["tab get"] = true })

		start := time.Now()
		r.Close()
		require.Equal(t, restoreTimeout, time.Since(start))

		require.Len(t, f.commands("pane release-agent"), 1)
		calls := f.recorded()
		require.Equal(t, []string{"tab", "get", "w1:t3"}, calls[len(calls)-1])
		require.Equal(t, "release-agent", calls[len(calls)-2][1])
	})
}
