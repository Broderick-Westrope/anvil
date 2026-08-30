package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/openaicompat"
	"github.com/Broderick-Westrope/anvil/internal/agent/prompt"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	toolsmcp "github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/plugin"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSessionAgent is a minimal mock for the SessionAgent interface.
type mockSessionAgent struct {
	model     Model
	runFunc   func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error)
	cancelled []string
}

func (m *mockSessionAgent) Run(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
	return m.runFunc(ctx, call)
}

func (m *mockSessionAgent) Model() Model                              { return m.model }
func (m *mockSessionAgent) SetModels(large, small Model)              {}
func (m *mockSessionAgent) SetProviderConfig(_ config.ProviderConfig) {}
func (m *mockSessionAgent) SetTools(tools []fantasy.AgentTool)        {}
func (m *mockSessionAgent) SetLazyMCPToolMap(_ map[string]string)     {}
func (m *mockSessionAgent) SetConnectFn(_ tools.ConnectFn)            {}
func (m *mockSessionAgent) SetSystemPrompt(systemPrompt string)       {}
func (m *mockSessionAgent) Cancel(sessionID string) {
	m.cancelled = append(m.cancelled, sessionID)
}
func (m *mockSessionAgent) CancelAll()                                  {}
func (m *mockSessionAgent) IsSessionBusy(sessionID string) bool         { return false }
func (m *mockSessionAgent) IsBusy() bool                                { return false }
func (m *mockSessionAgent) QueuedPrompts(sessionID string) int          { return 0 }
func (m *mockSessionAgent) QueuedPromptsList(sessionID string) []string { return nil }
func (m *mockSessionAgent) ClearQueue(sessionID string)                 {}
func (m *mockSessionAgent) Summarize(context.Context, string, fantasy.ProviderOptions) error {
	return nil
}

// newTestCoordinator creates a minimal coordinator for unit testing runSubAgent.
func newTestCoordinator(t *testing.T, env fakeEnv, providerID string, providerCfg config.ProviderConfig) *coordinator {
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)
	cfg.Config().Providers.Set(providerID, providerCfg)
	return &coordinator{
		cfg:         cfg,
		sessions:    env.sessions,
		messages:    env.messages,
		permissions: env.permissions,
	}
}

// newMockAgent creates a mockSessionAgent with the given provider and run function.
func newMockAgent(providerID string, maxTokens int64, runFunc func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error)) *mockSessionAgent {
	return &mockSessionAgent{
		model: Model{
			CatwalkCfg: catwalk.Model{
				DefaultMaxTokens: maxTokens,
			},
			ModelCfg: config.SelectedModel{
				Provider: providerID,
			},
		},
		runFunc: runFunc,
	}
}

// agentResultWithText creates a minimal AgentResult with the given text response.
func agentResultWithText(text string) *fantasy.AgentResult {
	return &fantasy.AgentResult{
		Response: fantasy.Response{
			Content: fantasy.ResponseContent{
				fantasy.TextContent{Text: text},
			},
		},
	}
}

