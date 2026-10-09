package agent

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSessionAgent is a minimal mock for the SessionAgent interface.
type mockSessionAgent struct {
	model     Model
	runFunc   func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error)
	setModels func() // Optional; runs on every SetModels call.
	cancelled []string
}

func (m *mockSessionAgent) Run(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
	return m.runFunc(ctx, call)
}

func (m *mockSessionAgent) Model() Model { return m.model }
func (m *mockSessionAgent) SetModels(large, small Model) {
	if m.setModels != nil {
		m.setModels()
	}
}
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

func (m *mockSessionAgent) RunWake(context.Context, SessionAgentCall, func() bool) (*fantasy.AgentResult, error) {
	return nil, nil
}
func (m *mockSessionAgent) IsSummarizing(string) bool { return false }

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

	t.Run("agent run error hands off jobs", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		var autoID, explicitID string
		agent := newMockAgent(providerID, 4096, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
			autoID = startTestJob(t, call.SessionID, "sleep 30", shell.OriginAuto)
			explicitID = startTestJob(t, call.SessionID, "sleep 30", shell.OriginExplicit)
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
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "Failed to generate response: provider request failed")
		assert.Contains(t, resp.Content, "<background_jobs>")
		requireJobOwner(t, explicitID, parentSession.ID)
		requireJobGone(t, autoID)
	})

	t.Run("cancelled sub-agent hands off jobs", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		var autoID, explicitID string
		agent := newMockAgent(providerID, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
			autoID = startTestJob(t, call.SessionID, "sleep 30", shell.OriginAuto)
			explicitID = startTestJob(t, call.SessionID, "sleep 30", shell.OriginExplicit)
			go cancel()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Second):
				return nil, errors.New("context was not cancelled")
			}
		})

		resp, err := coord.runSubAgent(ctx, subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, context.Canceled.Error())
		requireJobOwner(t, explicitID, parentSession.ID)
		requireJobGone(t, autoID)
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
	agentsDir := filepath.Join(pluginDir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "helper.md"), []byte("---\nrole: helps\n---\nHelp.\n"), 0o644))
	cfg.Config().Plugins = []config.PluginConfig{{Path: pluginDir}}
	agentsBefore := maps.Clone(cfg.Config().Agents)

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

	_, err = coord.ReloadPlugins(t.Context())
	require.Error(t, err)
	require.Equal(t, []*skills.Skill{oldSkill}, coord.activeSkills)
	require.Equal(t, []*skills.SkillState{{Name: "old-skill", State: skills.StateNormal}}, coord.skillStates)
	require.Equal(t, agentsBefore, cfg.Config().Agents)
	require.NotContains(t, cfg.Config().Agents, "helper")
	require.Empty(t, coord.agentConfigs)
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

func TestTrustedReadPathsIncludesCommandDirectories(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Options: &config.Options{
			ProjectDirectory: "/project/.anvil",
			SkillsPaths:      []string{"/user/skills"},
		},
	}
	plugins := []*plugin.Plugin{{Name: "p1", SkillsPath: "/plugins/p1/skills", CommandsPath: "/plugins/p1/commands"}}

	got := trustedReadPaths(cfg, plugins)
	require.Contains(t, got, "/user/skills")
	require.Contains(t, got, "/plugins/p1/skills")
	require.Contains(t, got, filepath.Join("/project/.anvil", "commands"))
	require.Contains(t, got, "/plugins/p1/commands")
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
			nil, // jobEvents — notifications disabled.
			nil, // jobArchive — no persisted job fallbacks.
			nil, // onIdle — no waker.
			nil, // jobWakeEnabled — never wakes.
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

