package permission

import (
	"encoding/json"
	"slices"

	"github.com/Broderick-Westrope/anvil/internal/pubsub"
)

// DecisionSource identifies which layer of the permission pipeline
// resolved a request.
type DecisionSource string

const (
	DecisionSourceYolo         DecisionSource = "yolo"
	DecisionSourceHook         DecisionSource = "hook"
	DecisionSourceAutoSession  DecisionSource = "auto_session"
	DecisionSourceRule         DecisionSource = "rule"
	DecisionSourceSessionRule  DecisionSource = "session_rule"
	DecisionSourceSessionGrant DecisionSource = "session_grant"
	DecisionSourceAssessor     DecisionSource = "assessor"
	DecisionSourceHuman        DecisionSource = "human"
)

// Verdict is the final outcome recorded for a request.
type Verdict string

const (
	VerdictAllow     Verdict = "allow"
	VerdictDeny      Verdict = "deny"
	VerdictCancelled Verdict = "cancelled"
)

// Decision is one resolved permission request, as recorded in the
// decision log.
type Decision struct {
	SessionID     string
	ToolCallID    string
	ToolName      string
	Action        string
	Input         string
	InputSegments []string
	WorkingDir    string
	DecidedBy     DecisionSource
	Verdict       Verdict
	MatchedRule   string
	Assessment    json.RawMessage
}

// DecisionRecorder must not block the caller on persistence.
type DecisionRecorder interface {
	Record(d Decision)
}

// AssessmentSchemaVersion changes with the schema, battery, or routing so
// stats never combines incomparable assessments.
const AssessmentSchemaVersion = 1

// AssessmentRecord is the JSON stored in the decision log's assessment
// column. Field names are a stable contract between writers and readers.
type AssessmentRecord struct {
	SchemaVersion  int                `json:"schema_version"`
	BatteryVersion string             `json:"battery_version"`
	Mode           string             `json:"mode"`
	Model          string             `json:"model"`
	Outcome        string             `json:"outcome"`
	Reason         string             `json:"reason,omitempty"`
	SkipReason     string             `json:"skip_reason,omitempty"`
	Nouls          map[string]float64 `json:"nouls,omitempty"`
	Severity       *float64           `json:"severity,omitempty"`
	Thresholds     map[string]float64 `json:"thresholds,omitempty"`
	InputTokens    int                `json:"input_tokens"`
	OutputTokens   int                `json:"output_tokens"`
	LatencyMS      int64              `json:"latency_ms"`
	Error          string             `json:"error,omitempty"`
}

// Option configures a permission service.
type Option func(*permissionService)

// WithDecisionRecorder sets the recorder that receives every decision.
func WithDecisionRecorder(r DecisionRecorder) Option {
	return func(s *permissionService) { s.recorder = r }
}

func (s *permissionService) record(opts CreatePermissionRequest, src DecisionSource, verdict Verdict, matchedRule string, assessment json.RawMessage) {
	if s.recorder == nil {
		return
	}
	s.recorder.Record(Decision{
		SessionID:     opts.SessionID,
		ToolCallID:    opts.ToolCallID,
		ToolName:      opts.ToolName,
		Action:        opts.Action,
		Input:         opts.Input,
		InputSegments: slices.Clone(opts.InputSegments),
		WorkingDir:    s.workingDir,
		DecidedBy:     src,
		Verdict:       verdict,
		MatchedRule:   matchedRule,
		Assessment:    slices.Clone(assessment),
	})
}

func (s *permissionService) finish(opts CreatePermissionRequest, src DecisionSource, verdict Verdict, matchedRule string, assessment json.RawMessage, reason string) RequestResult {
	granted := verdict == VerdictAllow
	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		ToolCallID: opts.ToolCallID,
		Granted:    granted,
		Denied:     verdict == VerdictDeny,
		Reason:     reason,
	})
	s.record(opts, src, verdict, matchedRule, assessment)
	return RequestResult{Granted: granted, Reason: reason}
}
