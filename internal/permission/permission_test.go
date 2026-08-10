package permission

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermissionService_ConfigRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		configRules []config.PermissionRule
		toolName    string
		input       string
		expected    bool
	}{
		{
			name: "tool allowed by wildcard rule",
			configRules: []config.PermissionRule{
				{ToolPattern: "*", Action: config.PermissionAllow},
			},
			toolName: "bash",
			expected: true,
		},
		{
			name: "tool allowed by exact rule",
			configRules: []config.PermissionRule{
				{ToolPattern: "bash", Action: config.PermissionAllow},
			},
			toolName: "bash",
			expected: true,
		},
		{
			name: "tool not matched defaults to ask",
			configRules: []config.PermissionRule{
				{ToolPattern: "view", Action: config.PermissionAllow},
			},
			toolName: "bash",
			expected: false, // will fall through to ask, requires subscriber
		},
		{
			name:        "empty rules defaults to ask",
			configRules: nil,
			toolName:    "bash",
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := NewPermissionService("/tmp", config.YoloOff, tt.configRules, nil)
			ps := service.(*permissionService)

			result := Evaluate(tt.toolName, tt.input, ps.configRules, nil)
			allowed := result.Action == config.PermissionAllow

			require.Equal(t, tt.expected, allowed)
		})
	}
}

func TestSkipRace(t *testing.T) {
	t.Parallel()
	svc := NewPermissionService("/tmp", config.YoloOff, nil, nil)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		svc.SetYoloLevel(config.YoloStandard)
	}()
	go func() {
		defer wg.Done()
		svc.YoloLevel()
	}()
	wg.Wait()
}

func TestPermissionService_YoloStandard(t *testing.T) {
	t.Parallel()
	service := NewPermissionService("/tmp", config.YoloStandard, nil, nil)

	result, err := service.Request(t.Context(), CreatePermissionRequest{
		SessionID:   "test-session",
		ToolName:    "bash",
		Action:      "execute",
		Description: "test command",
		Path:        "/tmp",
	})
	require.NoError(t, err)
	require.True(t, result.Granted, "YoloStandard should promote ask to allow")
}

func TestPermissionService_YoloFull(t *testing.T) {
	t.Parallel()
	service := NewPermissionService("/tmp", config.YoloFull, nil, nil)

	result, err := service.Request(t.Context(), CreatePermissionRequest{
		SessionID:   "test-session",
		ToolName:    "bash",
		Action:      "execute",
		Description: "test command",
		Path:        "/tmp",
	})
	require.NoError(t, err)
	require.True(t, result.Granted, "YoloFull should bypass all checks")
}

func TestPermissionService_HookApproval(t *testing.T) {
	t.Parallel()

	t.Run("matching tool call ID short-circuits the prompt", func(t *testing.T) {
		t.Parallel()
		service := NewPermissionService("/tmp", config.YoloOff, nil, nil)

		ctx := WithHookApproval(t.Context(), "call-42")
		granted, err := service.Request(ctx, CreatePermissionRequest{
			SessionID:   "s1",
			ToolCallID:  "call-42",
			ToolName:    "bash",
			Action:      "execute",
			Description: "hook-approved command",
			Path:        "/tmp",
		})
		require.NoError(t, err)
		assert.True(t, granted.Granted, "hook-approved call should bypass the prompt")
	})

	t.Run("approval is scoped to the stamped tool call ID", func(t *testing.T) {
		t.Parallel()
		service := NewPermissionService("/tmp", config.YoloOff, nil, nil)

		// Stamp for call-42, ask for a different call ID — must not leak.
		ctx := WithHookApproval(t.Context(), "call-42")

		// Kick off a real request that will need a subscriber to resolve it.
		events := service.Subscribe(t.Context())
		var (
			wg      sync.WaitGroup
			granted RequestResult
			err     error
		)
		wg.Go(func() {
			granted, err = service.Request(ctx, CreatePermissionRequest{
				SessionID:   "s1",
				ToolCallID:  "call-other",
				ToolName:    "bash",
				Action:      "execute",
				Description: "unrelated call",
				Path:        "/tmp",
			})
		})

		// Confirm the service published a real request (i.e. didn't bypass).
		event := <-events
		service.Deny(event.Payload, "")
		wg.Wait()
		require.NoError(t, err)
		assert.False(t, granted.Granted, "stamped approval must not apply to a different tool call")
	})

	t.Run("notifies subscribers that permission was granted", func(t *testing.T) {
		t.Parallel()
		service := NewPermissionService("/tmp", config.YoloOff, nil, nil)

		notifications := service.SubscribeNotifications(t.Context())

		ctx := WithHookApproval(t.Context(), "call-99")
		granted, err := service.Request(ctx, CreatePermissionRequest{
			SessionID:  "s1",
			ToolCallID: "call-99",
			ToolName:   "view",
			Action:     "read",
			Path:       "/tmp",
		})
		require.NoError(t, err)
		assert.True(t, granted.Granted)

		event := <-notifications
		assert.Equal(t, "call-99", event.Payload.ToolCallID)
		assert.True(t, event.Payload.Granted, "subscribers should see a granted notification")
	})
}

