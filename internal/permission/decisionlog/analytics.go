package decisionlog

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/permission/triage"
)

func LoadRecords(ctx context.Context, q db.Querier, since time.Time) ([]triage.Record, error) {
	rows, err := q.ListPermissionDecisionsSince(ctx, since.Unix())
	if err != nil {
		return nil, err
	}
	records := make([]triage.Record, 0, len(rows))
	for _, row := range rows {
		r := triage.Record{SessionID: row.SessionID, WorkingDir: row.WorkingDir, ToolName: row.ToolName, Input: row.Input, DecidedBy: row.DecidedBy, Verdict: row.Verdict}
		if err := json.Unmarshal([]byte(row.InputSegments), &r.InputSegments); err != nil {
			slog.Debug("Invalid permission decision segments", "id", row.ID, "error", err)
			r.InputSegments = nil
		}
		if a := parseAssessment(row); a != nil {
			for _, key := range []string{"destructive", "exfiltration", "credentials", "remote_exec", "shared_infra"} {
				if value, ok := a.Nouls[key]; ok && (r.MaxHazard == nil || value > *r.MaxHazard) {
					r.MaxHazard = &value
				}
			}
		}
		records = append(records, r)
	}
	return records, nil
}

func parseAssessment(row db.PermissionDecision) *permission.AssessmentRecord {
	if !row.Assessment.Valid {
		return nil
	}
	var a *permission.AssessmentRecord
	if err := json.Unmarshal([]byte(row.Assessment.String), &a); err != nil {
		slog.Debug("Invalid permission assessment", "id", row.ID, "error", err)
		return nil
	}
	return a
}

type VerdictMatrix map[string]map[string]int

type Comparisons struct {
	Samples int           `json:"samples"`
	Matrix  VerdictMatrix `json:"matrix"`
}

type Enforcement struct {
	Samples  int            `json:"samples"`
	Assessor map[string]int `json:"assessor"`
	Human    Comparisons    `json:"human"`
}

type UsageStats struct {
	Samples          int     `json:"samples"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	MeanInputTokens  float64 `json:"mean_input_tokens"`
	MeanOutputTokens float64 `json:"mean_output_tokens"`
	MeanLatencyMS    float64 `json:"mean_latency_ms"`
	P95LatencyMS     int64   `json:"p95_latency_ms"`
}

type AssessmentStats struct {
	SchemaVersion  int            `json:"schema_version"`
	BatteryVersion string         `json:"battery_version"`
	Total          int            `json:"total"`
	ByDecidedBy    map[string]int `json:"by_decided_by"`
	Shadow         Comparisons    `json:"shadow"`
	Enforce        Enforcement    `json:"enforce"`
	Errors         map[string]int `json:"errors"`
	Skips          map[string]int `json:"skips"`
	Usage          UsageStats     `json:"usage"`
}

type Stats struct {
	Total              int               `json:"total"`
	ByDecidedBy        map[string]int    `json:"by_decided_by"`
	WithoutAssessment  int               `json:"without_assessment"`
	InvalidAssessments int               `json:"invalid_assessments"`
	Groups             []AssessmentStats `json:"groups"`
}

func addComparison(c *Comparisons, outcome, verdict string) {
	c.Samples++
	if c.Matrix[outcome] == nil {
		c.Matrix[outcome] = map[string]int{}
	}
	c.Matrix[outcome][verdict]++
}

func ComputeStats(rows []db.PermissionDecision) Stats {
	stats := Stats{Total: len(rows), ByDecidedBy: map[string]int{}, Groups: []AssessmentStats{}}
	type version struct {
		schema  int
		battery string
	}
	type accumulator struct {
		stats     AssessmentStats
		latencies []int64
	}
	groups := map[version]*accumulator{}
	for _, row := range rows {
		stats.ByDecidedBy[row.DecidedBy]++
		a := parseAssessment(row)
		if a == nil {
			if row.Assessment.Valid && strings.TrimSpace(row.Assessment.String) != "null" {
				stats.InvalidAssessments++
			} else {
				stats.WithoutAssessment++
			}
			continue
		}
		key := version{a.SchemaVersion, a.BatteryVersion}
		g := groups[key]
		if g == nil {
			g = &accumulator{stats: AssessmentStats{SchemaVersion: key.schema, BatteryVersion: key.battery, ByDecidedBy: map[string]int{}, Shadow: Comparisons{Matrix: VerdictMatrix{}}, Enforce: Enforcement{Assessor: map[string]int{}, Human: Comparisons{Matrix: VerdictMatrix{}}}, Errors: map[string]int{}, Skips: map[string]int{}}}
			groups[key] = g
		}
		s := &g.stats
		s.Total++
		s.ByDecidedBy[row.DecidedBy]++
		if a.Mode == "shadow" && row.DecidedBy == string(permission.DecisionSourceHuman) {
			addComparison(&s.Shadow, a.Outcome, row.Verdict)
		}
		if a.Mode == "enforce" {
			s.Enforce.Samples++
			if row.DecidedBy == string(permission.DecisionSourceAssessor) {
				s.Enforce.Assessor[row.Verdict]++
			}
			if row.DecidedBy == string(permission.DecisionSourceHuman) {
				addComparison(&s.Enforce.Human, a.Outcome, row.Verdict)
			}
		}
		switch a.Outcome {
		case "error":
			prefix, _, _ := strings.Cut(a.Error, ":")
			s.Errors[cmp.Or(strings.TrimSpace(prefix), "unknown")]++
		case "skipped":
			s.Skips[cmp.Or(a.SkipReason, "unknown")]++
		case "allow", "escalate", "deny":
			s.Usage.Samples++
			s.Usage.InputTokens += int64(a.InputTokens)
			s.Usage.OutputTokens += int64(a.OutputTokens)
			s.Usage.MeanLatencyMS += float64(a.LatencyMS)
			g.latencies = append(g.latencies, a.LatencyMS)
		}
	}
	for _, g := range groups {
		u := &g.stats.Usage
		if u.Samples > 0 {
			u.MeanInputTokens = float64(u.InputTokens) / float64(u.Samples)
			u.MeanOutputTokens = float64(u.OutputTokens) / float64(u.Samples)
			u.MeanLatencyMS /= float64(u.Samples)
			slices.Sort(g.latencies)
			u.P95LatencyMS = g.latencies[int(math.Ceil(0.95*float64(len(g.latencies))))-1]
		}
		stats.Groups = append(stats.Groups, g.stats)
	}
	slices.SortFunc(stats.Groups, func(a, b AssessmentStats) int {
		return cmp.Or(cmp.Compare(a.SchemaVersion, b.SchemaVersion), cmp.Compare(a.BatteryVersion, b.BatteryVersion))
	})
	return stats
}
