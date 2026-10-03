package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/stretchr/testify/require"
)

type fakeAssessor struct {
	outcome AssessOutcome
	reason  string
	err     error
	calls   atomic.Int32
	entered chan AssessInput // Receives each input on entry when non-nil.
	release chan struct{}    // Assess blocks until closed when non-nil.
}

func (f *fakeAssessor) Assess(ctx context.Context, in AssessInput) (Assessment, error) {
	f.calls.Add(1)
	if f.entered != nil {
		f.entered <- in
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return Assessment{Reason: "assessor error"}, ctx.Err()
		}
	}
	severity := 0.3
	rec := AssessmentRecord{
		SchemaVersion: AssessmentSchemaVersion,
		Outcome:       f.outcome.String(),
		Nouls:         map[string]float64{"destructive": 0.05},
		Severity:      &severity,
	}
	if f.err != nil {
		rec.Outcome = "error"
		rec.Error = f.err.Error()
	}
	details, _ := json.Marshal(rec)
	return Assessment{Outcome: f.outcome, Reason: f.reason, Details: details}, f.err
}

type assessorHarness struct {
	svc           *permissionService
	fake          *fakeAssessor
	rec           *fakeRecorder
	events        <-chan pubsub.Event[PermissionRequest]
	notifications <-chan pubsub.Event[PermissionNotification]
	dir           string
}

func newAssessorHarness(t *testing.T, fake *fakeAssessor, mode AssessorMode, rules []config.PermissionRule, store *config.ConfigStore, extra ...Option) *assessorHarness {
	t.Helper()
	dir := t.TempDir()
	rec := &fakeRecorder{}
	opts := []Option{WithDecisionRecorder(rec)}
	if fake != nil {
		opts = append(opts, WithAssessor(AssessorOptions{Assessor: fake, Mode: mode, Timeout: 5 * time.Second}))
	}
	opts = append(opts, extra...)
	svc := NewPermissionService(dir, config.YoloOff, rules, store, opts...).(*permissionService)
	return &assessorHarness{
		svc:           svc,
		fake:          fake,
		rec:           rec,
		events:        svc.Subscribe(t.Context()),
		notifications: svc.SubscribeNotifications(t.Context()),
		dir:           dir,
	}
}

func (h *assessorHarness) req(callID, input string) CreatePermissionRequest {
	return CreatePermissionRequest{
		SessionID:  "session",
		ToolCallID: callID,
		ToolName:   "bash",
		Action:     "execute",
		Input:      input,
		Path:       h.dir,
	}
}

// drainNotifications returns the notifications published so far.
func (h *assessorHarness) drainNotifications() []PermissionNotification {
	var out []PermissionNotification
	for {
		select {
		case n := <-h.notifications:
			out = append(out, n.Payload)
		default:
			return out
		}
	}
}

func (h *assessorHarness) assessment(t *testing.T, d Decision) AssessmentRecord {
	t.Helper()
	require.NotEmpty(t, d.Assessment)
	var rec AssessmentRecord
	require.NoError(t, json.Unmarshal(d.Assessment, &rec))
	return rec
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// requestAsync runs Request in a goroutine and returns a channel that
// yields its result.
type asyncResult struct {
	result RequestResult
	err    error
}

func requestAsync(ctx context.Context, svc *permissionService, opts CreatePermissionRequest) <-chan asyncResult {
	ch := make(chan asyncResult, 1)
	go func() {
		r, err := svc.Request(ctx, opts)
		ch <- asyncResult{r, err}
	}()
	return ch
}

func waitResult(t *testing.T, ch <-chan asyncResult) asyncResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("request did not return")
		return asyncResult{}
	}
}

func waitPrompt(t *testing.T, events <-chan pubsub.Event[PermissionRequest]) PermissionRequest {
	t.Helper()
	select {
	case e := <-events:
		return e.Payload
	case <-time.After(10 * time.Second):
		t.Fatal("prompt was not published")
		return PermissionRequest{}
	}
}

func waitEntered(t *testing.T, f *fakeAssessor) AssessInput {
	t.Helper()
	select {
	case in := <-f.entered:
		return in
	case <-time.After(10 * time.Second):
		t.Fatal("assessor was not called")
		return AssessInput{}
	}
}

func TestAssessorNotCalledWhenOff(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		fake *fakeAssessor
		mode AssessorMode
	}{
		{name: "no assessor"},
		{name: "mode off", fake: &fakeAssessor{outcome: AssessAllow}, mode: AssessorOff},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newAssessorHarness(t, tt.fake, tt.mode, nil, nil)
			done := requestAsync(testCtx(t), h.svc, h.req("call", "rm -rf build"))
			perm := waitPrompt(t, h.events)
			require.Empty(t, perm.AssessorNote)
			h.svc.Grant(perm)
			r := waitResult(t, done)
			require.NoError(t, r.err)
			require.True(t, r.result.Granted)
			if tt.fake != nil {
				require.Zero(t, tt.fake.calls.Load())
			}
			decisions := h.rec.snapshot()
			require.Len(t, decisions, 1)
			require.Equal(t, DecisionSourceHuman, decisions[0].DecidedBy)
			require.Empty(t, decisions[0].Assessment)
		})
	}
}

