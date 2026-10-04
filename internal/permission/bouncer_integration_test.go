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

type fakeBouncer struct {
	outcome AssessOutcome
	reason  string
	err     error
	calls   atomic.Int32
	entered chan AssessInput // Receives each input on entry when non-nil.
	release chan struct{}    // Assess blocks until closed when non-nil.
}

func (f *fakeBouncer) Assess(ctx context.Context, in AssessInput) (Assessment, error) {
	f.calls.Add(1)
	if f.entered != nil {
		f.entered <- in
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return Assessment{Reason: "bouncer error"}, ctx.Err()
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

type bouncerHarness struct {
	svc           *permissionService
	fake          *fakeBouncer
	rec           *fakeRecorder
	events        <-chan pubsub.Event[PermissionRequest]
	notifications <-chan pubsub.Event[PermissionNotification]
	dir           string
}

func newBouncerHarness(t *testing.T, fake *fakeBouncer, mode BouncerMode, rules []config.PermissionRule, store *config.ConfigStore, extra ...Option) *bouncerHarness {
	t.Helper()
	dir := t.TempDir()
	rec := &fakeRecorder{}
	opts := []Option{WithDecisionRecorder(rec)}
	if fake != nil {
		opts = append(opts, WithBouncer(BouncerOptions{Bouncer: fake, Mode: mode, Timeout: 5 * time.Second}))
	}
	opts = append(opts, extra...)
	svc := NewPermissionService(dir, config.YoloOff, rules, store, opts...).(*permissionService)
	return &bouncerHarness{
		svc:           svc,
		fake:          fake,
		rec:           rec,
		events:        svc.Subscribe(t.Context()),
		notifications: svc.SubscribeNotifications(t.Context()),
		dir:           dir,
	}
}

func (h *bouncerHarness) req(callID, input string) CreatePermissionRequest {
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
func (h *bouncerHarness) drainNotifications() []PermissionNotification {
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

func (h *bouncerHarness) assessment(t *testing.T, d Decision) AssessmentRecord {
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

func waitEntered(t *testing.T, f *fakeBouncer) AssessInput {
	t.Helper()
	select {
	case in := <-f.entered:
		return in
	case <-time.After(10 * time.Second):
		t.Fatal("bouncer was not called")
		return AssessInput{}
	}
}

func TestBouncerNotCalledWhenOff(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		fake *fakeBouncer
		mode BouncerMode
	}{
		{name: "no bouncer"},
		{name: "mode off", fake: &fakeBouncer{outcome: AssessAllow}, mode: BouncerOff},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newBouncerHarness(t, tt.fake, tt.mode, nil, nil)
			done := requestAsync(testCtx(t), h.svc, h.req("call", "rm -rf build"))
			perm := waitPrompt(t, h.events)
			require.Empty(t, perm.BouncerNote)
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

func TestBouncerNotCalledForExplicitRules(t *testing.T) {
	t.Parallel()
	for _, action := range []config.PermissionAction{config.PermissionAllow, config.PermissionDeny} {
		t.Run(string(action), func(t *testing.T) {
			t.Parallel()
			fake := &fakeBouncer{outcome: AssessAllow}
			rules := []config.PermissionRule{{ToolPattern: "bash", Action: action}}
			h := newBouncerHarness(t, fake, BouncerEnforce, rules, nil)
			r, err := h.svc.Request(testCtx(t), h.req("call", "ls"))
			require.NoError(t, err)
			require.Equal(t, action == config.PermissionAllow, r.Granted)
			require.Zero(t, fake.calls.Load())
			require.Equal(t, DecisionSourceRule, h.rec.snapshot()[0].DecidedBy)
		})
	}
}

func TestBouncerEnforceAllow(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessAllow}
	h := newBouncerHarness(t, fake, BouncerEnforce, nil, nil)
	opts := h.req("call", "go build ./...")

	r, err := h.svc.Request(testCtx(t), opts)
	require.NoError(t, err)
	require.True(t, r.Granted)
	require.Empty(t, h.events)
	require.Equal(t, []PermissionNotification{{ToolCallID: "call"}, {ToolCallID: "call", Granted: true}}, h.drainNotifications())
	decisions := h.rec.snapshot()
	require.Len(t, decisions, 1)
	require.Equal(t, DecisionSourceBouncer, decisions[0].DecidedBy)
	require.Equal(t, VerdictAllow, decisions[0].Verdict)
	rec := h.assessment(t, decisions[0])
	require.Equal(t, "enforce", rec.Mode)
	require.Equal(t, "allow", rec.Outcome)

	// A repeat is granted from the cache without calling the bouncer.
	opts.ToolCallID = "call-2"
	r, err = h.svc.Request(testCtx(t), opts)
	require.NoError(t, err)
	require.True(t, r.Granted)
	require.Equal(t, int32(1), fake.calls.Load())
	require.Empty(t, h.events)
	decisions = h.rec.snapshot()
	require.Len(t, decisions, 2)
	require.Equal(t, DecisionSourceBouncer, decisions[1].DecidedBy)
	require.Equal(t, "session allow cache", h.assessment(t, decisions[1]).Reason)

	// Different content is a different call.
	opts.Content = "other"
	opts.ToolCallID = "call-3"
	_, err = h.svc.Request(testCtx(t), opts)
	require.NoError(t, err)
	require.Equal(t, int32(2), fake.calls.Load())
}

func TestBouncerEnforceDeny(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessDeny, reason: "destructive"}
	h := newBouncerHarness(t, fake, BouncerEnforce, nil, nil)

	r, err := h.svc.Request(testCtx(t), h.req("call", "rm -rf /"))
	require.NoError(t, err)
	require.False(t, r.Granted)
	require.Contains(t, r.Reason, "permission bouncer")
	require.Contains(t, r.Reason, "destructive")
	require.Empty(t, h.events)
	decisions := h.rec.snapshot()
	require.Equal(t, DecisionSourceBouncer, decisions[0].DecidedBy)
	require.Equal(t, VerdictDeny, decisions[0].Verdict)
}

func TestBouncerEnforceEscalatePrompts(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		fake *fakeBouncer
		note string
	}{
		{name: "escalate", fake: &fakeBouncer{outcome: AssessEscalate}, note: "bouncer: escalate · destructive=0.05 severity=0.3"},
		{name: "error", fake: &fakeBouncer{outcome: AssessAllow, err: errors.New("boom")}, note: "bouncer: error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newBouncerHarness(t, tt.fake, BouncerEnforce, nil, nil)
			done := requestAsync(testCtx(t), h.svc, h.req("call", "make deploy"))
			perm := waitPrompt(t, h.events)
			require.Equal(t, tt.note, perm.BouncerNote)
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

func TestBouncerShadowAllowStillPrompts(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessAllow}
	h := newBouncerHarness(t, fake, BouncerShadow, nil, nil)
	done := requestAsync(testCtx(t), h.svc, h.req("call", "go test ./..."))
	perm := waitPrompt(t, h.events)
	require.Equal(t, "bouncer (shadow): allow · destructive=0.05 severity=0.3", perm.BouncerNote)
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

func TestBouncerExplicitAskToHuman(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessAllow}
	rules := []config.PermissionRule{{ToolPattern: "bash", Action: config.PermissionAsk}}
	h := newBouncerHarness(t, nil, "", rules, nil, WithBouncer(BouncerOptions{
		Bouncer:            fake,
		Mode:               BouncerEnforce,
		ExplicitAskToHuman: true,
	}))
	done := requestAsync(testCtx(t), h.svc, h.req("call", "ls"))
	perm := waitPrompt(t, h.events)
	require.Empty(t, perm.BouncerNote)
	h.svc.Grant(perm)
	require.NoError(t, waitResult(t, done).err)
	require.Zero(t, fake.calls.Load())
}

func TestBouncerCommitBoundaryOneHonoursNewDeny(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{
		outcome: AssessAllow,
		entered: make(chan AssessInput, 1),
		release: make(chan struct{}),
	}
	dir := t.TempDir()
	store := config.NewTestStoreWithDataPath(&config.Config{}, filepath.Join(dir, "anvil.json"))
	h := newBouncerHarness(t, fake, BouncerEnforce, nil, store)

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

func TestBouncerCommitBoundaryTwo(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		grant   func(h *bouncerHarness) error
		granted bool
		source  DecisionSource
	}{
		{
			name: "session allow",
			grant: func(h *bouncerHarness) error {
				return h.svc.GrantSession("session", "bash", "make b", config.PermissionAllow)
			},
			granted: true,
			source:  DecisionSourceSessionRule,
		},
		{
			name: "config deny",
			grant: func(h *bouncerHarness) error {
				return h.svc.GrantForever("bash", "make b", config.PermissionDeny, config.ScopeGlobal)
			},
			source: DecisionSourceRule,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeBouncer{outcome: AssessEscalate}
			lockWait := make(chan string, 2)
			store := config.NewTestStoreWithDataPath(&config.Config{}, filepath.Join(t.TempDir(), "anvil.json"))
			h := newBouncerHarness(t, fake, BouncerEnforce, nil, store)
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

func TestBouncerCancelWhileWaitingForLock(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessEscalate}
	lockWait := make(chan string, 2)
	h := newBouncerHarness(t, fake, BouncerEnforce, nil, nil)
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

func TestBouncerRunsConcurrently(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{
		outcome: AssessAllow,
		entered: make(chan AssessInput, 2),
		release: make(chan struct{}),
	}
	h := newBouncerHarness(t, fake, BouncerEnforce, nil, nil)

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

func TestBouncerNoteSkipped(t *testing.T) {
	t.Parallel()
	details, err := json.Marshal(AssessmentRecord{Outcome: "skipped", SkipReason: "protected path"})
	require.NoError(t, err)
	require.Equal(t, "bouncer: skipped · protected path", assessmentSummary(Assessment{}, details, false, BouncerEnforce).Note())
	require.Equal(t, "bouncer (shadow): escalate", assessmentSummary(Assessment{}, nil, false, BouncerShadow).Note())
	require.Empty(t, (*AssessmentSummary)(nil).Note())
}

func TestAssessmentSummaryOrdersDrivingAxesFirst(t *testing.T) {
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

	sum := assessmentSummary(Assessment{Outcome: AssessEscalate}, details, false, BouncerEnforce)
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
	require.Equal(t, "bouncer: escalate · remote_exec=0.91 destructive=0.62 exfiltration=0.1 credentials=0.02 severity=2.4 user_requested=0.88", sum.Note())
}

func TestEscalatedPromptCarriesSummary(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessEscalate}
	h := newBouncerHarness(t, fake, BouncerEnforce, nil, nil)
	done := requestAsync(testCtx(t), h.svc, h.req("call", "make deploy"))
	perm := waitPrompt(t, h.events)
	require.NotNil(t, perm.Bouncer)
	require.Equal(t, "escalate", perm.Bouncer.Outcome)
	require.Equal(t, perm.Bouncer.Note(), perm.BouncerNote)
	h.svc.Deny(perm, "no")
	require.NoError(t, waitResult(t, done).err)
}

func TestWithAssessmentMode(t *testing.T) {
	t.Parallel()
	require.Nil(t, withAssessmentMode(nil, BouncerShadow))
	require.JSONEq(t, `"not a record"`, string(withAssessmentMode(json.RawMessage(`"not a record"`), BouncerShadow)))
	out := withAssessmentMode(json.RawMessage(`{"outcome":"allow"}`), BouncerShadow)
	var rec AssessmentRecord
	require.NoError(t, json.Unmarshal(out, &rec))
	require.Equal(t, "shadow", rec.Mode)
	require.Equal(t, "allow", rec.Outcome)
}

func TestBouncerPanicEscalates(t *testing.T) {
	t.Parallel()
	h := newBouncerHarness(t, nil, "", nil, nil, WithBouncer(BouncerOptions{Bouncer: panicBouncer{}, Mode: BouncerEnforce}))
	done := requestAsync(testCtx(t), h.svc, h.req("call", "ls"))
	perm := waitPrompt(t, h.events)
	require.Equal(t, "bouncer: error", perm.BouncerNote)
	h.svc.Grant(perm)
	require.NoError(t, waitResult(t, done).err)
}

type panicBouncer struct{}

func (panicBouncer) Assess(context.Context, AssessInput) (Assessment, error) {
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

func TestBouncerAllowCacheKeyedOnDiff(t *testing.T) {
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
		fake := &fakeBouncer{outcome: AssessAllow}
		h := newBouncerHarness(t, fake, BouncerEnforce, nil, nil)
		_, err := h.svc.Request(testCtx(t), edit("a", "-// comment\n"))
		require.NoError(t, err)
		_, err = h.svc.Request(testCtx(t), edit("b", "-func main() {}\n-func run() {}\n"))
		require.NoError(t, err)
		require.Equal(t, int32(2), fake.calls.Load())
	})
	t.Run("same diff hits cache", func(t *testing.T) {
		t.Parallel()
		fake := &fakeBouncer{outcome: AssessAllow}
		h := newBouncerHarness(t, fake, BouncerEnforce, nil, nil)
		_, err := h.svc.Request(testCtx(t), edit("a", "-// comment\n"))
		require.NoError(t, err)
		r, err := h.svc.Request(testCtx(t), edit("b", "-// comment\n"))
		require.NoError(t, err)
		require.True(t, r.Granted)
		require.Equal(t, int32(1), fake.calls.Load())
	})
}

func TestBouncerReceivesDiff(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessAllow, entered: make(chan AssessInput, 1)}
	h := newBouncerHarness(t, fake, BouncerEnforce, nil, nil)
	opts := h.req("call", "/work/main.go")
	opts.ToolName = "edit"
	opts.Diff = "-gone\n"
	_, err := h.svc.Request(testCtx(t), opts)
	require.NoError(t, err)
	require.Equal(t, "-gone\n", waitEntered(t, fake).Diff)
}

func TestBouncerAllowCacheRechecksPolicy(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessAllow}
	h := newBouncerHarness(t, fake, BouncerEnforce, nil, nil)
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

func TestBouncerShadowSkipsAllowCache(t *testing.T) {
	t.Parallel()
	for _, prefilled := range []bool{true, false} {
		t.Run(fmt.Sprintf("prefilled=%v", prefilled), func(t *testing.T) {
			t.Parallel()
			fake := &fakeBouncer{outcome: AssessAllow}
			h := newBouncerHarness(t, fake, BouncerShadow, nil, nil)
			opts := h.req("a", "go test ./...")
			if prefilled {
				h.svc.allowCache.Load().Set(allowCacheKey(opts), struct{}{})
			}

			for _, id := range []string{"a", "b"} {
				opts.ToolCallID = id
				done := requestAsync(testCtx(t), h.svc, opts)
				perm := waitPrompt(t, h.events)
				require.Equal(t, "bouncer (shadow): allow · destructive=0.05 severity=0.3", perm.BouncerNote)
				h.svc.Grant(perm)
				require.NoError(t, waitResult(t, done).err)
			}
			require.Equal(t, int32(2), fake.calls.Load())
			if !prefilled {
				require.Zero(t, h.svc.allowCache.Load().Len())
			}
		})
	}
}

func newYoloBouncerHarness(t *testing.T, fake *fakeBouncer, mode BouncerMode, level config.YoloLevel, rules []config.PermissionRule, extra ...Option) *bouncerHarness {
	t.Helper()
	h := newBouncerHarness(t, fake, mode, rules, nil, extra...)
	h.svc.SetYoloLevel(level)
	return h
}

func TestYoloWithBouncer(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		fake    *fakeBouncer
		mode    BouncerMode
		granted bool
		source  DecisionSource
		calls   int32
	}{
		{name: "enforce allow stands", fake: &fakeBouncer{outcome: AssessAllow}, mode: BouncerEnforce, granted: true, source: DecisionSourceBouncer, calls: 1},
		{name: "enforce deny blocks", fake: &fakeBouncer{outcome: AssessDeny, reason: "destructive"}, mode: BouncerEnforce, granted: false, source: DecisionSourceBouncer, calls: 1},
		{name: "enforce escalate is approved by yolo", fake: &fakeBouncer{outcome: AssessEscalate}, mode: BouncerEnforce, granted: true, source: DecisionSourceYolo, calls: 1},
		{name: "bouncer error is approved by yolo", fake: &fakeBouncer{outcome: AssessAllow, err: errors.New("boom")}, mode: BouncerEnforce, granted: true, source: DecisionSourceYolo, calls: 1},
		{name: "shadow deny never blocks", fake: &fakeBouncer{outcome: AssessDeny}, mode: BouncerShadow, granted: true, source: DecisionSourceYolo, calls: 1},
		{name: "bouncer off keeps plain yolo", fake: &fakeBouncer{outcome: AssessDeny}, mode: BouncerOff, granted: true, source: DecisionSourceYolo, calls: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newYoloBouncerHarness(t, tt.fake, tt.mode, config.YoloStandard, nil)
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
				require.Contains(t, r.Reason, "permission bouncer")
			}
		})
	}
}

func TestYoloWithBouncerRespectsRules(t *testing.T) {
	t.Parallel()
	rules := []config.PermissionRule{{ToolPattern: "bash", SubRules: []config.PermissionSubRule{
		{InputPattern: "rm *", Action: config.PermissionDeny},
		{InputPattern: "go test *", Action: config.PermissionAllow},
	}}}
	fake := &fakeBouncer{outcome: AssessAllow}
	h := newYoloBouncerHarness(t, fake, BouncerEnforce, config.YoloStandard, rules)

	r, err := h.svc.Request(testCtx(t), h.req("deny", "rm -rf /"))
	require.NoError(t, err)
	require.False(t, r.Granted, "explicit deny rules still win")

	r, err = h.svc.Request(testCtx(t), h.req("allow", "go test ./..."))
	require.NoError(t, err)
	require.True(t, r.Granted)
	require.Zero(t, fake.calls.Load(), "explicit rules never reach the bouncer")
}

func TestYoloWithBouncerExplicitAskToHuman(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessDeny}
	rules := []config.PermissionRule{{ToolPattern: "bash", Action: config.PermissionAsk}}
	h := newYoloBouncerHarness(t, nil, "", config.YoloStandard, rules, WithBouncer(BouncerOptions{
		Bouncer:            fake,
		Mode:               BouncerEnforce,
		ExplicitAskToHuman: true,
	}))
	r, err := h.svc.Request(testCtx(t), h.req("call", "ls"))
	require.NoError(t, err)
	require.True(t, r.Granted, "yolo approves asks the bouncer never sees")
	require.Zero(t, fake.calls.Load())
	require.Empty(t, h.events)
}

func TestYoloFullBypassesBouncer(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessDeny}
	h := newYoloBouncerHarness(t, fake, BouncerEnforce, config.YoloFull, nil)
	r, err := h.svc.Request(testCtx(t), h.req("call", "rm -rf build"))
	require.NoError(t, err)
	require.True(t, r.Granted)
	require.Zero(t, fake.calls.Load())
}

func TestNoYoloEscalateStillPrompts(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessEscalate}
	h := newYoloBouncerHarness(t, fake, BouncerEnforce, config.YoloOff, nil)
	done := requestAsync(testCtx(t), h.svc, h.req("call", "make deploy"))
	perm := waitPrompt(t, h.events)
	h.svc.Grant(perm)
	r := waitResult(t, done)
	require.NoError(t, r.err)
	require.True(t, r.result.Granted)
	require.Equal(t, DecisionSourceHuman, h.rec.snapshot()[0].DecidedBy)
}

