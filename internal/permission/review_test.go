package permission

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/stretchr/testify/require"
)

type fakeIntent struct {
	msgs []string
	err  error
}

func (f fakeIntent) RecentUserMessages(context.Context, string, int) ([]string, error) {
	return f.msgs, f.err
}

type fakeReviewer struct {
	opinion ReviewOpinion
	err     error
	calls   chan ReviewInput
	release chan struct{} // Review blocks until closed when non-nil.
}

func (f *fakeReviewer) Review(ctx context.Context, in ReviewInput) (ReviewOpinion, error) {
	if f.calls != nil {
		f.calls <- in
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ReviewOpinion{}, ctx.Err()
		}
	}
	return f.opinion, f.err
}

func reviewOption(r Reviewer, msgs ...string) Option {
	return WithReviewer(ReviewOptions{Reviewer: r, Intent: fakeIntent{msgs: msgs}, Mode: ReviewShadow, Timeout: 5 * time.Second})
}

func waitUpdate(t *testing.T, events <-chan pubsub.Event[PermissionRequest]) PermissionRequest {
	t.Helper()
	select {
	case e := <-events:
		require.Equal(t, pubsub.UpdatedEvent, e.Type)
		return e.Payload
	case <-time.After(10 * time.Second):
		t.Fatal("review update was not published")
		return PermissionRequest{}
	}
}

// waitDecision waits for the recorder to hold one decision, since a
// decision with a review still running is recorded once it settles.
func waitDecision(t *testing.T, rec *fakeRecorder) Decision {
	t.Helper()
	require.Eventually(t, func() bool { return len(rec.snapshot()) == 1 }, 10*time.Second, 5*time.Millisecond)
	return rec.snapshot()[0]
}

func TestReviewEffect(t *testing.T) {
	t.Parallel()
	low, high := 0.3, 2.6
	allow := ReviewOpinion{Verdict: ReviewAllow}
	tests := []struct {
		name     string
		outcome  string
		severity *float64
		op       ReviewOpinion
		verified bool
		want     ReviewVerdict
	}{
		{"escalate with a verified quote allows", "escalate", &low, allow, true, ReviewAllow},
		{"escalate without a verified quote stays", "escalate", &low, allow, false, ReviewEscalate},
		{"escalate at high severity stays", "escalate", &high, allow, true, ReviewEscalate},
		{"escalate with unknown severity stays", "escalate", nil, allow, true, ReviewEscalate},
		{"escalate is never raised to deny", "escalate", &low, ReviewOpinion{Verdict: ReviewDeny}, false, ReviewEscalate},
		{"deny never goes straight to allow", "deny", &low, allow, true, ReviewEscalate},
		{"deny can drop to escalate", "deny", &low, ReviewOpinion{Verdict: ReviewEscalate}, false, ReviewEscalate},
		{"deny the reviewer agrees with stays", "deny", &low, ReviewOpinion{Verdict: ReviewDeny}, false, ReviewDeny},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, reviewEffect(tt.outcome, tt.severity, tt.op, tt.verified))
		})
	}
}

func TestVerifyQuote(t *testing.T) {
	t.Parallel()
	msgs := []string{"Looks good.\nNow commit it and   open a PR for this change.", "thanks"}
	tests := []struct {
		quote string
		want  bool
	}{
		{"open a PR for this change", true},
		{`"Open a PR  for this change."`, true},
		{"commit it and open a PR", true},
		{"push to main", false},
		{"thanks", false},
		{"ok", false},
		{"", false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, verifyQuote(tt.quote, msgs), tt.quote)
	}
}

func TestReviewShownAndRecordedWithPrompt(t *testing.T) {
	t.Parallel()
	reviewer := &fakeReviewer{
		opinion: ReviewOpinion{Verdict: ReviewAllow, Quote: "open a PR for this", Reason: "the user asked for a PR", Model: "small"},
		calls:   make(chan ReviewInput, 1),
		release: make(chan struct{}),
	}
	h := newBouncerHarness(t, &fakeBouncer{outcome: AssessEscalate}, BouncerEnforce, nil, nil,
		reviewOption(reviewer, "please open a PR for this"))
	done := requestAsync(testCtx(t), h.svc, h.req("call", "gh pr create"))

	perm := waitPrompt(t, h.events)
	require.NotNil(t, perm.Review)
	require.True(t, perm.Review.Pending)
	require.True(t, perm.Review.Shadow)

	in := <-reviewer.calls
	require.Equal(t, []string{"please open a PR for this"}, in.UserMessages)
	require.Equal(t, "gh pr create", in.Input)
	require.Equal(t, "escalate", in.Bouncer.Outcome)

	close(reviewer.release)
	update := waitUpdate(t, h.events)
	require.Equal(t, perm.ID, update.ID)
	require.False(t, update.Review.Pending)
	require.Equal(t, "allow", update.Review.Effect)
	require.True(t, update.Review.QuoteVerified)

	h.svc.Grant(update)
	r := waitResult(t, done)
	require.NoError(t, r.err)
	require.True(t, r.result.Granted, "shadow review never changes the outcome")

	d := waitDecision(t, h.rec)
	require.Equal(t, DecisionSourceHuman, d.DecidedBy)
	rec := h.assessment(t, d)
	require.NotNil(t, rec.Review)
	require.Equal(t, "shadow", rec.Review.Mode)
	require.Equal(t, "small", rec.Review.Model)
	require.Equal(t, "allow", rec.Review.Verdict)
	require.Equal(t, "allow", rec.Review.Effect)
	require.True(t, rec.Review.QuoteVerified)
}

