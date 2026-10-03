package permission

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/permission/match"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/google/uuid"
)

// hookApprovalKey is the unexported context key used to mark a tool call as
// pre-approved by a PreToolUse hook. The value is the tool call ID so an
// approval can't be reused across calls that happen to share a context.
type hookApprovalKey struct{}

// WithHookApproval returns a context that marks the given tool call ID as
// pre-approved by a hook. When the permission service sees a matching
// request it short-circuits the normal prompt and grants immediately.
func WithHookApproval(ctx context.Context, toolCallID string) context.Context {
	return context.WithValue(ctx, hookApprovalKey{}, toolCallID)
}

// hookApproved reports whether the context carries a hook approval for the
// given tool call ID.
func hookApproved(ctx context.Context, toolCallID string) bool {
	if toolCallID == "" {
		return false
	}
	v, _ := ctx.Value(hookApprovalKey{}).(string)
	return v == toolCallID
}

// RequestResult holds the outcome of a permission request.
type RequestResult struct {
	Granted bool
	Reason  string // Non-empty when denied with a reason.
}

type CreatePermissionRequest struct {
	SessionID   string `json:"session_id"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params"`
	Path        string `json:"path"`
	Input       string `json:"input"` // Matchable input for rule evaluation.
	// InputSegments holds individually-evaluable sub-inputs (e.g. the
	// simple commands within a chained bash command). When non-empty,
	// each segment is evaluated separately and the worst outcome wins.
	InputSegments []string `json:"input_segments,omitempty"`
	// Content is the new file content for edits. It is local-only and
	// never serialised.
	Content string `json:"-"`
	// Diff is the unified diff an edit would apply, so removals are
	// visible alongside Content. It is local-only and never serialised.
	Diff string `json:"-"`
	// ArgsJSON is the raw MCP tool arguments. It is local-only and never
	// serialised.
	ArgsJSON string `json:"-"`
}

// permissionResponse is sent through the pending request channel to convey
// the user's grant/deny decision along with an optional reason.
type permissionResponse struct {
	Granted bool
	Reason  string
}

type PermissionNotification struct {
	ToolCallID string `json:"tool_call_id"`
	Granted    bool   `json:"granted"`
	Denied     bool   `json:"denied"`
	Reason     string `json:"reason,omitempty"`
}

type PermissionRequest struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params"`
	Path        string `json:"path"`
	Input       string `json:"input"`
	// InputSegments mirrors CreatePermissionRequest.InputSegments so the
	// UI dialog can access the individually-evaluated segments.
	InputSegments []string `json:"input_segments,omitempty"`
	// AssessorNote is a one-line summary of the assessor's verdict, shown
	// alongside the prompt.
	AssessorNote string `json:"assessor_note,omitempty"`
}

type Service interface {
	pubsub.Subscriber[PermissionRequest]
	// Deprecated: use GrantSession instead. Kept for backward compatibility
	// with the TUI.
	GrantPersistent(permission PermissionRequest)
	Grant(permission PermissionRequest)
	Deny(permission PermissionRequest, reason string)
	Request(ctx context.Context, opts CreatePermissionRequest) (RequestResult, error)
	AutoApproveSession(sessionID string)
	RevokeAutoApproveSession(sessionID string)
	SetYoloLevel(level config.YoloLevel)
	YoloLevel() config.YoloLevel
	SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[PermissionNotification]

	// GrantSession adds an ephemeral permission rule for this session.
	// The patterns are matched as globs against tool names and inputs.
	// Cannot override config-level deny rules (enforced at evaluation time).
	// Returns an error if a pattern is invalid.
	GrantSession(sessionID, toolPattern, inputPattern string, action config.PermissionAction) error

	// GrantForever writes a permission rule to the config file.
	// scope determines project vs user config.
	GrantForever(toolPattern string, inputPattern string, action config.PermissionAction, scope config.Scope) error
}

