package permission

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

type fakeRecorder struct {
	mu        sync.Mutex
	decisions []Decision
}

func (r *fakeRecorder) Record(d Decision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisions = append(r.decisions, d)
}

func (r *fakeRecorder) snapshot() []Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.decisions)
}

func TestDecisionRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		level              config.YoloLevel
		rule               config.PermissionAction
		session            config.PermissionAction
		hook, auto, legacy bool
		prompt             string
		source             DecisionSource
		verdict            Verdict
		matched            string
		notifications      int
	}{
		{name: "full yolo", level: config.YoloFull, rule: config.PermissionDeny, source: DecisionSourceYolo, verdict: VerdictAllow},
		{name: "hook", hook: true, rule: config.PermissionDeny, source: DecisionSourceHook, verdict: VerdictAllow, notifications: 1},
		{name: "auto session", auto: true, source: DecisionSourceAutoSession, verdict: VerdictAllow, notifications: 2},
		{name: "config allow", rule: config.PermissionAllow, source: DecisionSourceRule, verdict: VerdictAllow, matched: "bash", notifications: 2},
		{name: "config deny", rule: config.PermissionDeny, source: DecisionSourceRule, verdict: VerdictDeny, matched: "bash", notifications: 2},
		{name: "session allow", session: config.PermissionAllow, source: DecisionSourceSessionRule, verdict: VerdictAllow, matched: "bash:git *", notifications: 2},
		{name: "session overrides ask", rule: config.PermissionAsk, session: config.PermissionAllow, source: DecisionSourceSessionRule, verdict: VerdictAllow, matched: "bash:git *", notifications: 2},
		{name: "deny beats session", rule: config.PermissionDeny, session: config.PermissionAllow, source: DecisionSourceRule, verdict: VerdictDeny, matched: "bash", notifications: 2},
		{name: "session deny", session: config.PermissionDeny, source: DecisionSourceSessionRule, verdict: VerdictDeny, matched: "bash:git *", notifications: 2},
		{name: "standard default", level: config.YoloStandard, source: DecisionSourceYolo, verdict: VerdictAllow, notifications: 2},
		{name: "standard ask", level: config.YoloStandard, rule: config.PermissionAsk, source: DecisionSourceYolo, verdict: VerdictAllow, matched: "bash", notifications: 2},
		{name: "standard session ask", level: config.YoloStandard, session: config.PermissionAsk, source: DecisionSourceYolo, verdict: VerdictAllow, matched: "bash:git *", notifications: 2},
		{name: "standard explicit allow", level: config.YoloStandard, rule: config.PermissionAllow, source: DecisionSourceRule, verdict: VerdictAllow, matched: "bash", notifications: 2},
		{name: "standard deny", level: config.YoloStandard, rule: config.PermissionDeny, source: DecisionSourceRule, verdict: VerdictDeny, matched: "bash", notifications: 2},
		{name: "legacy grant", legacy: true, source: DecisionSourceSessionGrant, verdict: VerdictAllow, notifications: 2},
		{name: "human allow", prompt: "allow", source: DecisionSourceHuman, verdict: VerdictAllow, notifications: 2},
		{name: "human deny", prompt: "deny", source: DecisionSourceHuman, verdict: VerdictDeny, notifications: 2},
		{name: "cancelled", prompt: "cancel", source: DecisionSourceHuman, verdict: VerdictCancelled, notifications: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			r := &fakeRecorder{}
			var rules []config.PermissionRule
			if tt.rule != "" {
				rules = []config.PermissionRule{{ToolPattern: "bash", Action: tt.rule}}
			}
			svc := NewPermissionService(dir, tt.level, rules, nil, WithDecisionRecorder(r)).(*permissionService)
			if tt.session != "" {
				require.NoError(t, svc.GrantSession("session", "bash", "git *", tt.session))
			}
			if tt.auto {
				svc.AutoApproveSession("session")
			}
			if tt.legacy {
				svc.GrantPersistent(PermissionRequest{SessionID: "session", ToolName: "bash", Action: "execute", Path: dir})
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if tt.hook {
				ctx = WithHookApproval(ctx, "call")
			}
			notifications := svc.SubscribeNotifications(t.Context())
			events := svc.Subscribe(t.Context())
			var wg sync.WaitGroup
			if tt.prompt != "" {
				wg.Go(func() {
					select {
					case event := <-events:
						switch tt.prompt {
						case "allow":
							svc.Grant(event.Payload)
						case "deny":
							svc.Deny(event.Payload, "not needed")
						case "cancel":
							cancel()
						}
					case <-ctx.Done():
					}
				})
			}
			opts := CreatePermissionRequest{SessionID: "session", ToolCallID: "call", ToolName: "bash", Action: "execute", Input: "git status && git log", InputSegments: []string{"git status", "git log"}, Path: dir}
			result, err := svc.Request(ctx, opts)
			wg.Wait()
			if tt.verdict == VerdictCancelled {
				require.ErrorIs(t, err, context.Canceled)
				svc.activeRequestMu.Lock()
				active := svc.activeRequest
				svc.activeRequestMu.Unlock()
				require.Nil(t, active)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.verdict == VerdictAllow, result.Granted)
			if tt.prompt == "deny" {
				require.Equal(t, "not needed", result.Reason)
			}
			require.Equal(t, []Decision{{SessionID: opts.SessionID, ToolCallID: opts.ToolCallID, ToolName: opts.ToolName, Action: opts.Action, Input: opts.Input, InputSegments: opts.InputSegments, WorkingDir: dir, DecidedBy: tt.source, Verdict: tt.verdict, MatchedRule: tt.matched}}, r.snapshot())
			require.Len(t, notifications, tt.notifications)
			for i := range tt.notifications {
				n := (<-notifications).Payload
				expected := PermissionNotification{ToolCallID: "call"}
				if i == tt.notifications-1 && tt.verdict != VerdictCancelled {
					expected.Granted = result.Granted
					expected.Denied = !result.Granted
					expected.Reason = result.Reason
				}
				require.Equal(t, expected, n)
			}
			opts.InputSegments[0] = "changed"
			require.Equal(t, "git status", r.snapshot()[0].InputSegments[0])
		})
	}
}