func TestReviewRecordedAfterHumanAnswersFirst(t *testing.T) {
	t.Parallel()
	reviewer := &fakeReviewer{
		opinion: ReviewOpinion{Verdict: ReviewAllow, Quote: "something the user never said"},
		release: make(chan struct{}),
	}
	h := newBouncerHarness(t, &fakeBouncer{outcome: AssessDeny, reason: "destructive=0.95"}, BouncerEnforce, nil, nil,
		reviewOption(reviewer, "tidy up the build directory"))
	done := requestAsync(testCtx(t), h.svc, h.req("call", "rm -rf /"))

	perm := waitPrompt(t, h.events)
	h.svc.Deny(perm, "")
	r := waitResult(t, done)
	require.NoError(t, r.err)
	require.False(t, r.result.Granted)
	require.Empty(t, h.rec.snapshot(), "the decision waits for the review")

	close(reviewer.release)
	rec := h.assessment(t, waitDecision(t, h.rec))
	require.Equal(t, "deny", rec.Outcome)
	require.NotNil(t, rec.Review)
	require.False(t, rec.Review.QuoteVerified, "a quote the user never wrote is not verified")
	require.Equal(t, "escalate", rec.Review.Effect, "a deny never goes straight to allow")
}

func TestReviewErrorIsRecorded(t *testing.T) {
	t.Parallel()
	reviewer := &fakeReviewer{err: errors.New("model unavailable")}
	h := newBouncerHarness(t, &fakeBouncer{outcome: AssessEscalate}, BouncerEnforce, nil, nil, reviewOption(reviewer, "hi"))
	done := requestAsync(testCtx(t), h.svc, h.req("call", "make deploy"))
	perm := waitPrompt(t, h.events)
	h.svc.Grant(perm)
	require.NoError(t, waitResult(t, done).err)
	rec := h.assessment(t, waitDecision(t, h.rec))
	require.Equal(t, "model unavailable", rec.Review.Error)
	require.Equal(t, "escalate", rec.Review.Effect)
}

func TestReviewSkipped(t *testing.T) {
	t.Parallel()
	t.Run("bouncer allow in enforce", func(t *testing.T) {
		t.Parallel()
		reviewer := &fakeReviewer{calls: make(chan ReviewInput, 1)}
		h := newBouncerHarness(t, &fakeBouncer{outcome: AssessAllow}, BouncerEnforce, nil, nil, reviewOption(reviewer))
		r, err := h.svc.Request(testCtx(t), h.req("call", "go build"))
		require.NoError(t, err)
		require.True(t, r.Granted)
		require.Empty(t, reviewer.calls)
	})
	t.Run("bouncer allow in shadow", func(t *testing.T) {
		t.Parallel()
		reviewer := &fakeReviewer{calls: make(chan ReviewInput, 1)}
		h := newBouncerHarness(t, &fakeBouncer{outcome: AssessAllow}, BouncerShadow, nil, nil, reviewOption(reviewer))
		done := requestAsync(testCtx(t), h.svc, h.req("call", "go build"))
		perm := waitPrompt(t, h.events)
		require.Nil(t, perm.Review)
		h.svc.Grant(perm)
		require.NoError(t, waitResult(t, done).err)
		require.Empty(t, reviewer.calls)
		require.Nil(t, h.assessment(t, h.rec.snapshot()[0]).Review)
	})
	t.Run("reviewer off", func(t *testing.T) {
		t.Parallel()
		reviewer := &fakeReviewer{calls: make(chan ReviewInput, 1)}
		h := newBouncerHarness(t, &fakeBouncer{outcome: AssessEscalate}, BouncerEnforce, nil, nil,
			WithReviewer(ReviewOptions{Reviewer: reviewer, Intent: fakeIntent{}, Mode: ReviewOff}))
		done := requestAsync(testCtx(t), h.svc, h.req("call", "make deploy"))
		perm := waitPrompt(t, h.events)
		require.Nil(t, perm.Review)
		h.svc.Grant(perm)
		require.NoError(t, waitResult(t, done).err)
		require.Empty(t, reviewer.calls)
	})
}

func TestReviewCancelledWithRequest(t *testing.T) {
	t.Parallel()
	reviewer := &fakeReviewer{release: make(chan struct{})}
	h := newBouncerHarness(t, &fakeBouncer{outcome: AssessEscalate}, BouncerEnforce, nil, nil, reviewOption(reviewer, "hi"))
	ctx, cancel := context.WithCancel(testCtx(t))
	done := requestAsync(ctx, h.svc, h.req("call", "make deploy"))
	waitPrompt(t, h.events)
	cancel()
	require.ErrorIs(t, waitResult(t, done).err, context.Canceled)
	d := waitDecision(t, h.rec)
	require.Equal(t, VerdictCancelled, d.Verdict)
	require.NotEmpty(t, h.assessment(t, d).Review.Error)
}