func TestYoloWithBouncerTurnedOffMidFlight(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessDeny, entered: make(chan AssessInput, 1), release: make(chan struct{})}
	h := newYoloBouncerHarness(t, fake, BouncerEnforce, config.YoloStandard, nil)
	done := requestAsync(testCtx(t), h.svc, h.req("call", "rm -rf build"))
	waitEntered(t, fake)
	h.svc.bouncerMode.Store(BouncerOff)
	close(fake.release)
	r := waitResult(t, done)
	require.NoError(t, r.err)
	require.True(t, r.result.Granted, "a deny from a now-disabled bouncer must not apply")
	require.Empty(t, h.events, "yolo must never prompt")
	require.Equal(t, DecisionSourceYolo, h.rec.snapshot()[0].DecidedBy)
}

func TestSetBouncerModeWithoutBouncerIsNoOp(t *testing.T) {
	t.Parallel()
	h := newBouncerHarness(t, nil, "", nil, nil)
	require.False(t, h.svc.BouncerConfigured())
	h.svc.SetBouncerMode(BouncerEnforce)
	require.Equal(t, BouncerOff, h.svc.BouncerMode())
}

func TestSetBouncerModeIgnoresUnknownMode(t *testing.T) {
	t.Parallel()
	h := newBouncerHarness(t, &fakeBouncer{outcome: AssessAllow}, BouncerShadow, nil, nil)
	require.True(t, h.svc.BouncerConfigured())
	h.svc.SetBouncerMode("bogus")
	require.Equal(t, BouncerShadow, h.svc.BouncerMode())
}