// PermissionKey is a composite key for session permission lookups.
type PermissionKey struct {
	SessionID string
	ToolName  string
	Action    string
	Path      string
}

// Lock ordering: requestMu is the outermost lock and must not be held
// when acquiring configRulesMu, sessionRulesMu, or activeRequestMu. The
// latter three are independent and never nested.
type permissionService struct {
	*pubsub.Broker[PermissionRequest]

	notificationBroker    *pubsub.Broker[PermissionNotification]
	workingDir            string
	sessionPermissions    *csync.Map[PermissionKey, bool]
	pendingRequests       *csync.Map[string, chan permissionResponse]
	autoApproveSessions   map[string]bool
	autoApproveSessionsMu sync.RWMutex
	yoloLevel             atomic.Int32
	configRules           []config.PermissionRule
	configRulesMu         sync.RWMutex
	sessionRules          map[string][]config.PermissionRule
	sessionRulesMu        sync.RWMutex
	configStore           *config.ConfigStore
	recorder              DecisionRecorder
	assessor              AssessorOptions
	assessorMode          atomic.Value // AssessorMode.
	allowCache            *csync.Map[string, struct{}]

	// beforePromptLock, when set, runs just before requestMu is acquired.
	// Tests use it as a barrier.
	beforePromptLock func(CreatePermissionRequest)

	// requestMu makes sure we only process one request at a time.
	requestMu       sync.Mutex
	activeRequest   *PermissionRequest
	activeRequestMu sync.Mutex
}

// Deprecated: use GrantSession instead. Kept for the HTTP/proto remote
// workspace path (backend.GrantPermission), which has no pattern
// information and still relies on exact PermissionKey matching. Remove
// once the remote protocol carries patterns for session grants.
func (s *permissionService) GrantPersistent(permission PermissionRequest) {
	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		ToolCallID: permission.ToolCallID,
		Granted:    true,
	})
	respCh, ok := s.pendingRequests.Get(permission.ID)
	if ok {
		respCh <- permissionResponse{Granted: true}
	}

	s.sessionPermissions.Set(PermissionKey{
		SessionID: permission.SessionID,
		ToolName:  permission.ToolName,
		Action:    permission.Action,
		Path:      permission.Path,
	}, true)

	s.activeRequestMu.Lock()
	if s.activeRequest != nil && s.activeRequest.ID == permission.ID {
		s.activeRequest = nil
	}
	s.activeRequestMu.Unlock()
}

func (s *permissionService) Grant(permission PermissionRequest) {
	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		ToolCallID: permission.ToolCallID,
		Granted:    true,
	})
	respCh, ok := s.pendingRequests.Get(permission.ID)
	if ok {
		respCh <- permissionResponse{Granted: true}
	}

	s.activeRequestMu.Lock()
	if s.activeRequest != nil && s.activeRequest.ID == permission.ID {
		s.activeRequest = nil
	}
	s.activeRequestMu.Unlock()
}

func (s *permissionService) Deny(permission PermissionRequest, reason string) {
	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		ToolCallID: permission.ToolCallID,
		Granted:    false,
		Denied:     true,
		Reason:     reason,
	})
	respCh, ok := s.pendingRequests.Get(permission.ID)
	if ok {
		respCh <- permissionResponse{Granted: false, Reason: reason}
	}

	s.activeRequestMu.Lock()
	if s.activeRequest != nil && s.activeRequest.ID == permission.ID {
		s.activeRequest = nil
	}
	s.activeRequestMu.Unlock()
}

