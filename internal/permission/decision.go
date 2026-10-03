package permission

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

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
	DecisionSourceBouncer      DecisionSource = "bouncer"
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
	// Triggers maps each answer that crossed a routing threshold to the
	// effect it had: TriggerDeny, TriggerEscalate, or TriggerMitigate.
	Triggers     map[string]string `json:"triggers,omitempty"`
	InputTokens  int               `json:"input_tokens"`
	OutputTokens int               `json:"output_tokens"`
	LatencyMS    int64             `json:"latency_ms"`
	Error        string            `json:"error,omitempty"`
}

// Trigger effects recorded in AssessmentRecord.Triggers.
const (
	// TriggerDeny marks a hazard at or above the deny threshold.
	TriggerDeny = "deny"
	// TriggerEscalate marks a hazard or severity at or above the
	// escalation threshold.
	TriggerEscalate = "escalate"
	// TriggerMitigate marks a user-request signal strong enough to turn
	// a deny into an escalation.
	TriggerMitigate = "mitigate"
)

// AssessOutcome is the bouncer's routing decision for a request.
type AssessOutcome int

const (
	// AssessEscalate hands the request to the human. It is the zero value
	// so any unset or failed assessment asks the human.
	AssessEscalate AssessOutcome = iota
	// AssessAllow grants the request without a prompt.
	AssessAllow
	// AssessDeny blocks the request.
	AssessDeny
)

// AssessInput is what the bouncer may see. It excludes tool output.
type AssessInput struct {
	SessionID, ToolName, Action, Description string
	Input, Path, WorkingDir                  string
	Segments                                 []string
	// Content is the new file content for edits.
	Content string
	// Diff is the unified diff an edit would apply.
	Diff               string
	ArgsJSON           string
	RecentUserMessages []string
}

// Assessment is the result of assessing one request.
type Assessment struct {
	Outcome AssessOutcome
	Reason  string
	Details json.RawMessage // Marshalled AssessmentRecord.
}

// Bouncer judges whether a request needs a human.
type Bouncer interface {
	Assess(ctx context.Context, in AssessInput) (Assessment, error)
}

// IntentSource returns the most recent user messages on the active
// branch, oldest first, resolving subagent sessions to their parent.
type IntentSource interface {
	RecentUserMessages(ctx context.Context, sessionID string, n int) ([]string, error)
}

// BouncerMode controls whether the bouncer runs and whether its
// verdict is acted on.
type BouncerMode string

const (
	// BouncerOff disables bouncer calls.
	BouncerOff BouncerMode = "off"
	// BouncerShadow assesses and logs, but the human still decides.
	BouncerShadow BouncerMode = "shadow"
	// BouncerEnforce acts on the bouncer's allow and deny verdicts.
	BouncerEnforce BouncerMode = "enforce"
)

// BouncerOptions configures the bouncer.
type BouncerOptions struct {
	Bouncer            Bouncer
	Intent             IntentSource // Nil when user messages are not shared.
	Mode               BouncerMode  // Initial mode; may be BouncerOff.
	Timeout            time.Duration
	ExplicitAskToHuman bool
	// Warm primes the bouncer when the runtime mode moves from off to
	// shadow or enforce. It runs in the background and may be nil.
	Warm func(ctx context.Context)
}

// Option configures a permission service.
type Option func(*permissionService)

// WithDecisionRecorder sets the recorder that receives every decision.
func WithDecisionRecorder(r DecisionRecorder) Option {
	return func(s *permissionService) { s.recorder = r }
}