func TestSkillsUsageParity(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		allowed, disabled []string
		want              bool
	}{
		"nil":              {nil, nil, true},
		"wildcard":         {[]string{"*"}, nil, true},
		"empty":            {[]string{}, nil, false},
		"include view":     {[]string{"view", "bash"}, nil, true},
		"include other":    {[]string{"bash"}, nil, false},
		"exclude view":     {[]string{"!view"}, nil, false},
		"exclude other":    {[]string{"!bash"}, nil, true},
		"mixed":            {[]string{"view", "!bash"}, nil, true},
		"global disabled":  {nil, []string{"view"}, false},
		"include disabled": {[]string{"view"}, []string{"view"}, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg, err := config.Init(t.TempDir(), t.TempDir(), false)
			require.NoError(t, err)
			cfg.Config().Options.DisabledTools = tt.disabled
			cfg.Config().Options.ContextPaths = nil
			cfg.Config().MCP = nil
			c := &coordinator{cfg: cfg}
			for _, agentName := range []string{config.AgentOrchestrator, "fixer"} {
				for _, allowedSkills := range [][]string{nil, {}} {
					agentCfg := config.Agent{ID: agentName, AllowedTools: tt.allowed, AllowedSkills: allowedSkills}
					active := []*skills.Skill{{Name: "example", Description: "Example skill", SkillFilePath: "/private/example/SKILL.md"}}
					builtTools, _, err := c.buildToolsWithState(t.Context(), agentCfg, 1, active, active, nil, nil, nil)
					require.NoError(t, err)
					hasView := false
					for _, tool := range builtTools {
						hasView = hasView || tool.Info().Name == tools.ViewToolName
					}
					require.Equal(t, tt.want, hasView)
					p, err := c.buildPromptWithState(agentName, agentCfg, active, nil, nil)
					require.NoError(t, err)
					built, err := p.Build(t.Context(), "test", "test", cfg)
					require.NoError(t, err)
					require.Equal(t, hasView, strings.Contains(built, "<skills_usage>"), agentName)
					require.Equal(t, hasView && agentName == config.AgentOrchestrator, strings.Contains(built, "LOAD MATCHING SKILLS"), agentName)
					require.Equal(t, allowedSkills == nil, strings.Contains(built, "</available_skills>"))
					require.Contains(t, built, "never authorizes delegation, pushes or pull requests")
					require.Contains(t, built, "\n<git_workflow>\n", agentName)
					require.Contains(t, built, "In a linked worktree you are authorized to commit as you go.", agentName)
					require.NotContains(t, built, "<location>")
					require.NotContains(t, built, "/private/example")
				}
			}
		})
	}
}

func TestCoordinatorWaitBackgroundJobsForwards(t *testing.T) {
	orch := &waitingSessionAgent{}
	c := &coordinator{orchestrator: orch, admission: newAdmission(t.Context())}
	c.WaitBackgroundJobs()
	require.True(t, orch.waited)
}

type waitingSessionAgent struct {
	mockSessionAgent
	waited bool
}

func (a *waitingSessionAgent) WaitBackgroundJobs() { a.waited = true }

func TestWithJobTools(t *testing.T) {
	t.Parallel()
	all := []string{"bash", "view", "job_output", "job_kill", "job_list"}
	tests := map[string]struct {
		filter, allowed, want []string
	}{
		"include with bash": {
			filter:  []string{"bash", "view"},
			allowed: []string{"bash", "view"},
			want:    []string{"bash", "view", "job_output", "job_kill", "job_list"},
		},
		"include without bash": {
			filter:  []string{"view"},
			allowed: []string{"view"},
			want:    []string{"view"},
		},
		"exclude job_kill": {
			filter:  []string{"!job_kill"},
			allowed: []string{"bash", "view", "job_output", "job_list"},
			want:    []string{"bash", "view", "job_output", "job_list"},
		},
		"nil filter": {
			filter:  nil,
			allowed: all,
			want:    all,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, withJobTools(tt.filter, slices.Clone(tt.allowed)))
		})
	}
}

