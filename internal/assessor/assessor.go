package assessor

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/permission"
)

const (
	maxConcurrent    = 4
	breakerThreshold = 3
	breakerCooldown  = 60 * time.Second
)

// Record outcomes for skipped and failed assessments.
const (
	outcomeSkipped = "skipped"
	outcomeError   = "error"
)

// Skip reasons set by the Assessor itself rather than BuildState.
const (
	skipUnavailable = "assessor unavailable"
	skipBusy        = "assessor busy"
)

// Assessor implements permission.Assessor with a System One client.
type Assessor struct {
	Client           *Client
	Thresholds       Thresholds
	SendUserMessages bool
	sem              chan struct{} // Cap 4: bounded concurrency.
	breaker          breaker       // 3 consecutive failures → open 60s.
	now              func() time.Time
}

var _ permission.Assessor = (*Assessor)(nil)

// New returns an Assessor. Callers should validate th first.
func New(c *Client, th Thresholds, sendUserMessages bool) *Assessor {
	return &Assessor{
		Client:           c,
		Thresholds:       th,
		SendUserMessages: sendUserMessages,
		sem:              make(chan struct{}, maxConcurrent),
		now:              time.Now,
	}
}

// Assess classifies one request. Ineligible inputs, an open breaker, and
// a full concurrency cap escalate without a network call. Details is
// always a marshalled permission.AssessmentRecord.
func (a *Assessor) Assess(ctx context.Context, in permission.AssessInput) (permission.Assessment, error) {
	rec := permission.AssessmentRecord{
		SchemaVersion:  permission.AssessmentSchemaVersion,
		BatteryVersion: BatteryVersion,
		Model:          a.Client.Model,
		Thresholds:     a.Thresholds.asMap(),
	}

	state, skip := BuildState(in, a.SendUserMessages)
	if skip != "" {
		return skipped(rec, skip), nil
	}
	if a.breaker.isOpen(a.now()) {
		return skipped(rec, skipUnavailable), nil
	}

	select {
	case a.sem <- struct{}{}:
	default:
		select {
		case a.sem <- struct{}{}:
		case <-ctx.Done():
			return skipped(rec, skipBusy), nil
		}
	}
	defer func() { <-a.sem }()

	start := a.now()
	resp, err := a.Client.Evaluate(ctx, state, Battery(a.SendUserMessages))
	rec.LatencyMS = a.now().Sub(start).Milliseconds()
	if err != nil {
		// A caller cancelling is not an outage; a timeout is.
		if !errors.Is(err, context.Canceled) {
			a.breaker.fail(a.now())
		}
		rec.Outcome = outcomeError
		rec.Error = err.Error()
		return permission.Assessment{Outcome: permission.AssessEscalate, Reason: "assessor error", Details: marshal(rec)}, err
	}
	a.breaker.succeed()

	outcome, reason := Route(resp.Answers, a.Thresholds)
	rec.Model = resp.Model
	rec.Outcome = outcomeName(outcome)
	rec.Reason = reason
	rec.Nouls = map[string]float64{}
	for id, ans := range resp.Answers {
		if v, ok := value(ans.Noul); ok && ans.Type == "noul" {
			rec.Nouls[id] = v
		}
	}
	if v, ok := value(resp.Answers[QSeverity].Score); ok {
		rec.Severity = &v
	}
	rec.InputTokens = resp.Usage.InputTokens
	rec.OutputTokens = resp.Usage.OutputTokens
	return permission.Assessment{Outcome: outcome, Reason: reason, Details: marshal(rec)}, nil
}

func skipped(rec permission.AssessmentRecord, reason string) permission.Assessment {
	rec.Outcome = outcomeSkipped
	rec.SkipReason = reason
	return permission.Assessment{Outcome: permission.AssessEscalate, Reason: reason, Details: marshal(rec)}
}

func marshal(rec permission.AssessmentRecord) json.RawMessage {
	raw, err := json.Marshal(rec)
	if err != nil {
		return nil
	}
	return raw
}

// breaker stops calling the classifier for a cooldown after repeated
// consecutive failures. Once the cooldown ends, a single further failure
// reopens it until a success resets the count.
type breaker struct {
	mu        sync.Mutex
	failures  int
	openUntil time.Time
}

func (b *breaker) isOpen(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return now.Before(b.openUntil)
}

func (b *breaker) fail(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.failures >= breakerThreshold {
		b.openUntil = now.Add(breakerCooldown)
	}
}

func (b *breaker) succeed() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openUntil = time.Time{}
}