// WithBouncer sets the bouncer consulted for requests that no explicit
// rule resolves.
func WithBouncer(o BouncerOptions) Option {
	return func(s *permissionService) {
		s.bouncer = o
		s.bouncerMode.Store(o.Mode)
	}
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

// defaultBouncerTimeout bounds a bouncer call when no timeout is set.
const defaultBouncerTimeout = 8 * time.Second

func (s *permissionService) currentBouncerMode() BouncerMode {
	mode, _ := s.bouncerMode.Load().(BouncerMode)
	return mode
}

func (s *permissionService) BouncerConfigured() bool {
	return s.bouncer.Bouncer != nil
}

func (s *permissionService) BouncerMode() BouncerMode {
	if mode := s.currentBouncerMode(); mode != "" {
		return mode
	}
	return BouncerOff
}

func (s *permissionService) SetBouncerMode(mode BouncerMode) {
	if s.bouncer.Bouncer == nil {
		return
	}
	switch mode {
	case BouncerOff, BouncerShadow, BouncerEnforce:
	default:
		return
	}
	for {
		old := s.currentBouncerMode()
		if old == mode {
			return
		}
		// The swap decides which caller saw the off-to-on transition,
		// so concurrent toggles warm at most once.
		if !s.bouncerMode.CompareAndSwap(old, mode) {
			continue
		}
		if (old == "" || old == BouncerOff) && mode != BouncerOff && s.bouncer.Warm != nil {
			go s.bouncer.Warm(context.Background())
		}
		return
	}
}

// shouldAssess reports whether an unresolved request goes to the bouncer
// before the human.
func (s *permissionService) shouldAssess(p policyResult) bool {
	if s.bouncer.Bouncer == nil {
		return false
	}
	if mode := s.currentBouncerMode(); mode == "" || mode == BouncerOff {
		return false
	}
	return p.isDefault || !s.bouncer.ExplicitAskToHuman
}

// assess runs the bouncer, converting errors and panics into an escalate
// outcome. failed reports whether the bouncer errored.
func (s *permissionService) assess(ctx context.Context, opts CreatePermissionRequest) (a Assessment, failed bool) {
	in := AssessInput{
		SessionID:   opts.SessionID,
		ToolName:    opts.ToolName,
		Action:      opts.Action,
		Description: opts.Description,
		Input:       opts.Input,
		Path:        opts.Path,
		WorkingDir:  s.workingDir,
		Segments:    slices.Clone(opts.InputSegments),
		Content:     opts.Content,
		Diff:        opts.Diff,
		ArgsJSON:    opts.ArgsJSON,
	}
	if s.bouncer.Intent != nil {
		msgs, err := s.bouncer.Intent.RecentUserMessages(ctx, opts.SessionID, 3)
		if err != nil {
			slog.Warn("Failed to load user intent for the bouncer", "tool", opts.ToolName, "error", err)
		} else {
			in.RecentUserMessages = msgs
		}
	}

	timeout := s.bouncer.Timeout
	if timeout <= 0 {
		timeout = defaultBouncerTimeout
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	a, err := s.callBouncer(actx, in)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("Bouncer failed; asking user", "tool", opts.ToolName, "error", err)
		}
		a.Outcome = AssessEscalate
		return a, true
	}
	return a, false
}

func (s *permissionService) callBouncer(ctx context.Context, in AssessInput) (a Assessment, err error) {
	defer func() {
		if r := recover(); r != nil {
			a = Assessment{Reason: "bouncer error"}
			err = fmt.Errorf("bouncer panicked: %v", r)
		}
	}()
	return s.bouncer.Bouncer.Assess(ctx, in)
}

// withAssessmentMode stamps the mode onto a marshalled AssessmentRecord.
// Details that don't decode are returned unchanged.
func withAssessmentMode(details json.RawMessage, mode BouncerMode) json.RawMessage {
	if len(details) == 0 {
		return nil
	}
	var rec AssessmentRecord
	if err := json.Unmarshal(details, &rec); err != nil {
		return details
	}
	rec.Mode = string(mode)
	out, err := json.Marshal(rec)
	if err != nil {
		return details
	}
	return out
}

func cachedAllowRecord() json.RawMessage {
	out, _ := json.Marshal(AssessmentRecord{
		SchemaVersion: AssessmentSchemaVersion,
		Mode:          string(BouncerEnforce),
		Outcome:       "allow",
		Reason:        "session allow cache",
	})
	return out
}