func TestJobToolNamesCoversRegisteredJobTools(t *testing.T) {
	t.Parallel()
	cfg, err := config.Init(t.TempDir(), t.TempDir(), false)
	require.NoError(t, err)
	cfg.Config().MCP = nil
	c := &coordinator{cfg: cfg}
	builtTools, _, err := c.buildToolsWithState(t.Context(), config.Agent{ID: "all"}, 1, nil, nil, nil, nil, nil)
	require.NoError(t, err)

	var registered []string
	for _, tool := range builtTools {
		if name := tool.Info().Name; strings.HasPrefix(name, "job_") {
			registered = append(registered, name)
		}
	}
	require.ElementsMatch(t, tools.JobToolNames(), registered,
		"every job_* tool must be listed in tools.JobToolNames so agents with bash get it")
}

func TestBuildToolsAutoGrantsJobTools(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		disabled []string
		want     []string
	}{
		"bash only": {
			want: append([]string{tools.BashToolName}, tools.JobToolNames()...),
		},
		"globally disabled job_kill": {
			disabled: []string{tools.JobKillToolName},
			want:     []string{tools.BashToolName, tools.JobOutputToolName, tools.JobListToolName},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg, err := config.Init(t.TempDir(), t.TempDir(), false)
			require.NoError(t, err)
			cfg.Config().Options.DisabledTools = tt.disabled
			cfg.Config().MCP = nil
			c := &coordinator{cfg: cfg}
			agentCfg := config.Agent{ID: "fixer", AllowedTools: []string{tools.BashToolName}}
			builtTools, _, err := c.buildToolsWithState(t.Context(), agentCfg, 1, nil, nil, nil, nil, nil)
			require.NoError(t, err)
			var names []string
			for _, tool := range builtTools {
				names = append(names, tool.Info().Name)
			}
			require.ElementsMatch(t, tt.want, names)
		})
	}
}

const (
	reloadTestProvider = "openai-compat-test"
	reloadTestModel    = "test-model"
)

// newReloadTestCoordinator builds a real coordinator against a provider that
// needs no network, with one plugin providing the "helper" agent. It
// returns the coordinator and the plugin directory.
func newReloadTestCoordinator(t *testing.T, env fakeEnv) (*coordinator, string) {
	t.Helper()
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)
	cfg.Config().Providers.Set(reloadTestProvider, config.ProviderConfig{
		ID:   reloadTestProvider,
		Type: openaicompat.Name,
		Models: []catwalk.Model{{
			ID:               reloadTestModel,
			ContextWindow:    10000,
			DefaultMaxTokens: 1000,
		}},
	})
	cfg.Config().Models = map[config.SelectedModelType]config.SelectedModel{
		config.SelectedModelTypeLarge: {Provider: reloadTestProvider, Model: reloadTestModel},
		config.SelectedModelTypeSmall: {Provider: reloadTestProvider, Model: reloadTestModel},
	}
	pluginDir := filepath.Join(t.TempDir(), "plug")
	agentsDir := filepath.Join(pluginDir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "helper.md"), []byte("---\nrole: helps\n---\nHelp.\n"), 0o644))
	cfg.Config().Plugins = []config.PluginConfig{{Path: pluginDir}}

	coord, err := NewCoordinator(t.Context(), cfg, env.sessions, env.messages, env.permissions,
		*env.filetracker, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	c := coord.(*coordinator)
	require.Contains(t, c.agentConfigs, "helper")
	return c, pluginDir
}

func (c *coordinator) setOrchestratorForTest(orch SessionAgent) {
	c.orchestratorMu.Lock()
	defer c.orchestratorMu.Unlock()
	c.orchestrator = orch
}

func (c *coordinator) isPausedForTest() bool {
	c.admitMu.Lock()
	defer c.admitMu.Unlock()
	return c.pauses > 0
}

func TestPauseBlocksNewRunsUntilResume(t *testing.T) {
	t.Parallel()
	c := &coordinator{}

	resume, err := c.Pause(t.Context(), time.Second)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, err := c.Run(t.Context(), "s", "hi")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Run was admitted during a pause: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	resume()
	select {
	case err := <-done:
		// Admitted: it gets as far as UpdateModels, which fails on the
		// bare coordinator.
		require.ErrorIs(t, err, errOrchestratorAgentNotConfigured)
	case <-time.After(10 * time.Second):
		t.Fatal("Run was not admitted after resume")
	}
}