func TestAssessorNotCalledForExplicitRules(t *testing.T) {
	t.Parallel()
	for _, action := range []config.PermissionAction{config.PermissionAllow, config.PermissionDeny} {
		t.Run(string(action), func(t *testing.T) {
			t.Parallel()
			fake := &fakeAssessor{outcome: AssessAllow}
			rules := []config.PermissionRule{{ToolPattern: "bash", Action: action}}
			h := newAssessorHarness(t, fake, AssessorEnforce, rules, nil)
			r, err := h.svc.Request(testCtx(t), h.req("call", "ls"))
			require.NoError(t, err)
			require.Equal(t, action == config.PermissionAllow, r.Granted)
			require.Zero(t, fake.calls.Load())
			require.Equal(t, DecisionSourceRule, h.rec.snapshot()[0].DecidedBy)
		})
	}
}

func TestAssessorEnforceAllow(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessAllow}
	h := newAssessorHarness(t, fake, AssessorEnforce, nil, nil)
	opts := h.req("call", "go build ./...")

	r, err := h.svc.Request(testCtx(t), opts)
	require.NoError(t, err)
	require.True(t, r.Granted)
	require.Empty(t, h.events)
	require.Equal(t, []PermissionNotification{{ToolCallID: "call"}, {ToolCallID: "call", Granted: true}}, h.drainNotifications())
	decisions := h.rec.snapshot()
	require.Len(t, decisions, 1)
	require.Equal(t, DecisionSourceAssessor, decisions[0].DecidedBy)
	require.Equal(t, VerdictAllow, decisions[0].Verdict)
	rec := h.assessment(t, decisions[0])
	require.Equal(t, "enforce", rec.Mode)
	require.Equal(t, "allow", rec.Outcome)

	// A repeat is granted from the cache without calling the assessor.
	opts.ToolCallID = "call-2"
	r, err = h.svc.Request(testCtx(t), opts)
	require.NoError(t, err)
	require.True(t, r.Granted)
	require.Equal(t, int32(1), fake.calls.Load())
	require.Empty(t, h.events)
	decisions = h.rec.snapshot()
	require.Len(t, decisions, 2)
	require.Equal(t, DecisionSourceAssessor, decisions[1].DecidedBy)
	require.Equal(t, "session allow cache", h.assessment(t, decisions[1]).Reason)

	// Different content is a different call.
	opts.Content = "other"
	opts.ToolCallID = "call-3"
	_, err = h.svc.Request(testCtx(t), opts)
	require.NoError(t, err)
	require.Equal(t, int32(2), fake.calls.Load())
}

func TestAssessorEnforceDeny(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessDeny, reason: "destructive"}
	h := newAssessorHarness(t, fake, AssessorEnforce, nil, nil)

	r, err := h.svc.Request(testCtx(t), h.req("call", "rm -rf /"))
	require.NoError(t, err)
	require.False(t, r.Granted)
	require.Contains(t, r.Reason, "permission assessor")
	require.Contains(t, r.Reason, "destructive")
	require.Empty(t, h.events)
	decisions := h.rec.snapshot()
	require.Equal(t, DecisionSourceAssessor, decisions[0].DecidedBy)
	require.Equal(t, VerdictDeny, decisions[0].Verdict)
}

func TestAssessorEnforceEscalatePrompts(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		fake *fakeAssessor
		note string
	}{
		{name: "escalate", fake: &fakeAssessor{outcome: AssessEscalate}, note: "assessor: escalate · destructive=0.05 severity=0.3"},
		{name: "error", fake: &fakeAssessor{outcome: AssessAllow, err: errors.New("boom")}, note: "assessor: error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newAssessorHarness(t, tt.fake, AssessorEnforce, nil, nil)
			done := requestAsync(testCtx(t), h.svc, h.req("call", "make deploy"))
			perm := waitPrompt(t, h.events)
			require.Equal(t, tt.note, perm.AssessorNote)
			h.svc.Deny(perm, "no")
			r := waitResult(t, done)
			require.NoError(t, r.err)
			require.False(t, r.result.Granted)
			d := h.rec.snapshot()[0]
			require.Equal(t, DecisionSourceHuman, d.DecidedBy)
			require.Equal(t, "enforce", h.assessment(t, d).Mode)
		})
	}
}

