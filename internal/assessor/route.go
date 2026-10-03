package assessor

import (
	"errors"
	"fmt"
	"math"

	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/systemone"
)

// maxSeverity is the highest severity score in the battery.
const maxSeverity = 3

// Thresholds control routing. Probabilities are 0-1; severity is 0-3.
type Thresholds struct {
	EscalateAt       float64 // Default 0.35.
	DenyAt           float64 // Default 0.9.
	SeverityEscalate float64 // Default 2.0.
	UserRequestedAt  float64 // Default 0.7.
}

// DefaultThresholds returns the conservative default thresholds.
func DefaultThresholds() Thresholds {
	return Thresholds{EscalateAt: 0.35, DenyAt: 0.9, SeverityEscalate: 2.0, UserRequestedAt: 0.7}
}

// Validate rejects out-of-range or inconsistent thresholds.
func (t Thresholds) Validate() error {
	probs := []struct {
		name string
		v    float64
	}{
		{"escalate_at", t.EscalateAt},
		{"deny_at", t.DenyAt},
		{"user_requested_at", t.UserRequestedAt},
	}
	for _, p := range probs {
		if !finite(p.v) || p.v < 0 || p.v > 1 {
			return fmt.Errorf("%s must be between 0 and 1, got %v", p.name, p.v)
		}
	}
	if !finite(t.SeverityEscalate) || t.SeverityEscalate < 0 || t.SeverityEscalate > maxSeverity {
		return fmt.Errorf("severity_escalate must be between 0 and %d, got %v", maxSeverity, t.SeverityEscalate)
	}
	if t.EscalateAt >= t.DenyAt {
		return errors.New("escalate_at must be less than deny_at")
	}
	return nil
}

func (t Thresholds) asMap() map[string]float64 {
	return map[string]float64{
		"escalate_at":       t.EscalateAt,
		"deny_at":           t.DenyAt,
		"severity_escalate": t.SeverityEscalate,
		"user_requested_at": t.UserRequestedAt,
	}
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
	reason := fmt.Sprintf("%s=%.2f severity=%.1f", topName, top, sev)
	switch {
	case top >= th.DenyAt && userReq < th.UserRequestedAt:
		return permission.AssessDeny, reason
	case top >= th.EscalateAt || sev >= th.SeverityEscalate:
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
	for _, q := range HazardQuestions {
		v, ok := value(answers[q].Noul)
		switch {
		case !ok:
		case v >= th.DenyAt:
			out[q] = permission.TriggerDeny
		case v >= th.EscalateAt:
			out[q] = permission.TriggerEscalate
		}
	}
	if v, ok := value(answers[QSeverity].Score); ok && v >= th.SeverityEscalate {
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