func (s *permissionService) Request(ctx context.Context, opts CreatePermissionRequest) (RequestResult, error) {
	// YoloFull bypasses all checks.
	if config.YoloLevel(s.yoloLevel.Load()) == config.YoloFull {
		s.record(opts, DecisionSourceYolo, VerdictAllow, "", nil)
		return RequestResult{Granted: true}, nil
	}

	// A PreToolUse hook that returned decision=allow stamps the context
	// with the tool call ID. Treat that as a pre-approval and skip the
	// prompt entirely. We still publish a granted notification so the UI
	// and audit subscribers see the outcome.
	if hookApproved(ctx, opts.ToolCallID) {
		return s.finish(opts, DecisionSourceHook, VerdictAllow, "", nil, ""), nil
	}

	// Tell the UI that a permission was requested.
	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		ToolCallID: opts.ToolCallID,
	})

	p := s.evaluatePolicy(opts)
	if p.resolved {
		return s.finishPolicy(opts, p, nil), nil
	}

	perm := PermissionRequest{
		ID:            uuid.New().String(),
		Path:          s.requestDir(opts.Path),
		SessionID:     opts.SessionID,
		ToolCallID:    opts.ToolCallID,
		ToolName:      opts.ToolName,
		Description:   opts.Description,
		Action:        opts.Action,
		Params:        opts.Params,
		Input:         opts.Input,
		InputSegments: opts.InputSegments,
	}

	// The assessor runs without requestMu so concurrent requests are
	// classified in parallel rather than queued behind a human prompt.
	var details json.RawMessage
	if s.shouldAssess(p) {
		key := allowCacheKey(opts)
		if s.currentAssessorMode() == AssessorEnforce {
			if _, ok := s.allowCache.Get(key); ok {
				return s.finish(opts, DecisionSourceAssessor, VerdictAllow, "", cachedAllowRecord(), ""), nil
			}
		}

		a, failed := s.assess(ctx, opts)
		mode := s.currentAssessorMode()
		details = withAssessmentMode(a.Details, mode)
		if ctx.Err() != nil {
			s.record(opts, DecisionSourceHuman, VerdictCancelled, "", details)
			return RequestResult{}, ctx.Err()
		}

		// Commit boundary 1: rules may have changed during the call.
		if p2 := s.evaluatePolicy(opts); p2.resolved {
			return s.finishPolicy(opts, p2, details), nil
		}

		if mode == AssessorEnforce {
			switch a.Outcome {
			case AssessAllow:
				s.allowCache.Set(key, struct{}{})
				return s.finish(opts, DecisionSourceAssessor, VerdictAllow, "", details, ""), nil
			case AssessDeny:
				reason := "blocked by permission assessor (" + a.Reason + "). Do not retry this or work around it; tell the user what you were trying to do."
				return s.finish(opts, DecisionSourceAssessor, VerdictDeny, "", details, reason), nil
			}
		}
		if mode != AssessorOff {
			perm.AssessorNote = assessorNote(a, details, failed, mode)
		}
	}

	if s.beforePromptLock != nil {
		s.beforePromptLock(opts)
	}
	s.requestMu.Lock()
	defer s.requestMu.Unlock()

	if ctx.Err() != nil {
		s.record(opts, DecisionSourceHuman, VerdictCancelled, "", details)
		return RequestResult{}, ctx.Err()
	}

	// Commit boundary 2: a sibling prompt may have added a grant or rule
	// while this request waited for the prompt slot.
	if p3 := s.evaluatePolicy(opts); p3.resolved {
		return s.finishPolicy(opts, p3, details), nil
	}

	s.activeRequestMu.Lock()
	s.activeRequest = &perm
	s.activeRequestMu.Unlock()

	respCh := make(chan permissionResponse, 1)
	s.pendingRequests.Set(perm.ID, respCh)
	defer s.pendingRequests.Del(perm.ID)

	// Publish the request.
	s.Publish(pubsub.CreatedEvent, perm)

	select {
	case <-ctx.Done():
		s.activeRequestMu.Lock()
		if s.activeRequest != nil && s.activeRequest.ID == perm.ID {
			s.activeRequest = nil
		}
		s.activeRequestMu.Unlock()
		s.record(opts, DecisionSourceHuman, VerdictCancelled, "", details)
		return RequestResult{}, ctx.Err()
	case resp := <-respCh:
		verdict := VerdictDeny
		if resp.Granted {
			verdict = VerdictAllow
		}
		s.record(opts, DecisionSourceHuman, verdict, "", details)
		return RequestResult{Granted: resp.Granted, Reason: resp.Reason}, nil
	}
}

