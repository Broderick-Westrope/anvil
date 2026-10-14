package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"charm.land/fantasy"

	"github.com/Broderick-Westrope/anvil/internal/agent/prompt"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
)

//go:embed templates/agentic_fetch.md
var agenticFetchToolDescription string

//go:embed templates/agentic_fetch_prompt.md.tpl
var agenticFetchPromptTmpl []byte

func (c *coordinator) agenticFetchTool(_ context.Context, client *http.Client) (fantasy.AgentTool, error) {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 100
		transport.MaxIdleConnsPerHost = 10
		transport.IdleConnTimeout = 90 * time.Second

		client = &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		}
	}

	return fantasy.NewParallelAgentTool(
		tools.AgenticFetchToolName,
		agenticFetchToolDescription,
		func(ctx context.Context, params tools.AgenticFetchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Prompt == "" {
				return fantasy.NewTextErrorResponse("prompt is required"), nil
			}
			sessionID, agentMessageID, err := subAgentOrigin(ctx)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			// Determine description based on mode.
			var description string
			if params.URL != "" {
				description = fmt.Sprintf("Fetch and analyze content from URL: %s", params.URL)
			} else {
				description = "Search the web and analyze results"
			}

			p, err := c.permissions.Request(ctx,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        c.cfg.WorkingDir(),
					ToolCallID:  call.ID,
					ToolName:    tools.AgenticFetchToolName,
					Action:      "fetch",
					Description: description,
					Params:      tools.AgenticFetchPermissionsParams(params),
					Input:       params.URL,
				},
			)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !p.Granted {
				return tools.NewPermissionDeniedResponse(p.Reason), nil
			}

			tmpDir, err := os.MkdirTemp(c.cfg.Config().Options.ProjectDirectory, "anvil-fetch-*")
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to create temporary directory: %s", err)), nil
			}
			defer os.RemoveAll(tmpDir)

			fullPrompt, failure := agenticFetchPrompt(ctx, client, tmpDir, params)
			if failure.IsError {
				return failure, nil
			}

			promptOpts := []prompt.Option{
				prompt.WithWorkingDir(tmpDir),
			}

			promptTemplate, err := prompt.NewPrompt("agentic_fetch", string(agenticFetchPromptTmpl), promptOpts...)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error creating prompt: %w", err)
			}

			fetchModel, smallModel, err := c.buildAgenticFetchModel(ctx)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error building models: %w", err)
			}

			systemPrompt, err := promptTemplate.Build(ctx, fetchModel.Model.Provider(), fetchModel.Model.Model(), c.cfg)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error building system prompt: %w", err)
			}

			fetchProviderCfg, ok := c.cfg.Config().Providers.Get(fetchModel.ModelCfg.Provider)
			if !ok {
				return fantasy.ToolResponse{}, errors.New("agentic_fetch model provider not configured")
			}

			webFetchTool := tools.NewWebFetchTool(tmpDir, client)
			webSearchTool := tools.NewWebSearchTool(client)
			fetchTools := []fantasy.AgentTool{
				webFetchTool,
				webSearchTool,
				tools.NewGlobTool(tmpDir, c.cfg.Config().Tools.Glob),
				tools.NewGrepTool(tmpDir, c.cfg.Config().Tools.Grep),
				tools.NewSourcegraphTool(client),
				tools.NewViewTool(c.lspManager, c.permissions, c.filetracker, nil, nil, tmpDir),
			}

			// Sub-agent tools run without hook interception. The top-level
			// `agentic_fetch` call itself is already wrapped from the orchestrator's
			// side; firing hooks again for every inner tool call would run
			// the user's hooks N times per delegated turn.

			agent := NewSessionAgent(SessionAgentOptions{
				admission:            c.admission,
				IsSubAgent:           true,
				LargeModel:           fetchModel,
				SmallModel:           smallModel,
				ProviderConfig:       fetchProviderCfg,
				SystemPromptPrefix:   fetchProviderCfg.SystemPromptPrefix,
				SystemPrompt:         systemPrompt,
				DisableAutoSummarize: c.cfg.Config().Options.DisableAutoSummarize,
				IsYolo:               c.permissions.YoloLevel() != config.YoloOff,
				Sessions:             c.sessions,
				Messages:             c.messages,
				Tools:                fetchTools,
				JobEvents:            c.jobEvents,
				UsageRecorder:        c.usageRecorder,
				AgentName:            config.AgenticFetchAgentID,
				WorkingDir:           c.cfg.WorkingDir(),
			})

			return c.runSubAgent(ctx, subAgentParams{
				Agent:          agent,
				SessionID:      sessionID,
				AgentMessageID: agentMessageID,
				ToolCallID:     call.ID,
				Prompt:         fullPrompt,
				SessionTitle:   "Fetch Analysis",
				OnSession: func(sessionID string) {
					c.permissions.AutoApproveSession(sessionID)
				},
			})
		}), nil
}

// agenticFetchPrompt returns the fetch sub-agent's task. For a URL it
// fetches the page first: a small page goes inline, and a large one is
// saved in dir for the sub-agent to search. Without a URL the sub-agent
// searches the web itself. When a step fails it returns, in place of the
// task, the error response the model sees.
func agenticFetchPrompt(ctx context.Context, client *http.Client, dir string, params tools.AgenticFetchParams) (string, fantasy.ToolResponse) {
	if params.URL == "" {
		return fmt.Sprintf("%s\n\nUse the web_search tool to find relevant information. Break down the question into smaller, focused searches if needed. After searching, use web_fetch to get detailed content from the most relevant results.", params.Prompt), fantasy.ToolResponse{}
	}
	content, err := tools.FetchURLAndConvert(ctx, client, params.URL)
	if err != nil {
		return "", fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to fetch URL: %s", err))
	}
	if len(content) <= tools.LargeContentThreshold {
		return fmt.Sprintf("%s\n\nWeb page URL: %s\n\n<webpage_content>\n%s\n</webpage_content>", params.Prompt, params.URL, content), fantasy.ToolResponse{}
	}
	tempFile, err := os.CreateTemp(dir, "page-*.md")
	if err != nil {
		return "", fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to create temporary file: %s", err))
	}
	defer tempFile.Close()
	if _, err := tempFile.WriteString(content); err != nil {
		return "", fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to write content to file: %s", err))
	}
	return fmt.Sprintf("%s\n\nThe web page from %s has been saved to: %s\n\nUse the view and grep tools to analyze this file and extract the requested information.", params.Prompt, params.URL, tempFile.Name()), fantasy.ToolResponse{}
}