func TestPermissionService_SequentialProperties(t *testing.T) {
	t.Run("Sequential permission requests with persistent grants", func(t *testing.T) {
		service := NewPermissionService("/tmp", config.YoloOff, nil, nil)

		req1 := CreatePermissionRequest{
			SessionID:   "session1",
			ToolName:    "file_tool",
			Description: "Read file",
			Action:      "read",
			Params:      map[string]string{"file": "test.txt"},
			Path:        "/tmp/test.txt",
		}

		var result1 RequestResult
		var wg sync.WaitGroup
		wg.Add(1)

		events := service.Subscribe(t.Context())

		go func() {
			defer wg.Done()
			result1, _ = service.Request(t.Context(), req1)
		}()

		var permissionReq PermissionRequest
		event := <-events

		permissionReq = event.Payload
		service.GrantPersistent(permissionReq)

		wg.Wait()
		assert.True(t, result1.Granted, "First request should be granted")

		// Second identical request should be automatically approved due to persistent permission.
		req2 := CreatePermissionRequest{
			SessionID:   "session1",
			ToolName:    "file_tool",
			Description: "Read file again",
			Action:      "read",
			Params:      map[string]string{"file": "test.txt"},
			Path:        "/tmp/test.txt",
		}
		result2, err := service.Request(t.Context(), req2)
		require.NoError(t, err)
		assert.True(t, result2.Granted, "Second request should be auto-approved")
	})
	t.Run("Sequential requests with temporary grants", func(t *testing.T) {
		service := NewPermissionService("/tmp", config.YoloOff, nil, nil)

		req := CreatePermissionRequest{
			SessionID:   "session2",
			ToolName:    "file_tool",
			Description: "Write file",
			Action:      "write",
			Params:      map[string]string{"file": "test.txt"},
			Path:        "/tmp/test.txt",
		}

		events := service.Subscribe(t.Context())
		var result1 RequestResult
		var wg sync.WaitGroup

		wg.Go(func() {
			result1, _ = service.Request(t.Context(), req)
		})

		var permissionReq PermissionRequest
		event := <-events
		permissionReq = event.Payload

		service.Grant(permissionReq)
		wg.Wait()
		assert.True(t, result1.Granted, "First request should be granted")

		var result2 RequestResult

		wg.Go(func() {
			result2, _ = service.Request(t.Context(), req)
		})

		event = <-events
		permissionReq = event.Payload
		service.Deny(permissionReq, "")
		wg.Wait()
		assert.False(t, result2.Granted, "Second request should be denied")
	})
	t.Run("Concurrent requests with different outcomes", func(t *testing.T) {
		service := NewPermissionService("/tmp", config.YoloOff, nil, nil)

		events := service.Subscribe(t.Context())

		var wg sync.WaitGroup
		results := make([]RequestResult, 3)

		requests := []CreatePermissionRequest{
			{
				SessionID:   "concurrent1",
				ToolName:    "tool1",
				Action:      "action1",
				Path:        "/tmp/file1.txt",
				Description: "First concurrent request",
			},
			{
				SessionID:   "concurrent2",
				ToolName:    "tool2",
				Action:      "action2",
				Path:        "/tmp/file2.txt",
				Description: "Second concurrent request",
			},
			{
				SessionID:   "concurrent3",
				ToolName:    "tool3",
				Action:      "action3",
				Path:        "/tmp/file3.txt",
				Description: "Third concurrent request",
			},
		}

		for i, req := range requests {
			wg.Add(1)
			go func(index int, request CreatePermissionRequest) {
				defer wg.Done()
				result, _ := service.Request(t.Context(), request)
				results[index] = result
			}(i, req)
		}

		for range 3 {
			event := <-events
			switch event.Payload.ToolName {
			case "tool1":
				service.Grant(event.Payload)
			case "tool2":
				service.GrantPersistent(event.Payload)
			case "tool3":
				service.Deny(event.Payload, "")
			}
		}
		wg.Wait()
		grantedCount := 0
		for _, result := range results {
			if result.Granted {
				grantedCount++
			}
		}

		assert.Equal(t, 2, grantedCount, "Should have 2 granted and 1 denied")
		secondReq := requests[1]
		secondReq.Description = "Repeat of second request"
		result, err := service.Request(t.Context(), secondReq)
		require.NoError(t, err)
		assert.True(t, result.Granted, "Repeated request should be auto-approved due to persistent permission")
	})
}

