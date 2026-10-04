package bouncer

import (
	"math"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/systemone"
	"github.com/stretchr/testify/require"
)

func answers(hazard map[string]float64, severity float64, userReq *float64) map[string]systemone.Answer {
	out := map[string]systemone.Answer{}
	for _, q := range HazardQuestions {
		v := hazard[q]
		out[q] = systemone.Answer{Type: "noul", Noul: &v}
	}
	out[QSeverity] = systemone.Answer{Type: "score", Score: &severity}
	if userReq != nil {
		out[QUserRequested] = systemone.Answer{Type: "noul", Noul: userReq}
	}
	return out
}

func ptr(v float64) *float64 { return &v }

func TestRoute(t *testing.T) {
	t.Parallel()

	th := DefaultThresholds()
	tests := []struct {
		name    string
		answers map[string]systemone.Answer
		want    permission.AssessOutcome
	}{
		{"all low", answers(map[string]float64{QDestructive: 0.1}, 0.5, ptr(0.1)), permission.AssessAllow},
		{"mid hazard low severity", answers(map[string]float64{QExfiltration: 0.5}, 0.5, ptr(0.1)), permission.AssessAllow},
		{"mid hazard with severity", answers(map[string]float64{QExfiltration: 0.5}, 1.5, ptr(0.1)), permission.AssessEscalate},
		{"below concern band with severity", answers(map[string]float64{QExfiltration: 0.3}, 1.9, ptr(0.1)), permission.AssessAllow},
		{"destructive below its threshold", answers(map[string]float64{QDestructive: 0.65}, 1.0, ptr(0.1)), permission.AssessAllow},
		{"destructive at its threshold", answers(map[string]float64{QDestructive: 0.7}, 0.0, ptr(0.1)), permission.AssessEscalate},
		{"credentials at its threshold", answers(map[string]float64{QCredentials: 0.6}, 0.0, ptr(0.1)), permission.AssessEscalate},
		{"remote exec below its threshold", answers(map[string]float64{QRemoteExec: 0.65}, 1.0, ptr(0.1)), permission.AssessAllow},
		{"high hazard not requested", answers(map[string]float64{QDestructive: 0.95}, 3, ptr(0.2)), permission.AssessDeny},
		{"high hazard requested", answers(map[string]float64{QDestructive: 0.95}, 3, ptr(0.9)), permission.AssessEscalate},
		{"high hazard no user question", answers(map[string]float64{QSharedInfra: 0.95}, 3, nil), permission.AssessDeny},
		{"high severity low hazards", answers(map[string]float64{QDestructive: 0.1}, 2.5, ptr(0.1)), permission.AssessEscalate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, reason := Route(tt.answers, th)
			require.Equal(t, tt.want, got)
			require.Contains(t, reason, "severity=")
		})
	}

	t.Run("missing answer escalates", func(t *testing.T) {
		t.Parallel()
		a := answers(nil, 0, nil)
		delete(a, QCredentials)
		got, _ := Route(a, th)
		require.Equal(t, permission.AssessEscalate, got)
	})

	t.Run("reason names top hazard", func(t *testing.T) {
		t.Parallel()
		_, reason := Route(answers(map[string]float64{QRemoteExec: 0.6, QDestructive: 0.2}, 1, nil), th)
		require.Equal(t, "remote_exec=0.60 severity=1.0", reason)
	})

	t.Run("reason names the hazard that crossed its threshold", func(t *testing.T) {
		t.Parallel()
		got, reason := Route(answers(map[string]float64{QDestructive: 0.68, QCredentials: 0.62}, 0, nil), th)
		require.Equal(t, permission.AssessEscalate, got)
		require.Equal(t, "credentials=0.62 severity=0.0", reason)
	})

	t.Run("uniform escalate_at matches the old single threshold", func(t *testing.T) {
		t.Parallel()
		uniform := DefaultThresholds()
		for q := range uniform.EscalateAt {
			uniform.EscalateAt[q] = 0.35
		}
		got, _ := Route(answers(map[string]float64{QDestructive: 0.36}, 0, ptr(0.1)), uniform)
		require.Equal(t, permission.AssessEscalate, got)
	})
}

func TestThresholdsValidate(t *testing.T) {
	t.Parallel()

	require.NoError(t, DefaultThresholds().Validate())

	bad := []struct {
		name   string
		mutate func(*Thresholds)
	}{
		{"escalate equals deny", func(th *Thresholds) { th.EscalateAt[QDestructive] = th.DenyAt }},
		{"escalate above deny", func(th *Thresholds) { th.EscalateAt[QCredentials] = 0.95 }},
		{"negative escalate", func(th *Thresholds) { th.EscalateAt[QRemoteExec] = -0.1 }},
		{"missing axis", func(th *Thresholds) { delete(th.EscalateAt, QSharedInfra) }},
		{"unknown axis", func(th *Thresholds) { th.EscalateAt["vibes"] = 0.5 }},
		{"nil axes", func(th *Thresholds) { th.EscalateAt = nil }},
		{"concern equals deny", func(th *Thresholds) { th.ConcernAt = th.DenyAt }},
		{"negative concern", func(th *Thresholds) { th.ConcernAt = -0.1 }},
		{"severity concern 4", func(th *Thresholds) { th.SeverityConcern = 4 }},
		{"negative user requested", func(th *Thresholds) { th.UserRequestedAt = -1 }},
		{"deny above one", func(th *Thresholds) { th.DenyAt = 1.1 }},
		{"severity 4", func(th *Thresholds) { th.SeverityEscalate = 4 }},
		{"negative severity", func(th *Thresholds) { th.SeverityEscalate = -1 }},
		{"NaN", func(th *Thresholds) { th.DenyAt = math.NaN() }},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			th := DefaultThresholds()
			tt.mutate(&th)
			require.Error(t, th.Validate())
		})
	}
}

func TestTriggers(t *testing.T) {
	t.Parallel()

	th := DefaultThresholds()
	tests := []struct {
		name    string
		answers map[string]systemone.Answer
		want    map[string]string
	}{
		{"nothing crosses", answers(map[string]float64{QDestructive: 0.1}, 0.5, ptr(0.1)), nil},
		{"hazard escalates", answers(map[string]float64{QExfiltration: 0.6}, 0.5, ptr(0.1)), map[string]string{QExfiltration: permission.TriggerEscalate}},
		{"mid hazard alone", answers(map[string]float64{QExfiltration: 0.5}, 0.5, ptr(0.1)), nil},
		{
			"mid hazard with severity",
			answers(map[string]float64{QExfiltration: 0.5}, 1.5, ptr(0.1)),
			map[string]string{QExfiltration: permission.TriggerEscalate, QSeverity: permission.TriggerEscalate},
		},
		{"severity below escalate with no hazard", answers(nil, 1.8, nil), nil},
		{
			"deny, escalate, severity, and mitigation together",
			answers(map[string]float64{QDestructive: 0.95, QSharedInfra: 0.6}, 2.5, ptr(0.9)),
			map[string]string{
				QDestructive:   permission.TriggerDeny,
				QSharedInfra:   permission.TriggerEscalate,
				QSeverity:      permission.TriggerEscalate,
				QUserRequested: permission.TriggerMitigate,
			},
		},
		{"severity alone", answers(nil, 2.0, nil), map[string]string{QSeverity: permission.TriggerEscalate}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, Triggers(tt.answers, th))
		})
	}
}

func TestAxisNamesMatchPermission(t *testing.T) {
	t.Parallel()
	require.Equal(t, permission.UserRequestedAxis, QUserRequested)
	require.Equal(t, permission.SeverityAxis, QSeverity)
}