func TestSetBouncerModeEnforceSkipsPrompt(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessAllow}
	h := newBouncerHarness(t, fake, BouncerOff, nil, nil)

	done := requestAsync(testCtx(t), h.svc, h.req("call-1", "go build ./..."))
	perm := waitPrompt(t, h.events)
	h.svc.Grant(perm)
	require.NoError(t, waitResult(t, done).err)
	require.Zero(t, fake.calls.Load())

	h.svc.SetBouncerMode(BouncerEnforce)
	require.Equal(t, BouncerEnforce, h.svc.BouncerMode())
	r, err := h.svc.Request(testCtx(t), h.req("call-2", "go build ./..."))
	require.NoError(t, err)
	require.True(t, r.Granted)
	require.Empty(t, h.events)
	require.Equal(t, int32(1), fake.calls.Load())
	decisions := h.rec.snapshot()
	require.Len(t, decisions, 2)
	require.Equal(t, DecisionSourceBouncer, decisions[1].DecidedBy)
}

func newWarmHarness(t *testing.T, mode BouncerMode) (*permissionService, <-chan struct{}) {
	t.Helper()
	warmed := make(chan struct{}, 16)
	svc := NewPermissionService(t.TempDir(), config.YoloOff, nil, nil, WithBouncer(BouncerOptions{
		Bouncer: &fakeBouncer{outcome: AssessAllow},
		Mode:    mode,
		Warm:    func(context.Context) { warmed <- struct{}{} },
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
		t.Fatal("bouncer warmed more than expected")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSetBouncerModeWarmsOncePerEnable(t *testing.T) {
	t.Parallel()
	svc, warmed := newWarmHarness(t, BouncerOff)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { svc.SetBouncerMode(BouncerEnforce) })
	}
	wg.Wait()
	waitWarms(t, warmed, 1)
}

func TestSetBouncerModeOnToOnDoesNotWarm(t *testing.T) {
	t.Parallel()
	svc, warmed := newWarmHarness(t, BouncerShadow)
	svc.SetBouncerMode(BouncerEnforce)
	require.Equal(t, BouncerEnforce, svc.BouncerMode())
	waitWarms(t, warmed, 0)
}

func TestSetBouncerModeReenableWarmsAgain(t *testing.T) {
	t.Parallel()
	svc, warmed := newWarmHarness(t, BouncerOff)
	svc.SetBouncerMode(BouncerEnforce)
	waitWarms(t, warmed, 1)
	svc.SetBouncerMode(BouncerOff)
	svc.SetBouncerMode(BouncerEnforce)
	waitWarms(t, warmed, 1)
}

// TestYoloEnabledWhileWaitingForPromptLock covers commit boundary 2: a
// request that skipped the bouncer, then waits for the prompt slot, is
// approved by yolo rather than prompting once yolo and the bouncer are
// both switched on.
func TestYoloEnabledWhileWaitingForPromptLock(t *testing.T) {
	t.Parallel()
	fake := &fakeBouncer{outcome: AssessDeny}
	h := newYoloBouncerHarness(t, fake, BouncerOff, config.YoloOff, nil)
	h.svc.beforePromptLock = func(CreatePermissionRequest) {
		h.svc.SetYoloLevel(config.YoloStandard)
		h.svc.bouncerMode.Store(BouncerEnforce)
	}
	r, err := h.svc.Request(testCtx(t), h.req("call", "make deploy"))
	require.NoError(t, err)
	require.True(t, r.Granted)
	require.Empty(t, h.events, "yolo must never prompt")
	require.Zero(t, fake.calls.Load(), "the bouncer was off when this request passed it")
	require.Equal(t, DecisionSourceYolo, h.rec.snapshot()[0].DecidedBy)
}

// TestYoloFullEnabledMidFlightStillPrompts pins current behaviour: full
// yolo is checked once on entry, so enabling it mid-flight leaves an
// in-flight request prompting. This fails towards more prompting.
func TestYoloFullEnabledMidFlightStillPrompts(t *testing.T) {
	t.Parallel()
	h := newYoloBouncerHarness(t, nil, "", config.YoloOff, nil)
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