func TestAssessorShadowAllowStillPrompts(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessAllow}
	h := newAssessorHarness(t, fake, AssessorShadow, nil, nil)
	done := requestAsync(testCtx(t), h.svc, h.req("call", "go test ./..."))
	perm := waitPrompt(t, h.events)
	require.Equal(t, "assessor (shadow): allow · destructive=0.05 severity=0.3", perm.AssessorNote)
	h.svc.Grant(perm)
	r := waitResult(t, done)
	require.NoError(t, r.err)
	require.True(t, r.result.Granted)
	decisions := h.rec.snapshot()
	require.Len(t, decisions, 1)
	require.Equal(t, DecisionSourceHuman, decisions[0].DecidedBy)
	rec := h.assessment(t, decisions[0])
	require.Equal(t, "shadow", rec.Mode)
	require.Equal(t, "allow", rec.Outcome)
}

func TestAssessorExplicitAskToHuman(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessAllow}
	rules := []config.PermissionRule{{ToolPattern: "bash", Action: config.PermissionAsk}}
	h := newAssessorHarness(t, nil, "", rules, nil, WithAssessor(AssessorOptions{
		Assessor:           fake,
		Mode:               AssessorEnforce,
		ExplicitAskToHuman: true,
	}))
	done := requestAsync(testCtx(t), h.svc, h.req("call", "ls"))
	perm := waitPrompt(t, h.events)
	require.Empty(t, perm.AssessorNote)
	h.svc.Grant(perm)
	require.NoError(t, waitResult(t, done).err)
	require.Zero(t, fake.calls.Load())
}

func TestAssessorCommitBoundaryOneHonoursNewDeny(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{
		outcome: AssessAllow,
		entered: make(chan AssessInput, 1),
		release: make(chan struct{}),
	}
	dir := t.TempDir()
	store := config.NewTestStoreWithDataPath(&config.Config{}, filepath.Join(dir, "anvil.json"))
	h := newAssessorHarness(t, fake, AssessorEnforce, nil, store)

	done := requestAsync(testCtx(t), h.svc, h.req("call", "git push --force"))
	waitEntered(t, fake)
	require.NoError(t, h.svc.GrantForever("bash", "git push *", config.PermissionDeny, config.ScopeGlobal))
	close(fake.release)

	r := waitResult(t, done)
	require.NoError(t, r.err)
	require.False(t, r.result.Granted)
	require.Equal(t, `denied by rule "bash:git push *"`, r.result.Reason)
	require.Empty(t, h.events)
	d := h.rec.snapshot()[0]
	require.Equal(t, DecisionSourceRule, d.DecidedBy)
	require.Equal(t, VerdictDeny, d.Verdict)
}

func TestAssessorCommitBoundaryTwo(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		grant   func(h *assessorHarness) error
		granted bool
		source  DecisionSource
	}{
		{
			name: "session allow",
			grant: func(h *assessorHarness) error {
				return h.svc.GrantSession("session", "bash", "make b", config.PermissionAllow)
			},
			granted: true,
			source:  DecisionSourceSessionRule,
		},
		{
			name: "config deny",
			grant: func(h *assessorHarness) error {
				return h.svc.GrantForever("bash", "make b", config.PermissionDeny, config.ScopeGlobal)
			},
			source: DecisionSourceRule,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeAssessor{outcome: AssessEscalate}
			lockWait := make(chan string, 2)
			store := config.NewTestStoreWithDataPath(&config.Config{}, filepath.Join(t.TempDir(), "anvil.json"))
			h := newAssessorHarness(t, fake, AssessorEnforce, nil, store)
			h.svc.beforePromptLock = func(o CreatePermissionRequest) { lockWait <- o.ToolCallID }

			a := requestAsync(testCtx(t), h.svc, h.req("a", "make a"))
			require.Equal(t, "a", <-lockWait)
			permA := waitPrompt(t, h.events)

			b := requestAsync(testCtx(t), h.svc, h.req("b", "make b"))
			// B has passed commit boundary 1 and cannot take the lock
			// until A resolves.
			require.Equal(t, "b", <-lockWait)
			require.NoError(t, tt.grant(h))
			h.drainNotifications()
			h.svc.Grant(permA)

			require.NoError(t, waitResult(t, a).err)
			rb := waitResult(t, b)
			require.NoError(t, rb.err)
			require.Equal(t, tt.granted, rb.result.Granted)
			require.Empty(t, h.events, "B must not prompt")

			var bNotes []PermissionNotification
			for _, n := range h.drainNotifications() {
				if n.ToolCallID == "b" {
					bNotes = append(bNotes, n)
				}
			}
			require.Equal(t, []PermissionNotification{{ToolCallID: "b", Granted: tt.granted, Denied: !tt.granted, Reason: rb.result.Reason}}, bNotes)

			var bDecision Decision
			for _, d := range h.rec.snapshot() {
				if d.ToolCallID == "b" {
					bDecision = d
				}
			}
			require.Equal(t, tt.source, bDecision.DecidedBy)
			require.Equal(t, "enforce", h.assessment(t, bDecision).Mode)
		})
	}
}

