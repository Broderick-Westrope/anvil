package assessor

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
		{"one hazard at 0.5", answers(map[string]float64{QExfiltration: 0.5}, 0.5, ptr(0.1)), permission.AssessEscalate},
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
}

func TestThresholdsValidate(t *testing.T) {
	t.Parallel()

	require.NoError(t, DefaultThresholds().Validate())

	bad := []struct {
		name   string
		mutate func(*Thresholds)
	}{
		{"escalate equals deny", func(th *Thresholds) { th.EscalateAt = th.DenyAt }},
		{"escalate above deny", func(th *Thresholds) { th.EscalateAt = 0.95 }},
		{"negative escalate", func(th *Thresholds) { th.EscalateAt = -0.1 }},
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
		{"hazard escalates", answers(map[string]float64{QExfiltration: 0.5}, 0.5, ptr(0.1)), map[string]string{QExfiltration: permission.TriggerEscalate}},
		{
			"deny, escalate, severity, and mitigation together",
			answers(map[string]float64{QDestructive: 0.95, QSharedInfra: 0.4}, 2.5, ptr(0.9)),
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