func TestPauseWaitsForRunInUpdateModels(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	c, _ := newReloadTestCoordinator(t, env)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	orch := newMockAgent(reloadTestProvider, 1000, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
		return agentResultWithText("ok"), nil
	})
	orch.setModels = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	c.setOrchestratorForTest(orch)

	runDone := make(chan error, 1)
	go func() {
		_, err := c.Run(t.Context(), "s", "hi")
		runDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("Run never reached UpdateModels")
	}

	type pauseResult struct {
		resume func()
		err    error
	}
	pauseDone := make(chan pauseResult, 1)
	go func() {
		resume, err := c.Pause(t.Context(), 10*time.Second)
		pauseDone <- pauseResult{resume, err}
	}()
	select {
	case <-pauseDone:
		t.Fatal("Pause returned while a run was still preparing")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-runDone)
	res := <-pauseDone
	require.NoError(t, res.err)
	res.resume()
}

func TestPauseTimesOutWithErrBusyAndUnpauses(t *testing.T) {
	t.Parallel()
	c := &coordinator{}
	release, err := c.admit(t.Context())
	require.NoError(t, err)

	_, err = c.Pause(t.Context(), 50*time.Millisecond)
	require.ErrorIs(t, err, ErrBusy)
	require.False(t, c.isPausedForTest())

	// New runs are admitted again straight away.
	again, err := c.admit(t.Context())
	require.NoError(t, err)
	again()
	release()
}

func TestPauseNestedPausesResumeTogether(t *testing.T) {
	t.Parallel()
	c := &coordinator{}
	first, err := c.Pause(t.Context(), time.Second)
	require.NoError(t, err)
	second, err := c.Pause(t.Context(), time.Second)
	require.NoError(t, err)

	first()
	first()
	require.True(t, c.isPausedForTest())
	second()
	require.False(t, c.isPausedForTest())
}

func TestPauseWaitingRunsReturnOnCancel(t *testing.T) {
	t.Parallel()
	c := &coordinator{}
	resume, err := c.Pause(t.Context(), time.Second)
	require.NoError(t, err)
	defer resume()

	ctx, cancel := context.WithCancel(t.Context())
	errs := make(chan error, 3)
	go func() {
		_, err := c.Run(ctx, "s", "hi")
		errs <- err
	}()
	go func() {
		_, err := c.RunWake(ctx, "s", func() bool { return true })
		errs <- err
	}()
	go func() {
		errs <- c.Summarize(ctx, "s")
	}()
	cancel()
	for range 3 {
		select {
		case err := <-errs:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(10 * time.Second):
			t.Fatal("a paused run ignored its cancelled context")
		}
	}
}

func TestPauseNestedSubagentDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	c, _ := newReloadTestCoordinator(t, env)

	parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
	require.NoError(t, err)

	sub := newMockAgent(reloadTestProvider, 1000, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
		return agentResultWithText("sub done"), nil
	})
	type pauseResult struct {
		resume func()
		err    error
	}
	pauseDone := make(chan pauseResult, 1)
	toolResp := make(chan fantasy.ToolResponse, 1)
	orch := newMockAgent(reloadTestProvider, 1000, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		go func() {
			resume, err := c.Pause(context.Background(), 10*time.Second)
			pauseDone <- pauseResult{resume, err}
		}()
		if !assert.Eventually(t, c.isPausedForTest, 10*time.Second, time.Millisecond) {
			return nil, errors.New("pause never started")
		}

		// UpdateModels reset the cache at the start of this run, so seed
		// the sub-agent now rather than building a real one.
		c.agents.Set("helper|2", sub)
		task, err := c.taskTool(ctx, config.AgentOrchestrator, 3)
		if err != nil {
			return nil, err
		}
		toolCtx := context.WithValue(ctx, tools.SessionIDContextKey, call.SessionID)
		toolCtx = context.WithValue(toolCtx, tools.MessageIDContextKey, "msg-1")
		resp, err := task.Run(toolCtx, fantasy.ToolCall{
			ID:    "call-1",
			Name:  TaskToolName,
			Input: `{"prompt":"do it","subagent_type":"helper","description":"Sub"}`,
		})
		if err != nil {
			return nil, err
		}
		toolResp <- resp
		return agentResultWithText("ok"), nil
	})
	c.setOrchestratorForTest(orch)

	runDone := make(chan error, 1)
	go func() {
		_, err := c.Run(t.Context(), parent.ID, "delegate")
		runDone <- err
	}()
	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("nested sub-agent deadlocked behind a pending pause")
	}
	resp := <-toolResp
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "sub done")
	res := <-pauseDone
	require.NoError(t, res.err)
	res.resume()
}