// allowCacheKey identifies a repeat of the same call within a session.
// Each field is length-prefixed so no two distinct calls can encode to the
// same bytes.
func allowCacheKey(opts CreatePermissionRequest) string {
	h := sha256.New()
	for _, f := range []string{
		opts.SessionID, opts.ToolName, opts.Action, opts.Path,
		opts.Input, opts.Content, opts.Diff, opts.ArgsJSON,
	} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// UserRequestedAxis is the noul measuring whether the user asked for the
// action. It lowers risk rather than raising it, so it is shown last.
// It must match bouncer.QUserRequested.
const UserRequestedAxis = "user_requested"

// SeverityAxis is the score rating how bad a mistake would be.
// It must match bouncer.QSeverity.
const SeverityAxis = "severity"

// AssessmentSummary is the bouncer's verdict on a prompted request, in a
// form the UI can lay out and highlight.
type AssessmentSummary struct {
	Shadow  bool              `json:"shadow,omitempty"`
	Outcome string            `json:"outcome"`          // allow, escalate, deny, skipped, or error.
	Detail  string            `json:"detail,omitempty"` // Skip reason, when skipped.
	Scores  []AssessmentScore `json:"scores,omitempty"`
}

// AssessmentScore is one answer from the bouncer.
type AssessmentScore struct {
	Name    string  `json:"name"`
	Value   float64 `json:"value"`
	Max     float64 `json:"max"`               // 1 for probabilities, 3 for severity.
	Trigger string  `json:"trigger,omitempty"` // A Trigger* effect, if it crossed a threshold.
}

// maxSeverity is the top of the severity score's scale.
const maxSeverity = 3

// assessmentSummary builds the display summary for a prompted request.
// Scores are ordered so the axes that drove the outcome come first:
// deny triggers, then escalation triggers, then the rest by value, with
// severity and the user-request signal last.
func assessmentSummary(a Assessment, details json.RawMessage, failed bool, mode BouncerMode) *AssessmentSummary {
	sum := &AssessmentSummary{Shadow: mode == BouncerShadow}
	if failed {
		sum.Outcome = "error"
		return sum
	}
	var rec AssessmentRecord
	_ = json.Unmarshal(details, &rec)
	if rec.Outcome == "skipped" {
		sum.Outcome = "skipped"
		sum.Detail = cmp.Or(rec.SkipReason, a.Reason)
		return sum
	}
	sum.Outcome = a.Outcome.String()

	var hazards []AssessmentScore
	var userRequested *AssessmentScore
	for name, v := range rec.Nouls {
		score := AssessmentScore{Name: name, Value: v, Max: 1, Trigger: rec.Triggers[name]}
		if name == UserRequestedAxis {
			userRequested = &score
			continue
		}
		hazards = append(hazards, score)
	}
	rank := map[string]int{TriggerDeny: 0, TriggerEscalate: 1}
	slices.SortFunc(hazards, func(x, y AssessmentScore) int {
		rx, okx := rank[x.Trigger]
		ry, oky := rank[y.Trigger]
		if !okx {
			rx = len(rank)
		}
		if !oky {
			ry = len(rank)
		}
		return cmp.Or(cmp.Compare(rx, ry), cmp.Compare(y.Value, x.Value), cmp.Compare(x.Name, y.Name))
	})
	sum.Scores = hazards
	if rec.Severity != nil {
		sum.Scores = append(sum.Scores, AssessmentScore{Name: SeverityAxis, Value: *rec.Severity, Max: maxSeverity, Trigger: rec.Triggers[SeverityAxis]})
	}
	if userRequested != nil {
		sum.Scores = append(sum.Scores, *userRequested)
	}
	return sum
}

// Note renders the summary as one plain-text line, for logs and clients
// that can't lay out the structured form.
func (s *AssessmentSummary) Note() string {
	if s == nil {
		return ""
	}
	prefix := "bouncer"
	if s.Shadow {
		prefix = "bouncer (shadow)"
	}
	note := prefix + ": " + s.Outcome
	if s.Detail != "" {
		return note + " · " + s.Detail
	}
	if len(s.Scores) == 0 {
		return note
	}
	scores := make([]string, len(s.Scores))
	for i, sc := range s.Scores {
		scores[i] = fmt.Sprintf("%s=%.2g", sc.Name, sc.Value)
	}
	return note + " · " + strings.Join(scores, " ")
}

// String returns the outcome name used in assessment records.
func (o AssessOutcome) String() string {
	switch o {
	case AssessAllow:
		return "allow"
	case AssessDeny:
		return "deny"
	default:
		return "escalate"
	}
}