func TestAssessorCancelWhileWaitingForLock(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessEscalate}
	lockWait := make(chan string, 2)
	h := newAssessorHarness(t, fake, AssessorEnforce, nil, nil)
	h.svc.beforePromptLock = func(o CreatePermissionRequest) { lockWait <- o.ToolCallID }

	a := requestAsync(testCtx(t), h.svc, h.req("a", "make a"))
	require.Equal(t, "a", <-lockWait)
	permA := waitPrompt(t, h.events)

	bctx, cancelB := context.WithCancel(testCtx(t))
	b := requestAsync(bctx, h.svc, h.req("b", "make b"))
	require.Equal(t, "b", <-lockWait)
	cancelB()
	h.svc.Grant(permA)

	require.NoError(t, waitResult(t, a).err)
	rb := waitResult(t, b)
	require.ErrorIs(t, rb.err, context.Canceled)
	require.Empty(t, h.events, "B must not prompt")

	h.svc.activeRequestMu.Lock()
	active := h.svc.activeRequest
	h.svc.activeRequestMu.Unlock()
	require.Nil(t, active)

	var verdicts []Verdict
	for _, d := range h.rec.snapshot() {
		if d.ToolCallID == "b" {
			verdicts = append(verdicts, d.Verdict)
		}
	}
	require.Equal(t, []Verdict{VerdictCancelled}, verdicts)
}

func TestAssessorRunsConcurrently(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{
		outcome: AssessAllow,
		entered: make(chan AssessInput, 2),
		release: make(chan struct{}),
	}
	h := newAssessorHarness(t, fake, AssessorEnforce, nil, nil)

	first := requestAsync(testCtx(t), h.svc, h.req("a", "make a"))
	second := requestAsync(testCtx(t), h.svc, h.req("b", "make b"))
	entered := map[string]bool{}
	entered[waitEntered(t, fake).Input] = true
	entered[waitEntered(t, fake).Input] = true
	require.Equal(t, map[string]bool{"make a": true, "make b": true}, entered)
	close(fake.release)

	for _, ch := range []<-chan asyncResult{first, second} {
		r := waitResult(t, ch)
		require.NoError(t, r.err)
		require.True(t, r.result.Granted)
	}
}

func TestAssessorNoteSkipped(t *testing.T) {
	t.Parallel()
	details, err := json.Marshal(AssessmentRecord{Outcome: "skipped", SkipReason: "protected path"})
	require.NoError(t, err)
	require.Equal(t, "assessor: skipped · protected path", assessorSummary(Assessment{}, details, false, AssessorEnforce).Note())
	require.Equal(t, "assessor (shadow): escalate", assessorSummary(Assessment{}, nil, false, AssessorShadow).Note())
	require.Empty(t, (*AssessorSummary)(nil).Note())
}

func TestAssessorSummaryOrdersDrivingAxesFirst(t *testing.T) {
	t.Parallel()
	severity := 2.4
	details, err := json.Marshal(AssessmentRecord{
		Outcome: "escalate",
		Nouls: map[string]float64{
			"credentials":     0.02,
			"destructive":     0.62,
			"exfiltration":    0.10,
			"remote_exec":     0.91,
			UserRequestedAxis: 0.88,
		},
		Severity: &severity,
		Triggers: map[string]string{
			"destructive":     TriggerEscalate,
			"remote_exec":     TriggerDeny,
			SeverityAxis:      TriggerEscalate,
			UserRequestedAxis: TriggerMitigate,
		},
	})
	require.NoError(t, err)

	sum := assessorSummary(Assessment{Outcome: AssessEscalate}, details, false, AssessorEnforce)
	require.Equal(t, "escalate", sum.Outcome)
	require.False(t, sum.Shadow)
	var names, triggers []string
	for _, sc := range sum.Scores {
		names = append(names, sc.Name)
		triggers = append(triggers, sc.Trigger)
	}
	require.Equal(t, []string{"remote_exec", "destructive", "exfiltration", "credentials", SeverityAxis, UserRequestedAxis}, names)
	require.Equal(t, []string{TriggerDeny, TriggerEscalate, "", "", TriggerEscalate, TriggerMitigate}, triggers)
	require.Equal(t, float64(3), sum.Scores[4].Max)
	require.Equal(t, float64(1), sum.Scores[0].Max)
	require.Equal(t, "assessor: escalate · remote_exec=0.91 destructive=0.62 exfiltration=0.1 credentials=0.02 severity=2.4 user_requested=0.88", sum.Note())
}

