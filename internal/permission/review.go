package permission

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Broderick-Westrope/anvil/internal/pubsub"
)

// Reviewer gives a second opinion on a request the bouncer is sending to
// the human. It sees the user's own messages and the tool call, so it can
// spot an action the user explicitly asked for that the bouncer, which
// judges the call mostly in isolation, flagged anyway.
type Reviewer interface {
	Review(ctx context.Context, in ReviewInput) (ReviewOpinion, error)
}

// ReviewInput is what the reviewer may see. Like [AssessInput] it excludes
// tool output and the agent's own messages, which the agent controls.
type ReviewInput struct {
	ToolName, Action, Description string
	Input, Path, WorkingDir       string
	Diff, ArgsJSON                string
	// UserMessages are the most recent user messages, oldest first.
	UserMessages []string
	// Bouncer is the verdict that sent the request to the human.
	Bouncer *AssessmentSummary
}

// ReviewVerdict is the reviewer's raw opinion of a request.
type ReviewVerdict string

const (
	ReviewAllow    ReviewVerdict = "allow"
	ReviewEscalate ReviewVerdict = "escalate"
	ReviewDeny     ReviewVerdict = "deny"
)

// ReviewOpinion is the reviewer's answer before any guardrail is applied.
type ReviewOpinion struct {
	Verdict ReviewVerdict
	// Quote is the user's words the reviewer says authorise the call. An
	// allow only counts when the quote really appears in a user message.
	Quote  string
	Reason string
	Model  string
}

// ReviewMode controls what the reviewer's opinion is used for.
type ReviewMode string

const (
	// ReviewOff disables the reviewer.
	ReviewOff ReviewMode = "off"
	// ReviewShadow shows and logs the reviewer's opinion, but never acts
	// on it.
	ReviewShadow ReviewMode = "shadow"
)

// ReviewOptions configures the reviewer.
type ReviewOptions struct {
	Reviewer Reviewer
	// Intent supplies the user's messages. The reviewer is skipped
	// without it.
	Intent  IntentSource
	Mode    ReviewMode
	Timeout time.Duration
}

// WithReviewer sets the reviewer consulted when the bouncer sends a
// request to the human.
func WithReviewer(o ReviewOptions) Option {
	return func(s *permissionService) { s.review = o }
}

const (
	// defaultReviewTimeout bounds a review. The human is already looking
	// at the prompt, so this only limits how long a stalled call lingers.
	defaultReviewTimeout = 45 * time.Second
	// reviewUserMessages is how many recent user messages the reviewer
	// sees. It is more than the bouncer gets because an instruction to
	// open a PR is often given several turns before the push.
	reviewUserMessages = 5
	// reviewAllowSeverityBelow is the severity at or above which the
	// reviewer may never approve a call, whatever the user said.
	reviewAllowSeverityBelow = 2.5
	// minQuoteRunes stops a fragment like "yes" or "ok" from counting as
	// authorisation.
	minQuoteRunes = 8
)

// ReviewRecord is the reviewer's part of an [AssessmentRecord]. Field
// names are a stable contract between writers and readers.
type ReviewRecord struct {
	Mode          string `json:"mode"`
	Model         string `json:"model,omitempty"`
	Verdict       string `json:"verdict,omitempty"`
	Quote         string `json:"quote,omitempty"`
	QuoteVerified bool   `json:"quote_verified"`
	Reason        string `json:"reason,omitempty"`
	// Effect is what the reviewer would do to the prompt once the
	// guardrails are applied: allow it, keep it as an escalation, or keep
	// it as a deny.
	Effect    string `json:"effect,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// ReviewSummary is the reviewer's opinion on a prompted request, in a form
// the UI can show next to the bouncer's verdict.
type ReviewSummary struct {
	Shadow        bool   `json:"shadow,omitempty"`
	Pending       bool   `json:"pending,omitempty"`
	Effect        string `json:"effect,omitempty"`
	Quote         string `json:"quote,omitempty"`
	QuoteVerified bool   `json:"quote_verified,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Error         string `json:"error,omitempty"`
}

func (r *ReviewRecord) summary() *ReviewSummary {
	return &ReviewSummary{
		Shadow:        r.Mode == string(ReviewShadow),
		Effect:        r.Effect,
		Quote:         r.Quote,
		QuoteVerified: r.QuoteVerified,
		Reason:        r.Reason,
		Error:         r.Error,
	}
}

// reviewEffect applies the guardrails to the reviewer's opinion. The
// reviewer can only move a request one step towards allowing it: a deny
// can drop to an ordinary escalation, and an escalation can become an
// allow only when the reviewer quotes the user asking for it and the call
// is not severe. It can never raise a request, and never take a deny
// straight to an allow.
func reviewEffect(bouncerOutcome string, severity *float64, op ReviewOpinion, quoteVerified bool) ReviewVerdict {
	switch bouncerOutcome {
	case AssessDeny.String():
		if op.Verdict == ReviewDeny {
			return ReviewDeny
		}
		return ReviewEscalate
	default:
		if op.Verdict == ReviewAllow && quoteVerified && severity != nil && *severity < reviewAllowSeverityBelow {
			return ReviewAllow
		}
		return ReviewEscalate
	}
}

// verifyQuote reports whether quote appears in one of msgs, ignoring case,
// whitespace, and the quotation marks a model tends to wrap it in.
func verifyQuote(quote string, msgs []string) bool {
	q := normalizeQuote(quote)
	if len([]rune(q)) < minQuoteRunes {
		return false
	}
	return slices.ContainsFunc(msgs, func(m string) bool {
		return strings.Contains(normalizeQuote(m), q)
	})
}

func normalizeQuote(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("\"'`“”‘’….", r)
	})
}