func TestDecisionCloneRules(t *testing.T) {
	t.Parallel()
	require.Nil(t, cloneRules(nil))
	rules := []config.PermissionRule{{ToolPattern: "bash", SubRules: []config.PermissionSubRule{{InputPattern: "git *", Action: config.PermissionAllow}}}}
	cloned := cloneRules(rules)
	rules = config.UpsertPermissionRule(rules, "bash", "git *", config.PermissionDeny)
	require.Equal(t, config.PermissionAllow, cloned[0].SubRules[0].Action)
	require.Equal(t, config.PermissionDeny, rules[0].SubRules[0].Action)
	rules[0].ToolPattern = "edit"
	require.Equal(t, "bash", cloned[0].ToolPattern)
}

func TestDecisionCancelPreservesOtherActiveRequest(t *testing.T) {
	t.Parallel()
	svc := NewPermissionService(t.TempDir(), config.YoloOff, nil, nil).(*permissionService)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	events := svc.Subscribe(ctx)
	var wg sync.WaitGroup
	var err error
	wg.Go(func() { _, err = svc.Request(ctx, CreatePermissionRequest{ToolName: "bash"}) })
	select {
	case <-events:
	case <-ctx.Done():
		t.Fatal("request was not published")
	}
	svc.activeRequestMu.Lock()
	svc.activeRequest = &PermissionRequest{ID: "other"}
	svc.activeRequestMu.Unlock()
	cancel()
	wg.Wait()
	require.ErrorIs(t, err, context.Canceled)
	svc.activeRequestMu.Lock()
	defer svc.activeRequestMu.Unlock()
	require.Equal(t, "other", svc.activeRequest.ID)
}

func TestWithAssessorSetsOptionsAndMode(t *testing.T) {
	t.Parallel()

	plain := NewPermissionService(t.TempDir(), config.YoloOff, nil, nil).(*permissionService)
	require.Equal(t, AssessorOff, plain.assessorMode.Load())
	require.Nil(t, plain.assessor.Assessor)

	opts := AssessorOptions{Mode: AssessorShadow, Timeout: time.Second, ExplicitAskToHuman: true}
	svc := NewPermissionService(t.TempDir(), config.YoloOff, nil, nil, WithAssessor(opts)).(*permissionService)
	require.Equal(t, AssessorShadow, svc.assessorMode.Load())
	require.Equal(t, opts, svc.assessor)
}