// policyResult is the outcome of the deterministic policy layers.
type policyResult struct {
	resolved    bool
	source      DecisionSource
	verdict     Verdict
	matchedRule string
	reason      string
	isDefault   bool // Unresolved because no rule matched.
}

// evaluatePolicy applies session auto-approval, config and session rules,
// yolo-standard promotion, and legacy session grants. It reads fresh
// snapshots on every call so it can be re-run at each commit boundary.
func (s *permissionService) evaluatePolicy(opts CreatePermissionRequest) policyResult {
	s.autoApproveSessionsMu.RLock()
	autoApprove := s.autoApproveSessions[opts.SessionID]
	s.autoApproveSessionsMu.RUnlock()

	if autoApprove {
		return policyResult{resolved: true, source: DecisionSourceAutoSession, verdict: VerdictAllow}
	}

	// Evaluate rules. Clone the slices so a concurrent GrantForever or
	// GrantSession upsert (which mutates elements in place) cannot race
	// with evaluation after the locks are released.
	s.configRulesMu.RLock()
	configRules := cloneRules(s.configRules)
	s.configRulesMu.RUnlock()

	s.sessionRulesMu.RLock()
	sessionRules := cloneRules(s.sessionRules[opts.SessionID])
	s.sessionRulesMu.RUnlock()

	var result EvaluateResult
	if len(opts.InputSegments) > 0 {
		result = EvaluateAll(opts.ToolName, opts.InputSegments, configRules, sessionRules)
	} else {
		result = Evaluate(opts.ToolName, opts.Input, configRules, sessionRules)
	}

	slog.Debug("Permission evaluation result",
		"tool", opts.ToolName,
		"input", opts.Input,
		"action", result.Action,
		"matched_rule", result.MatchedRule,
		"is_default", result.IsDefault,
	)

	// Apply yolo level: standard promotes ask → allow.
	action := result.Action
	source := DecisionSourceRule
	if result.FromSession {
		source = DecisionSourceSessionRule
	}
	if config.YoloLevel(s.yoloLevel.Load()) == config.YoloStandard && action == config.PermissionAsk {
		action = config.PermissionAllow
		source = DecisionSourceYolo
	}

	switch action {
	case config.PermissionAllow:
		return policyResult{resolved: true, source: source, verdict: VerdictAllow, matchedRule: result.MatchedRule}
	case config.PermissionDeny:
		return policyResult{
			resolved:    true,
			source:      source,
			verdict:     VerdictDeny,
			matchedRule: result.MatchedRule,
			reason:      fmt.Sprintf("denied by rule %q", result.MatchedRule),
		}
	}

	if _, ok := s.sessionPermissions.Get(PermissionKey{
		SessionID: opts.SessionID,
		ToolName:  opts.ToolName,
		Action:    opts.Action,
		Path:      s.requestDir(opts.Path),
	}); ok {
		return policyResult{resolved: true, source: DecisionSourceSessionGrant, verdict: VerdictAllow}
	}

	return policyResult{isDefault: result.IsDefault}
}

func (s *permissionService) finishPolicy(opts CreatePermissionRequest, p policyResult, assessment json.RawMessage) RequestResult {
	return s.finish(opts, p.source, p.verdict, p.matchedRule, assessment, p.reason)
}

// requestDir resolves the directory a request applies to, as shown in the
// prompt and used for legacy session grants.
func (s *permissionService) requestDir(path string) string {
	dir := path
	if fileInfo, err := os.Stat(path); err == nil && !fileInfo.IsDir() {
		dir = filepath.Dir(path)
	}
	if dir == "." {
		dir = s.workingDir
	}
	return dir
}