func TestEscalatedPromptCarriesSummary(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessEscalate}
	h := newAssessorHarness(t, fake, AssessorEnforce, nil, nil)
	done := requestAsync(testCtx(t), h.svc, h.req("call", "make deploy"))
	perm := waitPrompt(t, h.events)
	require.NotNil(t, perm.Assessor)
	require.Equal(t, "escalate", perm.Assessor.Outcome)
	require.Equal(t, perm.Assessor.Note(), perm.AssessorNote)
	h.svc.Deny(perm, "no")
	require.NoError(t, waitResult(t, done).err)
}

func TestWithAssessmentMode(t *testing.T) {
	t.Parallel()
	require.Nil(t, withAssessmentMode(nil, AssessorShadow))
	require.JSONEq(t, `"not a record"`, string(withAssessmentMode(json.RawMessage(`"not a record"`), AssessorShadow)))
	out := withAssessmentMode(json.RawMessage(`{"outcome":"allow"}`), AssessorShadow)
	var rec AssessmentRecord
	require.NoError(t, json.Unmarshal(out, &rec))
	require.Equal(t, "shadow", rec.Mode)
	require.Equal(t, "allow", rec.Outcome)
}

func TestAssessorPanicEscalates(t *testing.T) {
	t.Parallel()
	h := newAssessorHarness(t, nil, "", nil, nil, WithAssessor(AssessorOptions{Assessor: panicAssessor{}, Mode: AssessorEnforce}))
	done := requestAsync(testCtx(t), h.svc, h.req("call", "ls"))
	perm := waitPrompt(t, h.events)
	require.Equal(t, "assessor: error", perm.AssessorNote)
	h.svc.Grant(perm)
	require.NoError(t, waitResult(t, done).err)
}

type panicAssessor struct{}

func (panicAssessor) Assess(context.Context, AssessInput) (Assessment, error) {
	panic("boom")
}

func TestAllowCacheKeyIncludesEditFacts(t *testing.T) {
	t.Parallel()
	base := CreatePermissionRequest{SessionID: "s", ToolName: "edit", Action: "write", Path: "/w", Input: "/w/a.go", Content: "x", Diff: "-a\n+x\n"}
	key := allowCacheKey(base)
	for name, mutate := range map[string]func(*CreatePermissionRequest){
		"diff":   func(o *CreatePermissionRequest) { o.Diff = "-everything\n+x\n" },
		"action": func(o *CreatePermissionRequest) { o.Action = "delete" },
		"path":   func(o *CreatePermissionRequest) { o.Path = "/other" },
		"shifted fields": func(o *CreatePermissionRequest) {
			o.Content, o.Diff = "x\x00-a\n+x\n", ""
		},
	} {
		o := base
		mutate(&o)
		require.NotEqual(t, key, allowCacheKey(o), name)
	}
	require.Equal(t, key, allowCacheKey(base))
}

func TestAssessorAllowCacheKeyedOnDiff(t *testing.T) {
	t.Parallel()
	edit := func(callID, diff string) CreatePermissionRequest {
		return CreatePermissionRequest{
			SessionID:  "session",
			ToolCallID: callID,
			ToolName:   "edit",
			Action:     "write",
			Input:      "/work/main.go",
			Path:       "/work",
			Content:    "package main\n",
			Diff:       diff,
		}
	}
	t.Run("different diff reassesses", func(t *testing.T) {
		t.Parallel()
		fake := &fakeAssessor{outcome: AssessAllow}
		h := newAssessorHarness(t, fake, AssessorEnforce, nil, nil)
		_, err := h.svc.Request(testCtx(t), edit("a", "-// comment\n"))
		require.NoError(t, err)
		_, err = h.svc.Request(testCtx(t), edit("b", "-func main() {}\n-func run() {}\n"))
		require.NoError(t, err)
		require.Equal(t, int32(2), fake.calls.Load())
	})
	t.Run("same diff hits cache", func(t *testing.T) {
		t.Parallel()
		fake := &fakeAssessor{outcome: AssessAllow}
		h := newAssessorHarness(t, fake, AssessorEnforce, nil, nil)
		_, err := h.svc.Request(testCtx(t), edit("a", "-// comment\n"))
		require.NoError(t, err)
		r, err := h.svc.Request(testCtx(t), edit("b", "-// comment\n"))
		require.NoError(t, err)
		require.True(t, r.Granted)
		require.Equal(t, int32(1), fake.calls.Load())
	})
}