func TestRevokeAutoApproveSession(t *testing.T) {
	t.Parallel()

	t.Run("revokes auto-approve for a session", func(t *testing.T) {
		t.Parallel()
		service := NewPermissionService("/tmp", config.YoloOff, nil, nil)

		service.AutoApproveSession("sess-1")

		// Session should be auto-approved.
		granted, err := service.Request(t.Context(), CreatePermissionRequest{
			SessionID:   "sess-1",
			ToolName:    "bash",
			Action:      "execute",
			Description: "auto-approved command",
			Path:        "/tmp",
		})
		require.NoError(t, err)
		require.True(t, granted.Granted)

		service.RevokeAutoApproveSession("sess-1")

		// After revocation the session should no longer be auto-approved.
		// We need a subscriber to resolve the request now.
		events := service.Subscribe(t.Context())
		var (
			wg     sync.WaitGroup
			result RequestResult
			reqErr error
		)
		wg.Go(func() {
			result, reqErr = service.Request(t.Context(), CreatePermissionRequest{
				SessionID:   "sess-1",
				ToolName:    "bash",
				Action:      "execute",
				Description: "should require approval",
				Path:        "/tmp",
			})
		})

		event := <-events
		service.Deny(event.Payload, "")
		wg.Wait()

		require.NoError(t, reqErr)
		require.False(t, result.Granted, "revoked session should no longer be auto-approved")
	})

	t.Run("revoking nonexistent session does not panic", func(t *testing.T) {
		t.Parallel()
		service := NewPermissionService("/tmp", config.YoloOff, nil, nil)
		require.NotPanics(t, func() {
			service.RevokeAutoApproveSession("nonexistent")
		})
	})
}

func TestGrantSession_PatternAllowsMatchingRequest(t *testing.T) {
	t.Parallel()

	service := NewPermissionService("/tmp", config.YoloOff, nil, nil)

	// Grant a session rule for git commands.
	service.GrantSession("s1", "bash", "git *", config.PermissionAllow)

	// A request matching the pattern should be auto-allowed.
	result, err := service.Request(t.Context(), CreatePermissionRequest{
		SessionID:   "s1",
		ToolName:    "bash",
		Action:      "execute",
		Description: "git status",
		Input:       "git status",
		Path:        "/tmp",
	})
	require.NoError(t, err)
	require.True(t, result.Granted, "session grant should auto-allow matching request")
}

func TestGrantSession_CannotOverrideConfigDeny(t *testing.T) {
	t.Parallel()

	configRules := []config.PermissionRule{
		{ToolPattern: "bash", Action: config.PermissionDeny},
	}
	service := NewPermissionService("/tmp", config.YoloOff, configRules, nil)

	// Try to session-grant allow for bash.
	service.GrantSession("s1", "bash", "", config.PermissionAllow)

	// The config deny should win.
	result, err := service.Request(t.Context(), CreatePermissionRequest{
		SessionID:   "s1",
		ToolName:    "bash",
		Action:      "execute",
		Description: "denied command",
		Input:       "echo hello",
		Path:        "/tmp",
	})
	require.NoError(t, err)
	require.False(t, result.Granted, "session grant should not override config deny")
}

func TestGrantOnce_DoesNotAffectSubsequentRequests(t *testing.T) {
	t.Parallel()

	service := NewPermissionService("/tmp", config.YoloOff, nil, nil)
	events := service.Subscribe(t.Context())

	// First request: grant once.
	var wg sync.WaitGroup
	var result1 RequestResult
	wg.Go(func() {
		var err error
		result1, err = service.Request(t.Context(), CreatePermissionRequest{
			SessionID:   "s1",
			ToolName:    "bash",
			Action:      "execute",
			Description: "first request",
			Input:       "echo hello",
			Path:        "/tmp",
		})
		require.NoError(t, err)
	})

	event := <-events
	service.Grant(event.Payload)
	wg.Wait()
	require.True(t, result1.Granted, "first request should be granted")

	// Second identical request should NOT be auto-approved.
	var result2 RequestResult
	wg.Go(func() {
		var err error
		result2, err = service.Request(t.Context(), CreatePermissionRequest{
			SessionID:   "s1",
			ToolName:    "bash",
			Action:      "execute",
			Description: "second request",
			Input:       "echo hello",
			Path:        "/tmp",
		})
		require.NoError(t, err)
	})

	event = <-events
	service.Deny(event.Payload, "")
	wg.Wait()
	require.False(t, result2.Granted, "grant once should not affect subsequent requests")
}