func (s *permissionService) AutoApproveSession(sessionID string) {
	s.autoApproveSessionsMu.Lock()
	s.autoApproveSessions[sessionID] = true
	s.autoApproveSessionsMu.Unlock()
}

// RevokeAutoApproveSession removes the auto-approve entry for the
// given session, allowing it to go through normal permission checks.
func (s *permissionService) RevokeAutoApproveSession(sessionID string) {
	s.autoApproveSessionsMu.Lock()
	delete(s.autoApproveSessions, sessionID)
	s.autoApproveSessionsMu.Unlock()
}

func (s *permissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[PermissionNotification] {
	return s.notificationBroker.Subscribe(ctx)
}

func (s *permissionService) SetYoloLevel(level config.YoloLevel) {
	s.yoloLevel.Store(int32(level))
}

func (s *permissionService) YoloLevel() config.YoloLevel {
	return config.YoloLevel(s.yoloLevel.Load())
}

// GrantSession adds an ephemeral permission rule for this session. The
// patterns are matched as globs against tool names and inputs. Cannot
// override config-level deny rules (enforced at evaluation time).
// Invalid patterns are rejected with an error; they would never match
// and would give a false sense of having been granted.
func (s *permissionService) GrantSession(sessionID, toolPattern, inputPattern string, action config.PermissionAction) error {
	if err := match.Validate(toolPattern); err != nil {
		return fmt.Errorf("invalid tool pattern %q: %w", toolPattern, err)
	}
	if inputPattern != "" {
		if err := match.Validate(inputPattern); err != nil {
			return fmt.Errorf("invalid input pattern %q: %w", inputPattern, err)
		}
	}

	s.sessionRulesMu.Lock()
	s.sessionRules[sessionID] = config.UpsertPermissionRule(s.sessionRules[sessionID], toolPattern, inputPattern, action)
	s.sessionRulesMu.Unlock()
	return nil
}

// GrantForever writes a permission rule to the config file. scope
// determines project vs user config.
func (s *permissionService) GrantForever(toolPattern string, inputPattern string, action config.PermissionAction, scope config.Scope) error {
	if s.configStore == nil {
		return fmt.Errorf("no config store configured")
	}

	if err := s.configStore.SetPermissionRule(scope, toolPattern, inputPattern, action); err != nil {
		return err
	}

	// Upsert the rule into configRules in memory so it takes effect
	// immediately. The ConfigStore's autoReload refreshes the store's
	// own Config but not this service's snapshot, so this is the only
	// path for immediate in-memory effect. Upserting (not appending)
	// keeps the snapshot consistent with the upserted file state.
	s.configRulesMu.Lock()
	s.configRules = config.UpsertPermissionRule(s.configRules, toolPattern, inputPattern, action)
	s.configRulesMu.Unlock()

	return nil
}

func cloneRules(rules []config.PermissionRule) []config.PermissionRule {
	cloned := slices.Clone(rules)
	for i := range cloned {
		cloned[i].SubRules = slices.Clone(cloned[i].SubRules)
	}
	return cloned
}

// NewPermissionService creates a new permission service with the given
// config rules.
func NewPermissionService(workingDir string, yoloLevel config.YoloLevel, configRules []config.PermissionRule, configStore *config.ConfigStore, opts ...Option) Service {
	svc := &permissionService{
		Broker:              pubsub.NewBroker[PermissionRequest](),
		notificationBroker:  pubsub.NewBroker[PermissionNotification](),
		workingDir:          workingDir,
		sessionPermissions:  csync.NewMap[PermissionKey, bool](),
		autoApproveSessions: make(map[string]bool),
		configRules:         configRules,
		sessionRules:        make(map[string][]config.PermissionRule),
		pendingRequests:     csync.NewMap[string, chan permissionResponse](),
		configStore:         configStore,
		allowCache:          csync.NewMap[string, struct{}](),
	}
	svc.yoloLevel.Store(int32(yoloLevel))
	svc.assessorMode.Store(AssessorOff)
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}