func TestAssessorReceivesDiff(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessAllow, entered: make(chan AssessInput, 1)}
	h := newAssessorHarness(t, fake, AssessorEnforce, nil, nil)
	opts := h.req("call", "/work/main.go")
	opts.ToolName = "edit"
	opts.Diff = "-gone\n"
	_, err := h.svc.Request(testCtx(t), opts)
	require.NoError(t, err)
	require.Equal(t, "-gone\n", waitEntered(t, fake).Diff)
}

func TestAssessorAllowCacheRechecksPolicy(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessAllow}
	h := newAssessorHarness(t, fake, AssessorEnforce, nil, nil)
	opts := h.req("a", "make deploy")

	r, err := h.svc.Request(testCtx(t), opts)
	require.NoError(t, err)
	require.True(t, r.Granted)

	// The deny lands after the initial policy check but before the cache
	// is honoured.
	h.svc.beforeAllowCache = func(CreatePermissionRequest) {
		require.NoError(t, h.svc.GrantSession("session", "bash", "make deploy", config.PermissionDeny))
	}
	opts.ToolCallID = "b"
	r, err = h.svc.Request(testCtx(t), opts)
	require.NoError(t, err)
	require.False(t, r.Granted)
	require.Equal(t, `denied by rule "bash:make deploy"`, r.Reason)
	require.Equal(t, int32(1), fake.calls.Load())
	d := h.rec.snapshot()[1]
	require.Equal(t, DecisionSourceSessionRule, d.DecidedBy)
	require.Equal(t, VerdictDeny, d.Verdict)
}

func TestAssessorShadowSkipsAllowCache(t *testing.T) {
	t.Parallel()
	for _, prefilled := range []bool{true, false} {
		t.Run(fmt.Sprintf("prefilled=%v", prefilled), func(t *testing.T) {
			t.Parallel()
			fake := &fakeAssessor{outcome: AssessAllow}
			h := newAssessorHarness(t, fake, AssessorShadow, nil, nil)
			opts := h.req("a", "go test ./...")
			if prefilled {
				h.svc.allowCache.Set(allowCacheKey(opts), struct{}{})
			}

			for _, id := range []string{"a", "b"} {
				opts.ToolCallID = id
				done := requestAsync(testCtx(t), h.svc, opts)
				perm := waitPrompt(t, h.events)
				require.Equal(t, "assessor (shadow): allow · destructive=0.05 severity=0.3", perm.AssessorNote)
				h.svc.Grant(perm)
				require.NoError(t, waitResult(t, done).err)
			}
			require.Equal(t, int32(2), fake.calls.Load())
			if !prefilled {
				require.Zero(t, h.svc.allowCache.Len())
			}
		})
	}
}

func newYoloAssessorHarness(t *testing.T, fake *fakeAssessor, mode AssessorMode, level config.YoloLevel, rules []config.PermissionRule, extra ...Option) *assessorHarness {
	t.Helper()
	h := newAssessorHarness(t, fake, mode, rules, nil, extra...)
	h.svc.SetYoloLevel(level)
	return h
}

func TestYoloWithAssessor(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		fake    *fakeAssessor
		mode    AssessorMode
		granted bool
		source  DecisionSource
		calls   int32
	}{
		{name: "enforce allow stands", fake: &fakeAssessor{outcome: AssessAllow}, mode: AssessorEnforce, granted: true, source: DecisionSourceAssessor, calls: 1},
		{name: "enforce deny blocks", fake: &fakeAssessor{outcome: AssessDeny, reason: "destructive"}, mode: AssessorEnforce, granted: false, source: DecisionSourceAssessor, calls: 1},
		{name: "enforce escalate is approved by yolo", fake: &fakeAssessor{outcome: AssessEscalate}, mode: AssessorEnforce, granted: true, source: DecisionSourceYolo, calls: 1},
		{name: "assessor error is approved by yolo", fake: &fakeAssessor{outcome: AssessAllow, err: errors.New("boom")}, mode: AssessorEnforce, granted: true, source: DecisionSourceYolo, calls: 1},
		{name: "shadow deny never blocks", fake: &fakeAssessor{outcome: AssessDeny}, mode: AssessorShadow, granted: true, source: DecisionSourceYolo, calls: 1},
		{name: "assessor off keeps plain yolo", fake: &fakeAssessor{outcome: AssessDeny}, mode: AssessorOff, granted: true, source: DecisionSourceYolo, calls: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newYoloAssessorHarness(t, tt.fake, tt.mode, config.YoloStandard, nil)
			r, err := h.svc.Request(testCtx(t), h.req("call", "rm -rf build"))
			require.NoError(t, err)
			require.Equal(t, tt.granted, r.Granted)
			require.Empty(t, h.events, "yolo must never prompt")
			require.Equal(t, tt.calls, tt.fake.calls.Load())
			decisions := h.rec.snapshot()
			require.Len(t, decisions, 1)
			require.Equal(t, tt.source, decisions[0].DecidedBy)
			if tt.calls > 0 {
				require.Equal(t, string(tt.mode), h.assessment(t, decisions[0]).Mode)
			}
			if !tt.granted {
				require.Contains(t, r.Reason, "permission assessor")
			}
		})
	}
}