// shouldReview reports whether a prompt with this bouncer verdict gets a
// second opinion. Only verdicts that flagged the call are reviewed; a
// shadow-mode allow prompts for every call and has nothing to explain.
func (s *permissionService) shouldReview(sum *AssessmentSummary) bool {
	if s.review.Reviewer == nil || s.review.Intent == nil || sum == nil {
		return false
	}
	if s.review.Mode != ReviewShadow {
		return false
	}
	return sum.Outcome == AssessEscalate.String() || sum.Outcome == AssessDeny.String()
}

// pendingReview is a review running alongside a human prompt.
type pendingReview struct {
	mu        sync.Mutex
	done      chan struct{}
	record    *ReviewRecord
	published *PermissionRequest // The prompt, once published.
	cancel    context.CancelFunc
}

// startReview runs the reviewer in the background so it never delays the
// prompt. The review outlives ctx so it can still be recorded when the
// human answers first; cancel stops it.
func (s *permissionService) startReview(ctx context.Context, opts CreatePermissionRequest, sum *AssessmentSummary, details json.RawMessage) *pendingReview {
	timeout := s.review.Timeout
	if timeout <= 0 {
		timeout = defaultReviewTimeout
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	r := &pendingReview{done: make(chan struct{}), cancel: cancel}
	var severity *float64
	var rec AssessmentRecord
	if json.Unmarshal(details, &rec) == nil {
		severity = rec.Severity
	}
	go func() {
		defer cancel()
		result := s.runReview(rctx, opts, sum, severity)
		r.mu.Lock()
		r.record = &result
		close(r.done)
		perm := r.published
		r.mu.Unlock()
		if perm == nil {
			return
		}
		if _, ok := s.pendingRequests.Get(perm.ID); !ok {
			return
		}
		update := *perm
		update.Review = result.summary()
		s.Publish(pubsub.UpdatedEvent, update)
	}()
	return r
}

// stop cancels the review. It is safe on a nil review.
func (r *pendingReview) stop() {
	if r != nil {
		r.cancel()
	}
}

// publishWithReview publishes the prompt with whatever the reviewer has said so far,
// so a review that finishes later is sent as an update.
func (s *permissionService) publishWithReview(perm *PermissionRequest, r *pendingReview) {
	if r == nil {
		s.Publish(pubsub.CreatedEvent, *perm)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.record != nil {
		perm.Review = r.record.summary()
	} else {
		perm.Review = &ReviewSummary{Shadow: s.review.Mode == ReviewShadow, Pending: true}
	}
	published := *perm
	r.published = &published
	s.Publish(pubsub.CreatedEvent, *perm)
}

// recordReviewed records a human decision with the review attached. When
// the review is still running, it is recorded once the review settles.
func (s *permissionService) recordReviewed(opts CreatePermissionRequest, verdict Verdict, details json.RawMessage, r *pendingReview) {
	if r == nil {
		s.record(opts, DecisionSourceHuman, verdict, "", details)
		return
	}
	select {
	case <-r.done:
		s.record(opts, DecisionSourceHuman, verdict, "", withReview(details, r.record))
	default:
		go func() {
			<-r.done
			s.record(opts, DecisionSourceHuman, verdict, "", withReview(details, r.record))
		}()
	}
}

func (s *permissionService) runReview(ctx context.Context, opts CreatePermissionRequest, sum *AssessmentSummary, severity *float64) ReviewRecord {
	rec := ReviewRecord{Mode: string(s.review.Mode), Effect: string(ReviewEscalate)}
	if sum != nil && sum.Outcome == AssessDeny.String() {
		rec.Effect = string(ReviewDeny)
	}
	msgs, err := s.review.Intent.RecentUserMessages(ctx, opts.SessionID, reviewUserMessages)
	if err != nil {
		rec.Error = "load user messages: " + err.Error()
		return rec
	}
	in := ReviewInput{
		ToolName:     opts.ToolName,
		Action:       opts.Action,
		Description:  opts.Description,
		Input:        opts.Input,
		Path:         opts.Path,
		WorkingDir:   s.workingDir,
		Diff:         opts.Diff,
		ArgsJSON:     opts.ArgsJSON,
		UserMessages: msgs,
		Bouncer:      sum,
	}
	start := time.Now()
	op, err := s.callReviewer(ctx, in)
	rec.LatencyMS = time.Since(start).Milliseconds()
	rec.Model = op.Model
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("Permission reviewer failed", "tool", opts.ToolName, "error", err)
		}
		rec.Error = err.Error()
		return rec
	}
	rec.Verdict = string(op.Verdict)
	rec.Quote = op.Quote
	rec.Reason = op.Reason
	rec.QuoteVerified = op.Quote != "" && verifyQuote(op.Quote, msgs)
	rec.Effect = string(reviewEffect(sum.Outcome, severity, op, rec.QuoteVerified))
	return rec
}

func (s *permissionService) callReviewer(ctx context.Context, in ReviewInput) (op ReviewOpinion, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("reviewer panicked: %v", r)
		}
	}()
	op, err = s.review.Reviewer.Review(ctx, in)
	if err != nil {
		return op, err
	}
	switch op.Verdict {
	case ReviewAllow, ReviewEscalate, ReviewDeny:
		return op, nil
	default:
		return op, fmt.Errorf("unknown review verdict %q", op.Verdict)
	}
}

// withReview attaches a review to a marshalled AssessmentRecord. Details
// that don't decode are returned unchanged.
func withReview(details json.RawMessage, review *ReviewRecord) json.RawMessage {
	if len(details) == 0 || review == nil {
		return details
	}
	var rec AssessmentRecord
	if err := json.Unmarshal(details, &rec); err != nil {
		return details
	}
	rec.Review = review
	out, err := json.Marshal(rec)
	if err != nil {
		return details
	}
	return out
}