func TestGrantSession_ToolLevelAllow(t *testing.T) {
	t.Parallel()

	service := NewPermissionService("/tmp", config.YoloOff, nil, nil)

	// Grant all bash commands.
	service.GrantSession("s1", "bash", "", config.PermissionAllow)

	result, err := service.Request(t.Context(), CreatePermissionRequest{
		SessionID:   "s1",
		ToolName:    "bash",
		Action:      "execute",
		Description: "any bash command",
		Input:       "rm -rf /",
		Path:        "/tmp",
	})
	require.NoError(t, err)
	require.True(t, result.Granted, "session grant for all bash should auto-allow")
}

func TestPermissionService_InputSegments(t *testing.T) {
	t.Parallel()

	configRules := []config.PermissionRule{
		{
			ToolPattern: "bash",
			SubRules: []config.PermissionSubRule{
				{InputPattern: "git *", Action: config.PermissionAllow},
			},
		},
	}

	t.Run("all segments allowed grants immediately", func(t *testing.T) {
		t.Parallel()
		service := NewPermissionService("/tmp", config.YoloOff, configRules, nil)

		result, err := service.Request(t.Context(), CreatePermissionRequest{
			SessionID:     "s1",
			ToolName:      "bash",
			Action:        "execute",
			Description:   "chained git commands",
			Input:         "git status && git log",
			InputSegments: []string{"git status", "git log"},
			Path:          "/tmp",
		})
		require.NoError(t, err)
		require.True(t, result.Granted, "all-allowed segments should grant without prompting")
	})

	t.Run("unmatched segment blocks waiting for a response", func(t *testing.T) {
		t.Parallel()
		service := NewPermissionService("/tmp", config.YoloOff, configRules, nil)

		events := service.Subscribe(t.Context())

		var result RequestResult
		var wg sync.WaitGroup
		wg.Go(func() {
			result, _ = service.Request(t.Context(), CreatePermissionRequest{
				SessionID:     "s1",
				ToolName:      "bash",
				Action:        "execute",
				Description:   "chained command with unmatched segment",
				Input:         "git status && rm -rf /",
				InputSegments: []string{"git status", "rm -rf /"},
				Path:          "/tmp",
			})
		})

		// The request must block and publish a pending prompt.
		event := <-events
		require.Equal(t, []string{"git status", "rm -rf /"}, event.Payload.InputSegments)

		service.Deny(event.Payload, "")
		wg.Wait()
		require.False(t, result.Granted, "denied prompt should not grant")
	})
}

func TestGrantSession_RejectsInvalidPatterns(t *testing.T) {
	t.Parallel()

	svc := NewPermissionService(t.TempDir(), config.YoloOff, nil, nil)

	// An invalid input pattern must error and not create a session rule.
	err := svc.GrantSession("s1", "bash", "[unclosed", config.PermissionAllow)
	require.ErrorContains(t, err, "invalid input pattern")

	err = svc.GrantSession("s1", "{unclosed", "", config.PermissionAllow)
	require.ErrorContains(t, err, "invalid tool pattern")

	impl := svc.(*permissionService)
	impl.sessionRulesMu.RLock()
	defer impl.sessionRulesMu.RUnlock()
	require.Empty(t, impl.sessionRules["s1"])
}

func TestGrantSession_UpsertsRules(t *testing.T) {
	t.Parallel()

	svc := NewPermissionService(t.TempDir(), config.YoloOff, nil, nil)

	require.NoError(t, svc.GrantSession("s1", "bash", "git status *", config.PermissionAllow))
	require.NoError(t, svc.GrantSession("s1", "bash", "git diff *", config.PermissionAllow))
	// Repeating an identical grant must not add anything.
	require.NoError(t, svc.GrantSession("s1", "bash", "git diff *", config.PermissionAllow))

	impl := svc.(*permissionService)
	impl.sessionRulesMu.RLock()
	defer impl.sessionRulesMu.RUnlock()
	require.Len(t, impl.sessionRules["s1"], 1)
	require.Len(t, impl.sessionRules["s1"][0].SubRules, 2)
}

func TestGrantForever_UpsertsInMemory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := config.NewTestStoreWithDataPath(&config.Config{}, filepath.Join(dir, "anvil.json"))
	svc := NewPermissionService(dir, config.YoloOff, nil, store)
	impl := svc.(*permissionService)

	// Two grants for the same tool must merge into one rule with two
	// sub-rules, not two separate rules.
	require.NoError(t, svc.GrantForever("bash", "git status *", config.PermissionAllow, config.ScopeGlobal))
	require.NoError(t, svc.GrantForever("bash", "git diff *", config.PermissionAllow, config.ScopeGlobal))
	// Repeating an identical grant must not add anything.
	require.NoError(t, svc.GrantForever("bash", "git diff *", config.PermissionAllow, config.ScopeGlobal))

	impl.configRulesMu.RLock()
	defer impl.configRulesMu.RUnlock()
	require.Len(t, impl.configRules, 1)
	require.Len(t, impl.configRules[0].SubRules, 2)
}