func TestYoloWithAssessorRespectsRules(t *testing.T) {
	t.Parallel()
	rules := []config.PermissionRule{{ToolPattern: "bash", SubRules: []config.PermissionSubRule{
		{InputPattern: "rm *", Action: config.PermissionDeny},
		{InputPattern: "go test *", Action: config.PermissionAllow},
	}}}
	fake := &fakeAssessor{outcome: AssessAllow}
	h := newYoloAssessorHarness(t, fake, AssessorEnforce, config.YoloStandard, rules)

	r, err := h.svc.Request(testCtx(t), h.req("deny", "rm -rf /"))
	require.NoError(t, err)
	require.False(t, r.Granted, "explicit deny rules still win")

	r, err = h.svc.Request(testCtx(t), h.req("allow", "go test ./..."))
	require.NoError(t, err)
	require.True(t, r.Granted)
	require.Zero(t, fake.calls.Load(), "explicit rules never reach the assessor")
}

func TestYoloWithAssessorExplicitAskToHuman(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessDeny}
	rules := []config.PermissionRule{{ToolPattern: "bash", Action: config.PermissionAsk}}
	h := newYoloAssessorHarness(t, nil, "", config.YoloStandard, rules, WithAssessor(AssessorOptions{
		Assessor:           fake,
		Mode:               AssessorEnforce,
		ExplicitAskToHuman: true,
	}))
	r, err := h.svc.Request(testCtx(t), h.req("call", "ls"))
	require.NoError(t, err)
	require.True(t, r.Granted, "yolo approves asks the assessor never sees")
	require.Zero(t, fake.calls.Load())
	require.Empty(t, h.events)
}

func TestYoloFullBypassesAssessor(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessDeny}
	h := newYoloAssessorHarness(t, fake, AssessorEnforce, config.YoloFull, nil)
	r, err := h.svc.Request(testCtx(t), h.req("call", "rm -rf build"))
	require.NoError(t, err)
	require.True(t, r.Granted)
	require.Zero(t, fake.calls.Load())
}

func TestNoYoloEscalateStillPrompts(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessEscalate}
	h := newYoloAssessorHarness(t, fake, AssessorEnforce, config.YoloOff, nil)
	done := requestAsync(testCtx(t), h.svc, h.req("call", "make deploy"))
	perm := waitPrompt(t, h.events)
	h.svc.Grant(perm)
	r := waitResult(t, done)
	require.NoError(t, r.err)
	require.True(t, r.result.Granted)
	require.Equal(t, DecisionSourceHuman, h.rec.snapshot()[0].DecidedBy)
}

func TestYoloWithAssessorTurnedOffMidFlight(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessDeny, entered: make(chan AssessInput, 1), release: make(chan struct{})}
	h := newYoloAssessorHarness(t, fake, AssessorEnforce, config.YoloStandard, nil)
	done := requestAsync(testCtx(t), h.svc, h.req("call", "rm -rf build"))
	waitEntered(t, fake)
	h.svc.assessorMode.Store(AssessorOff)
	close(fake.release)
	r := waitResult(t, done)
	require.NoError(t, r.err)
	require.True(t, r.result.Granted, "a deny from a now-disabled assessor must not apply")
	require.Empty(t, h.events, "yolo must never prompt")
	require.Equal(t, DecisionSourceYolo, h.rec.snapshot()[0].DecidedBy)
}

func TestSetAssessorModeWithoutAssessorIsNoOp(t *testing.T) {
	t.Parallel()
	h := newAssessorHarness(t, nil, "", nil, nil)
	require.False(t, h.svc.AssessorConfigured())
	h.svc.SetAssessorMode(AssessorEnforce)
	require.Equal(t, AssessorOff, h.svc.AssessorMode())
}

func TestSetAssessorModeIgnoresUnknownMode(t *testing.T) {
	t.Parallel()
	h := newAssessorHarness(t, &fakeAssessor{outcome: AssessAllow}, AssessorShadow, nil, nil)
	require.True(t, h.svc.AssessorConfigured())
	h.svc.SetAssessorMode("bogus")
	require.Equal(t, AssessorShadow, h.svc.AssessorMode())
}

