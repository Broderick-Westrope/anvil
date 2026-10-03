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
	breaker          breaker       // 3 consecutive failures → open 60s, then one probe.
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
	allowed, probe := a.breaker.allow(a.now())
	if !allowed {
		return skipped(rec, skipUnavailable), nil
	}

	select {
	case a.sem <- struct{}{}:
	default:
		select {
		case a.sem <- struct{}{}:
		case <-ctx.Done():
			if probe {
				a.breaker.release()
			}
			return skipped(rec, skipBusy), nil
		}
	}
	defer func() { <-a.sem }()

	start := a.now()
	resp, err := a.Client.Evaluate(ctx, state, Battery(a.SendUserMessages))
	rec.LatencyMS = a.now().Sub(start).Milliseconds()
	if err != nil {
		// A caller cancelling is not an outage; a timeout is.
		if errors.Is(err, context.Canceled) {
			if probe {
				a.breaker.release()
			}
		} else {
			a.breaker.fail(a.now(), probe)
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
// consecutive failures. Once the cooldown ends it is half-open: a single
// probe call is let through while others are turned away. A successful
// probe closes the breaker; a failed one reopens it for another cooldown.
type breaker struct {
	mu        sync.Mutex
	failures  int
	openUntil time.Time
	probing   bool
}

// allow reports whether a call may go ahead and whether it is the
// half-open probe. A probe must end with fail, succeed, or release.
func (b *breaker) allow(now time.Time) (allowed, probe bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < breakerThreshold {
		return true, false
	}
	if now.Before(b.openUntil) || b.probing {
		return false, false
	}
	b.probing = true
	return true, true
}

func (b *breaker) fail(now time.Time, probe bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if probe {
		b.probing = false
	}
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
	b.probing = false
}

// release ends a probe that finished without telling us anything about
// the classifier's health, such as one the caller cancelled.
func (b *breaker) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
}