func TestRunSubAgent(t *testing.T) {
	const providerID = "test-provider"
	providerCfg := config.ProviderConfig{ID: providerID}

	t.Run("happy path", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
			assert.Equal(t, "do something", call.Prompt)
			assert.Equal(t, int64(4096), call.MaxOutputTokens)
			return agentResultWithText("done"), nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "do something",
			SessionTitle:   "Test Session",
		})
		require.NoError(t, err)
		assert.Equal(t, "done", resp.Content)
		assert.False(t, resp.IsError)
	})

	t.Run("cost update failure preserves output", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		// Unlike upstream, CreateTaskSession requires the parent session to
		// exist, so we can't start from a missing parent. Instead, delete the
		// parent while the sub-agent runs: output is already produced by the
		// time updateParentSessionCost fails to look the parent up.
		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			require.NoError(t, env.sessions.Delete(t.Context(), parentSession.ID))
			return agentResultWithText("output before cost failure"), nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		assert.Equal(t, "output before cost failure", resp.Content)
	})

	t.Run("response with text returns it", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return agentResultWithText("the answer"), nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		assert.Equal(t, "the answer", resp.Content)
	})

	t.Run("nil result returns error response", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return nil, nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Equal(t, "Sub-agent completed but produced no text output.", resp.Content)
	})

	t.Run("empty result returns error response", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return &fantasy.AgentResult{}, nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Equal(t, "Sub-agent completed but produced no text output.", resp.Content)
	})

	t.Run("ModelCfg.MaxTokens overrides default", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		agent := &mockSessionAgent{
			model: Model{
				CatwalkCfg: catwalk.Model{
					DefaultMaxTokens: 4096,
				},
				ModelCfg: config.SelectedModel{
					Provider:  providerID,
					MaxTokens: 8192,
				},
			},
			runFunc: func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
				assert.Equal(t, int64(8192), call.MaxOutputTokens)
				return agentResultWithText("ok"), nil
			},
		}

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.Equal(t, "ok", resp.Content)
	})

	t.Run("session creation failure with canceled context", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, nil)

		// Use a canceled context to trigger CreateTaskSession failure.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err = coord.runSubAgent(ctx, subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.Error(t, err)
	})

	t.Run("provider not configured", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		// Agent references a provider that doesn't exist in config.
		agent := newMockAgent("unknown-provider", 4096, nil)

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model provider not configured")
	})

	t.Run("agent run error returns error response", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return nil, errors.New("provider request failed")
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		// runSubAgent returns (errorResponse, nil) when agent.Run fails — not a Go error.
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Equal(t, "Failed to generate response: provider request failed", resp.Content)
	})

	t.Run("session setup callback is invoked", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		var setupCalledWith string
		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return agentResultWithText("ok"), nil
		})

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
			SessionSetup: func(sessionID string) {
				setupCalledWith = sessionID
			},
		})
		require.NoError(t, err)
		assert.NotEmpty(t, setupCalledWith, "SessionSetup should have been called")
	})

	t.Run("cost propagation to parent session", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
			// Simulate the agent incurring cost by updating the child session.
			childSession, err := env.sessions.Get(ctx, call.SessionID)
			if err != nil {
				return nil, err
			}
			childSession.Cost = 0.05
			_, err = env.sessions.Save(ctx, childSession)
			if err != nil {
				return nil, err
			}
			return agentResultWithText("ok"), nil
		})

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parentSession.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.05, updated.Cost, 1e-9)
	})
}

func TestUpdateParentSessionCost(t *testing.T) {
	t.Run("accumulates cost correctly", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		child, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child")
		require.NoError(t, err)

		// Set child cost.
		child.Cost = 0.10
		_, err = env.sessions.Save(t.Context(), child)
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child.ID, parent.ID)
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.10, updated.Cost, 1e-9)
	})

	t.Run("accumulates multiple child costs", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		child1, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child1")
		require.NoError(t, err)
		child1.Cost = 0.05
		_, err = env.sessions.Save(t.Context(), child1)
		require.NoError(t, err)

		child2, err := env.sessions.CreateTaskSession(t.Context(), "tool-2", parent.ID, "Child2")
		require.NoError(t, err)
		child2.Cost = 0.03
		_, err = env.sessions.Save(t.Context(), child2)
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child1.ID, parent.ID)
		require.NoError(t, err)
		err = coord.updateParentSessionCost(t.Context(), child2.ID, parent.ID)
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.08, updated.Cost, 1e-9)
	})

	t.Run("child session not found", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), "non-existent", parent.ID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get child session")
	})

	t.Run("parent session not found", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)
		child, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child")
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child.ID, "non-existent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get parent session")
	})

	t.Run("zero cost handled correctly", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)
		child, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child")
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child.ID, parent.ID)
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.0, updated.Cost, 1e-9)
	})
}