func TestSetAssessorModeEnforceSkipsPrompt(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessAllow}
	h := newAssessorHarness(t, fake, AssessorOff, nil, nil)

	done := requestAsync(testCtx(t), h.svc, h.req("call-1", "go build ./..."))
	perm := waitPrompt(t, h.events)
	h.svc.Grant(perm)
	require.NoError(t, waitResult(t, done).err)
	require.Zero(t, fake.calls.Load())

	h.svc.SetAssessorMode(AssessorEnforce)
	require.Equal(t, AssessorEnforce, h.svc.AssessorMode())
	r, err := h.svc.Request(testCtx(t), h.req("call-2", "go build ./..."))
	require.NoError(t, err)
	require.True(t, r.Granted)
	require.Empty(t, h.events)
	require.Equal(t, int32(1), fake.calls.Load())
	decisions := h.rec.snapshot()
	require.Len(t, decisions, 2)
	require.Equal(t, DecisionSourceAssessor, decisions[1].DecidedBy)
}

func newWarmHarness(t *testing.T, mode AssessorMode) (*permissionService, <-chan struct{}) {
	t.Helper()
	warmed := make(chan struct{}, 16)
	svc := NewPermissionService(t.TempDir(), config.YoloOff, nil, nil, WithAssessor(AssessorOptions{
		Assessor: &fakeAssessor{outcome: AssessAllow},
		Mode:     mode,
		Warm:     func(context.Context) { warmed <- struct{}{} },
	})).(*permissionService)
	return svc, warmed
}

// waitWarms waits for want warm-ups, then checks no more arrive.
func waitWarms(t *testing.T, warmed <-chan struct{}, want int) {
	t.Helper()
	for i := range want {
		select {
		case <-warmed:
		case <-time.After(10 * time.Second):
			t.Fatalf("warm %d of %d did not run", i+1, want)
		}
	}
	select {
	case <-warmed:
		t.Fatal("assessor warmed more than expected")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSetAssessorModeWarmsOncePerEnable(t *testing.T) {
	t.Parallel()
	svc, warmed := newWarmHarness(t, AssessorOff)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { svc.SetAssessorMode(AssessorEnforce) })
	}
	wg.Wait()
	waitWarms(t, warmed, 1)
}

func TestSetAssessorModeOnToOnDoesNotWarm(t *testing.T) {
	t.Parallel()
	svc, warmed := newWarmHarness(t, AssessorShadow)
	svc.SetAssessorMode(AssessorEnforce)
	require.Equal(t, AssessorEnforce, svc.AssessorMode())
	waitWarms(t, warmed, 0)
}

func TestSetAssessorModeReenableWarmsAgain(t *testing.T) {
	t.Parallel()
	svc, warmed := newWarmHarness(t, AssessorOff)
	svc.SetAssessorMode(AssessorEnforce)
	waitWarms(t, warmed, 1)
	svc.SetAssessorMode(AssessorOff)
	svc.SetAssessorMode(AssessorEnforce)
	waitWarms(t, warmed, 1)
}

// TestYoloEnabledWhileWaitingForPromptLock covers commit boundary 2: a
// request that skipped the assessor, then waits for the prompt slot, is
// approved by yolo rather than prompting once yolo and the assessor are
// both switched on.
func TestYoloEnabledWhileWaitingForPromptLock(t *testing.T) {
	t.Parallel()
	fake := &fakeAssessor{outcome: AssessDeny}
	h := newYoloAssessorHarness(t, fake, AssessorOff, config.YoloOff, nil)
	h.svc.beforePromptLock = func(CreatePermissionRequest) {
		h.svc.SetYoloLevel(config.YoloStandard)
		h.svc.assessorMode.Store(AssessorEnforce)
	}
	r, err := h.svc.Request(testCtx(t), h.req("call", "make deploy"))
	require.NoError(t, err)
	require.True(t, r.Granted)
	require.Empty(t, h.events, "yolo must never prompt")
	require.Zero(t, fake.calls.Load(), "the assessor was off when this request passed it")
	require.Equal(t, DecisionSourceYolo, h.rec.snapshot()[0].DecidedBy)
}

// TestYoloFullEnabledMidFlightStillPrompts pins current behaviour: full
// yolo is checked once on entry, so enabling it mid-flight leaves an
// in-flight request prompting. This fails towards more prompting.
func TestYoloFullEnabledMidFlightStillPrompts(t *testing.T) {
	t.Parallel()
	h := newYoloAssessorHarness(t, nil, "", config.YoloOff, nil)
	h.svc.beforePromptLock = func(CreatePermissionRequest) {
		h.svc.SetYoloLevel(config.YoloFull)
	}
	done := requestAsync(testCtx(t), h.svc, h.req("call", "make deploy"))
	perm := waitPrompt(t, h.events)
	h.svc.Grant(perm)
	r := waitResult(t, done)
	require.NoError(t, r.err)
	require.True(t, r.result.Granted)
	require.Equal(t, DecisionSourceHuman, h.rec.snapshot()[0].DecidedBy)
}
