package bouncer

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/systemone"
)

// maxSeverity is the highest severity score in the battery.
const maxSeverity = 3

// Thresholds control routing. Probabilities are 0-1; severity is 0-3.
//
// A request escalates when any hazard reaches its own EscalateAt, when
// severity reaches SeverityEscalate, or when the top hazard reaches
// ConcernAt and severity reaches SeverityConcern together. The concern
// band lets a mid hazard through when a mistake would be cheap to undo.
type Thresholds struct {
	EscalateAt       map[string]float64 // Per hazard axis; see DefaultThresholds.
	ConcernAt        float64            // Default 0.35.
	SeverityConcern  float64            // Default 1.5.
	DenyAt           float64            // Default 0.9.
	SeverityEscalate float64            // Default 2.0.
	UserRequestedAt  float64            // Default 0.7.
}

// DefaultThresholds returns the default thresholds. They were tuned by
// replaying the decision log; see TUNING.md.
func DefaultThresholds() Thresholds {
	return Thresholds{
		EscalateAt: map[string]float64{
			QDestructive:  0.7,
			QExfiltration: 0.6,
			QCredentials:  0.6,
			QRemoteExec:   0.7,
			QSharedInfra:  0.6,
		},
		ConcernAt:        0.35,
		SeverityConcern:  1.5,
		DenyAt:           0.9,
		SeverityEscalate: 2.0,
		UserRequestedAt:  0.7,
	}
}

// Validate rejects out-of-range or inconsistent thresholds.
func (t Thresholds) Validate() error {
	for q := range t.EscalateAt {
		if !slices.Contains(HazardQuestions, q) {
			return fmt.Errorf("escalate_at has unknown axis %q", q)
		}
	}
	probs := []struct {
		name string
		v    float64
	}{
		{"concern_at", t.ConcernAt},
		{"deny_at", t.DenyAt},
		{"user_requested_at", t.UserRequestedAt},
	}
	for _, q := range HazardQuestions {
		v, ok := t.EscalateAt[q]
		if !ok {
			return fmt.Errorf("escalate_at is missing axis %q", q)
		}
		probs = append(probs, struct {
			name string
			v    float64
		}{"escalate_at." + q, v})
	}
	for _, p := range probs {
		if !finite(p.v) || p.v < 0 || p.v > 1 {
			return fmt.Errorf("%s must be between 0 and 1, got %v", p.name, p.v)
		}
	}
	sevs := []struct {
		name string
		v    float64
	}{
		{"severity_escalate", t.SeverityEscalate},
		{"severity_concern", t.SeverityConcern},
	}
	for _, s := range sevs {
		if !finite(s.v) || s.v < 0 || s.v > maxSeverity {
			return fmt.Errorf("%s must be between 0 and %d, got %v", s.name, maxSeverity, s.v)
		}
	}
	for _, q := range HazardQuestions {
		if t.EscalateAt[q] >= t.DenyAt {
			return fmt.Errorf("escalate_at for %s must be less than deny_at", q)
		}
	}
	if t.ConcernAt >= t.DenyAt {
		return errors.New("concern_at must be less than deny_at")
	}
	return nil
}

func (t Thresholds) asMap() map[string]float64 {
	m := map[string]float64{
		"concern_at":        t.ConcernAt,
		"severity_concern":  t.SeverityConcern,
		"deny_at":           t.DenyAt,
		"severity_escalate": t.SeverityEscalate,
		"user_requested_at": t.UserRequestedAt,
	}
	for q, v := range t.EscalateAt {
		m["escalate_at."+q] = v
	}
	return m
}

// Route maps validated answers to an outcome and a short reason. Any
// missing or non-finite answer escalates.
func Route(answers map[string]systemone.Answer, th Thresholds) (permission.AssessOutcome, string) {
	topName, top := "", -1.0
	for _, q := range HazardQuestions {
		v, ok := value(answers[q].Noul)
		if !ok {
			return permission.AssessEscalate, "missing " + q + " answer"
		}
		if v > top {
			topName, top = q, v
		}
	}
	sev, ok := value(answers[QSeverity].Score)
	if !ok {
		return permission.AssessEscalate, "missing " + QSeverity + " answer"
	}
	userReq := 0.0
	if a, ok := answers[QUserRequested]; ok {
		if v, ok := value(a.Noul); ok {
			userReq = v
		}
	}

	// Name the hazard that drove the decision: the highest one that
	// crossed its own threshold, else the highest overall.
	crossedName, crossed := "", -1.0
	for _, q := range HazardQuestions {
		if v, _ := value(answers[q].Noul); v >= th.EscalateAt[q] && v > crossed {
			crossedName, crossed = q, v
		}
	}
	reasonName, reasonValue := topName, top
	if crossedName != "" && top < th.DenyAt {
		reasonName, reasonValue = crossedName, crossed
	}
	reason := fmt.Sprintf("%s=%.2f severity=%.1f", reasonName, reasonValue, sev)

	switch {
	case top >= th.DenyAt && userReq < th.UserRequestedAt:
		return permission.AssessDeny, reason
	case crossedName != "",
		sev >= th.SeverityEscalate,
		top >= th.ConcernAt && sev >= th.SeverityConcern:
		return permission.AssessEscalate, reason
	default:
		return permission.AssessAllow, reason
	}
}

func value(p *float64) (float64, bool) {
	if p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) {
		return 0, false
	}
	return *p, true
}

// Triggers reports which answers crossed a routing threshold and the
// effect each had, so a reviewer can see why a request was routed as it
// was. It mirrors the comparisons in Route.
func Triggers(answers map[string]systemone.Answer, th Thresholds) map[string]string {
	out := map[string]string{}
	sev, sevOK := value(answers[QSeverity].Score)
	concern := sevOK && sev >= th.SeverityConcern
	concerned := false
	for _, q := range HazardQuestions {
		v, ok := value(answers[q].Noul)
		switch {
		case !ok:
		case v >= th.DenyAt:
			out[q] = permission.TriggerDeny
		case v >= th.EscalateAt[q]:
			out[q] = permission.TriggerEscalate
		case concern && v >= th.ConcernAt:
			out[q] = permission.TriggerEscalate
			concerned = true
		}
	}
	if sevOK && (sev >= th.SeverityEscalate || concerned) {
		out[QSeverity] = permission.TriggerEscalate
	}
	if a, ok := answers[QUserRequested]; ok {
		if v, ok := value(a.Noul); ok && v >= th.UserRequestedAt {
			out[QUserRequested] = permission.TriggerMitigate
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