func TestReloadPluginsConcurrentWithTaskToolAndSubagentBuild(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	c, _ := newReloadTestCoordinator(t, env)
	ctx := t.Context()

	task, err := c.taskTool(ctx, config.AgentOrchestrator, 3)
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Go(func() {
		for range 5 {
			_, err := c.ReloadPlugins(ctx)
			assert.NoError(t, err)
		}
	})
	wg.Go(func() {
		for range 20 {
			resp, err := task.Run(ctx, fantasy.ToolCall{
				ID:    "call-1",
				Name:  TaskToolName,
				Input: `{"prompt":"do it","subagent_type":"nope"}`,
			})
			assert.NoError(t, err)
			assert.True(t, resp.IsError)
			assert.Contains(t, resp.Content, "helper")
		}
	})
	wg.Go(func() {
		for range 5 {
			_, err := c.getOrBuildAgent(ctx, "helper", 2, "")
			assert.NoError(t, err)
		}
	})
	wg.Wait()
}

func TestReloadPluginsDropsStaleSubagentBuild(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	c, _ := newReloadTestCoordinator(t, env)

	c.orchestratorMu.RLock()
	gen := c.agentsGen
	c.orchestratorMu.RUnlock()

	_, err := c.ReloadPlugins(t.Context())
	require.NoError(t, err)

	stale := newMockAgent(reloadTestProvider, 1000, nil)
	require.False(t, c.cacheAgent("helper|2", gen, stale))
	_, ok := c.agents.Get("helper|2")
	require.False(t, ok, "a build started before the reload was cached")

	c.orchestratorMu.RLock()
	gen = c.agentsGen
	c.orchestratorMu.RUnlock()
	require.True(t, c.cacheAgent("helper|2", gen, stale))
}

func TestReloadPluginsReportsPluginWarnings(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	c, pluginDir := newReloadTestCoordinator(t, env)

	brokenMD := filepath.Join(pluginDir, "agents", "broken.md")
	require.NoError(t, os.WriteFile(brokenMD, []byte("---\ntools: [unclosed\n---\nBody.\n"), 0o644))
	badPlugin := filepath.Join(t.TempDir(), "bad")
	require.NoError(t, os.MkdirAll(badPlugin, 0o755))
	badManifest := filepath.Join(badPlugin, "anvil-plugin.json")
	require.NoError(t, os.WriteFile(badManifest, []byte("{not json"), 0o644))
	c.cfg.Config().Plugins = append(c.cfg.Config().Plugins, config.PluginConfig{Path: badPlugin})

	warnings, err := c.ReloadPlugins(t.Context())
	require.NoError(t, err)
	paths := make([]string, 0, len(warnings))
	for _, w := range warnings {
		require.Error(t, w.Err)
		paths = append(paths, w.Path)
	}
	require.ElementsMatch(t, []string{badManifest, brokenMD}, paths)
	require.Contains(t, c.agentConfigs, "helper")
	require.NotContains(t, c.agentConfigs, "broken")
	require.Contains(t, c.cfg.Config().Agents, "helper")
}
