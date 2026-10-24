package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/prompt"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

// TestTaskTool captures slog, so it must not run in parallel.
func TestTaskTool(t *testing.T) {
	env := testEnv(t)
	c, _ := newReloadTestCoordinator(t, env)
	parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
	require.NoError(t, err)

	validOverride := reloadTestProvider + "/" + reloadTestModel
	c.orchestratorMu.Lock()
	helperCfg := c.agentConfigs["helper"]
	c.agentConfigs["fixer"] = helperCfg
	c.agentConfigs["explorer"] = helperCfg
	c.agentMDs["fixer"] = prompt.AgentMD{Name: "fixer", DelegatesTo: []string{"explorer"}}
	c.agentConfigs["summarizer"] = helperCfg
	c.agentMDs["summarizer"] = prompt.AgentMD{Name: "summarizer", BranchSummarizer: true}
	// The orchestrator may delegate to anyone, even with an agent file
	// that lists no delegates.
	c.agentMDs[config.AgentOrchestrator] = prompt.AgentMD{Name: config.AgentOrchestrator}
	c.orchestratorMu.Unlock()
	reply := func(text string) *mockSessionAgent {
		return newMockAgent(reloadTestProvider, 1000, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
			return agentResultWithText(text), nil
		})
	}
	c.agents.Set("helper|2", reply("helper done"))
	c.agents.Set("helper|2|"+validOverride, reply("override done"))
	c.agents.Set("explorer|1", reply("explorer done"))
	c.agents.Set("helper|1", reply("helper at depth 1 done"))

	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, parent.ID)
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "msg-1")
	longPrompt := strings.Repeat("p", 150)

	tests := map[string]struct {
		caller  string
		depth   int
		ctx     context.Context
		params  TaskParams
		want    string
		wantErr string
		wantLog string
		noLog   string
	}{
		"missing prompt": {
			params: TaskParams{SubagentType: "helper"},
			want:   "prompt is required",
		},
		"missing subagent type": {
			params: TaskParams{Prompt: "p"},
			want:   "subagent_type is required",
		},
		"unknown subagent type": {
			params: TaskParams{Prompt: "p", SubagentType: "nope"},
			want:   `unknown subagent_type "nope"; valid types: explorer, fixer, helper`,
		},
		"branch summarizer": {
			depth:  3,
			params: TaskParams{Prompt: "p", SubagentType: "summarizer"},
			want:   `agent "summarizer" summarizes branches and can't be delegated to`,
		},
		"specialist delegating outside delegates_to": {
			caller: "fixer",
			params: TaskParams{Prompt: "p", SubagentType: "helper"},
			want:   `agent "fixer" is not allowed to delegate to "helper"; allowed: explorer`,
		},
		"specialist delegating inside delegates_to": {
			caller: "fixer",
			params: TaskParams{Prompt: "p", SubagentType: "explorer"},
			want:   "explorer done",
		},
		"specialist without an agent file": {
			caller: "explorer",
			params: TaskParams{Prompt: "p", SubagentType: "helper"},
			want:   "helper at depth 1 done",
		},
		"orchestrator": {
			depth:   3,
			params:  TaskParams{Prompt: longPrompt, SubagentType: "helper"},
			want:    "helper done",
			wantLog: "task_summary=" + strings.Repeat("p", 100) + "\n",
		},
		"valid model override": {
			depth:  3,
			params: TaskParams{Prompt: "p", SubagentType: "helper", Model: validOverride},
			want:   "override done",
			noLog:  "Ignoring invalid model override",
		},
		"invalid model override": {
			depth:   3,
			params:  TaskParams{Prompt: "p", SubagentType: "helper", Model: "missing/model"},
			want:    "helper done",
			wantLog: "Ignoring invalid model override",
		},
		"missing session": {
			depth:   3,
			ctx:     context.WithValue(ctx, tools.SessionIDContextKey, ""),
			params:  TaskParams{Prompt: "p", SubagentType: "helper"},
			wantErr: "session id missing from context",
		},
		"missing message": {
			depth:   3,
			ctx:     context.WithValue(ctx, tools.MessageIDContextKey, ""),
			params:  TaskParams{Prompt: "p", SubagentType: "helper"},
			wantErr: "agent message id missing from context",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(logger) })
			caller := tt.caller
			if caller == "" {
				caller = config.AgentOrchestrator
			}
			depth := tt.depth
			if depth == 0 {
				depth = 2
			}
			runCtx := tt.ctx
			if runCtx == nil {
				runCtx = ctx
			}
			task, err := c.taskTool(runCtx, caller, depth)
			require.NoError(t, err)
			input, err := json.Marshal(tt.params)
			require.NoError(t, err)
			resp, err := task.Run(runCtx, fantasy.ToolCall{ID: "call-" + name, Name: TaskToolName, Input: string(input)})
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, withoutSubagentID(resp.Content))
			if tt.wantLog != "" {
				require.Contains(t, logs.String(), tt.wantLog)
			}
			if tt.noLog != "" {
				require.NotContains(t, logs.String(), tt.noLog)
			}
		})
	}
}

var subagentIDFooter = regexp.MustCompile(`\n\nsubagent_id: \S+$`)

// withoutSubagentID strips the handle every task result ends with.
func withoutSubagentID(content string) string {
	return subagentIDFooter.ReplaceAllString(content, "")
}