func TestGetProviderOptionsReasoningEffort(t *testing.T) {
	// Bedrock is Fantasy's Anthropic under a different provider name; options
	// must land under anthropic.Name so the Anthropic language model picks them up.
	tests := []struct {
		name         string
		providerType catwalk.Type
	}{
		{"anthropic honors reasoning_effort", catwalk.Type(anthropic.Name)},
		{"bedrock honors reasoning_effort", catwalk.Type(bedrock.Name)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := Model{
				CatwalkCfg: catwalk.Model{
					ID:              "claude-opus-4-7",
					CanReason:       true,
					ReasoningLevels: []string{"max"},
				},
				ModelCfg: config.SelectedModel{
					Provider:        "test",
					ReasoningEffort: "max",
				},
			}
			providerCfg := config.ProviderConfig{ID: "test", Type: tc.providerType}

			opts := getProviderOptions(model, providerCfg)

			raw, ok := opts[anthropic.Name]
			require.True(t, ok, "options should be keyed under anthropic.Name for type %q", tc.providerType)
			parsed, ok := raw.(*anthropic.ProviderOptions)
			require.True(t, ok)
			require.NotNil(t, parsed.Effort)
			assert.Equal(t, anthropic.Effort("max"), *parsed.Effort)
		})
	}
}

func TestAgentIDToName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   string
		want string
	}{
		{"single word", "explorer", "Explorer"},
		{"single word 2", "oracle", "Oracle"},
		{"hyphenated", "devils-advocate", "Devils Advocate"},
		{"single word 3", "fixer", "Fixer"},
		{"empty string", "", ""},
		{"leading hyphen", "-leading", "Leading"},
		{"trailing hyphen", "trailing-", "Trailing"},
		{"double hyphen", "a--b", "A B"},
		{"triple hyphen", "---", ""},
		{"single char", "a", "A"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, agentIDToName(tc.id))
		})
	}
}

func TestReloadPluginsPreservesStateOnFailure(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	cfg, err := config.Init(workingDir, "", false)
	require.NoError(t, err)

	pluginDir := filepath.Join(workingDir, "plug")
	skillDir := filepath.Join(pluginDir, "skills", "plugin-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: plugin-skill\ndescription: plugin skill\n---\nUse it.\n"), 0o644))
	cfg.Config().Plugins = []config.PluginConfig{{Path: pluginDir}}

	oldSkill := &skills.Skill{Name: "old-skill", Description: "old"}
	coord := &coordinator{
		cfg:          cfg,
		allSkills:    []*skills.Skill{oldSkill},
		activeSkills: []*skills.Skill{oldSkill},
		skillStates:  []*skills.SkillState{{Name: "old-skill", State: skills.StateNormal}},
		skillTracker: skills.NewTracker([]*skills.Skill{oldSkill}),
		agentConfigs: map[string]config.Agent{}, // Missing orchestrator forces reload failure after discovery.
		agentMDs:     map[string]prompt.AgentMD{},
		agents:       csync.NewMap[string, SessionAgent](),
	}

	err = coord.ReloadPlugins(t.Context())
	require.Error(t, err)
	require.Equal(t, []*skills.Skill{oldSkill}, coord.activeSkills)
	require.Equal(t, []*skills.SkillState{{Name: "old-skill", State: skills.StateNormal}}, coord.skillStates)
}

func TestMergeSkillsPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		userPaths []string
		plugins   []*plugin.Plugin
		want      []string
	}{
		{
			name:      "nil plugins",
			userPaths: []string{"/user/skills"},
			plugins:   nil,
			want:      []string{"/user/skills"},
		},
		{
			name:      "empty user paths and no plugins",
			userPaths: nil,
			plugins:   nil,
			want:      nil,
		},
		{
			name:      "plugin with empty SkillsPath is skipped",
			userPaths: []string{"/user/skills"},
			plugins:   []*plugin.Plugin{{Name: "no-skills", SkillsPath: ""}},
			want:      []string{"/user/skills"},
		},
		{
			name:      "plugin skills paths appended",
			userPaths: []string{"/user/skills"},
			plugins: []*plugin.Plugin{
				{Name: "p1", SkillsPath: "/plugins/p1/skills"},
				{Name: "p2", SkillsPath: "/plugins/p2/skills"},
			},
			want: []string{"/user/skills", "/plugins/p1/skills", "/plugins/p2/skills"},
		},
		{
			name:      "mixed plugins with and without skills",
			userPaths: []string{"/a", "/b"},
			plugins: []*plugin.Plugin{
				{Name: "has-skills", SkillsPath: "/plugins/x/skills"},
				{Name: "no-skills", SkillsPath: ""},
				{Name: "also-has", SkillsPath: "/plugins/y/skills"},
			},
			want: []string{"/a", "/b", "/plugins/x/skills", "/plugins/y/skills"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := mergeSkillsPaths(tt.userPaths, tt.plugins)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestGetProviderOptionsReasoningEffortFallback(t *testing.T) {
	t.Parallel()

	model := Model{
		CatwalkCfg: catwalk.Model{
			ID:              "glm-5.2",
			CanReason:       true,
			ReasoningLevels: []string{"high", "max"},
		},
		ModelCfg: config.SelectedModel{
			Provider: "zai",
		},
	}
	providerCfg := config.ProviderConfig{
		ID:   string(catwalk.InferenceProviderZAI),
		Type: openaicompat.Name,
	}

	opts := getProviderOptions(model, providerCfg)

	raw, ok := opts[openaicompat.Name]
	require.True(t, ok)
	parsed, ok := raw.(*openaicompat.ProviderOptions)
	require.True(t, ok)
	require.NotNil(t, parsed.ReasoningEffort)
	assert.Equal(t, "high", string(*parsed.ReasoningEffort))

	thinking, ok := parsed.ExtraBody["thinking"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "enabled", thinking["type"])
}

// TestNewCoordinatorDoesNotBlockOnMCPInit verifies that NewCoordinator returns
// without waiting for MCP initialisation to complete. This is a deterministic
// regression test: MCP init is armed (so WaitForInit would block) but
// Initialize is never called (so initDone is never closed). NewCoordinator
// must return within the safety timeout regardless, because the startup build
// passes waitForMCP=false to buildAgent.
//
// Do NOT add t.Parallel() — this test mutates toolsmcp package-level state.
func TestNewCoordinatorDoesNotBlockOnMCPInit(t *testing.T) {
	const (
		providerID = "openai-compat-test"
		modelID    = "test-model"
	)

	// Reset toolsmcp package state before and after the test so that the armed
	// channel does not bleed into subsequent tests.
	toolsmcp.ResetInitForTest()
	t.Cleanup(toolsmcp.ResetInitForTest)

	// Arm MCP init without ever calling Initialize: WaitForInit will block on
	// the initDone channel until the context is cancelled. A coordinator built
	// with waitForMCP=true would hang here.
	toolsmcp.ArmInit()

	env := testEnv(t)

	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)

	// Configure a minimal openai-compat provider with a model so that
	// buildAgentModels succeeds without network calls.
	providerCfg := config.ProviderConfig{
		ID:   providerID,
		Type: openaicompat.Name,
		Models: []catwalk.Model{{
			ID:               modelID,
			ContextWindow:    10000,
			DefaultMaxTokens: 1000,
		}},
	}
	cfg.Config().Providers.Set(providerID, providerCfg)
	cfg.Config().Models = map[config.SelectedModelType]config.SelectedModel{
		config.SelectedModelTypeLarge: {Provider: providerID, Model: modelID},
		config.SelectedModelTypeSmall: {Provider: providerID, Model: modelID},
	}

	// Use a context with a deadline beyond the safety timeout so that a
	// blocked WaitForInit goroutine is eventually unblocked and cleaned up if
	// the test times out.
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	type coordinatorResult struct {
		c   Coordinator
		err error
	}
	done := make(chan coordinatorResult, 1)
	go func() {
		c, buildErr := NewCoordinator(
			ctx,
			cfg,
			env.sessions,
			env.messages,
			env.permissions,
			*env.filetracker,
			nil, // lspManager — unused during construction.
			nil, // notify — unused during construction.
		)
		done <- coordinatorResult{c, buildErr}
	}()

	const safetyTimeout = 10 * time.Second
	select {
	case res := <-done:
		// NewCoordinator returned before MCP init completed: the startup
		// build correctly skips WaitForInit.
		require.NoError(t, res.err, "NewCoordinator should succeed, not just return quickly with an error")
	case <-time.After(safetyTimeout):
		t.Fatal("NewCoordinator blocked for >10s while MCP init was pending — likely regressed to waiting for MCP init at startup")
	}
}
