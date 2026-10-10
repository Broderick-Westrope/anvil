package agent

import (
	"cmp"
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/x/vcr"
	"github.com/Broderick-Westrope/anvil/internal/agent/cacheusage"
	"github.com/Broderick-Westrope/anvil/internal/agent/prompt"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/filetracker"
	"github.com/Broderick-Westrope/anvil/internal/lsp"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"

	_ "github.com/joho/godotenv/autoload"
)

// fakeEnv is an environment for testing.
type fakeEnv struct {
	workingDir  string
	sessions    session.Service
	messages    message.Service
	permissions permission.Service
	filetracker *filetracker.Service
	lspClients  *csync.Map[string, *lsp.Client]
	conn        *sql.DB
	// usage records step usage rows into conn. Close it before querying
	// step_usage so queued rows are written.
	usage *cacheusage.Recorder
}

type builderFunc func(t *testing.T, r *vcr.Recorder) (fantasy.LanguageModel, error)

type modelPair struct {
	name       string
	largeModel builderFunc
	smallModel builderFunc
}

func anthropicBuilder(model string) builderFunc {
	return func(t *testing.T, r *vcr.Recorder) (fantasy.LanguageModel, error) {
		provider, err := anthropic.New(
			anthropic.WithAPIKey(cmp.Or(os.Getenv("ANTHROPIC_API_KEY"), "test-api-key")),
			anthropic.WithHTTPClient(&http.Client{Transport: r}),
		)
		if err != nil {
			return nil, err
		}
		return provider.LanguageModel(t.Context(), model)
	}
}

func testEnv(t *testing.T) fakeEnv {
	workingDir := filepath.Join("/tmp/anvil-test/", t.Name())
	os.RemoveAll(workingDir)

	err := os.MkdirAll(workingDir, 0o755)
	require.NoError(t, err)

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)

	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q, message.WithConn(conn))

	permissions := permission.NewPermissionService(workingDir, config.YoloStandard, nil, nil)
	filetrackerService := filetracker.NewService(q)
	lspClients := csync.NewMap[string, *lsp.Client]()
	usage := cacheusage.New(q)

	t.Cleanup(func() {
		require.NoError(t, usage.Close(context.Background()))
		conn.Close()
		os.RemoveAll(workingDir)
	})

	return fakeEnv{
		workingDir:  workingDir,
		sessions:    sessions,
		messages:    messages,
		permissions: permissions,
		filetracker: &filetrackerService,
		lspClients:  lspClients,
		conn:        conn,
		usage:       usage,
	}
}

func testSessionAgent(env fakeEnv, large, small fantasy.LanguageModel, systemPrompt string, tools ...fantasy.AgentTool) SessionAgent {
	largeModel := Model{
		Model: large,
		CatwalkCfg: catwalk.Model{
			ContextWindow:    200000,
			DefaultMaxTokens: 10000,
		},
	}
	smallModel := Model{
		Model: small,
		CatwalkCfg: catwalk.Model{
			ContextWindow:    200000,
			DefaultMaxTokens: 10000,
		},
	}
	agent := NewSessionAgent(SessionAgentOptions{
		LargeModel:    largeModel,
		SmallModel:    smallModel,
		SystemPrompt:  systemPrompt,
		IsYolo:        true,
		Sessions:      env.sessions,
		Messages:      env.messages,
		Tools:         tools,
		UsageRecorder: env.usage,
		AgentName:     "orchestrator",
		WorkingDir:    env.workingDir,
	})
	return agent
}

func orchestratorAgent(r *vcr.Recorder, env fakeEnv, large, small fantasy.LanguageModel) (SessionAgent, error) {
	fixedTime := func() time.Time {
		t, _ := time.Parse("1/2/2006", "1/1/2025")
		return t
	}
	prompt, err := orchestratorPrompt(
		prompt.WithTimeFunc(fixedTime),
		prompt.WithPlatform("linux"),
		prompt.WithWorkingDir(filepath.ToSlash(env.workingDir)),
	)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Init(env.workingDir, "", false)
	if err != nil {
		return nil, err
	}

	// Clear some fields to avoid issues with VCR cassette matching.
	cfg.Config().Options.SkillsPaths = nil
	cfg.Config().Options.DisabledSkills = []string{"anvil-config"}
	cfg.Config().Options.ContextPaths = nil
	cfg.Config().LSP = nil

	systemPrompt, err := prompt.Build(context.TODO(), large.Provider(), large.Model(), cfg)
	if err != nil {
		return nil, err
	}

	allTools := []fantasy.AgentTool{
		tools.NewBashTool(env.permissions, env.workingDir, nil),
		tools.NewDownloadTool(env.permissions, env.workingDir, r.GetDefaultClient()),
		tools.NewEditTool(nil, env.permissions, *env.filetracker, env.workingDir),
		tools.NewMultiEditTool(nil, env.permissions, *env.filetracker, env.workingDir),
		tools.NewFetchTool(env.permissions, env.workingDir, r.GetDefaultClient()),
		tools.NewGlobTool(env.workingDir, cfg.Config().Tools.Glob),
		tools.NewGrepTool(env.workingDir, cfg.Config().Tools.Grep),
		tools.NewLsTool(env.permissions, env.workingDir, cfg.Config().Tools.Ls),
		tools.NewSourcegraphTool(r.GetDefaultClient()),
		tools.NewViewTool(nil, env.permissions, *env.filetracker, nil, nil, env.workingDir),
		tools.NewWriteTool(nil, env.permissions, *env.filetracker, env.workingDir),
	}

	return testSessionAgent(env, large, small, systemPrompt, allTools...), nil
}

// createSimpleGoProject creates a simple Go project structure in the given directory.
// It creates a go.mod file and a main.go file with a basic hello world program.
func createSimpleGoProject(t *testing.T, dir string) {
	goMod := `module example.com/testproject

go 1.23
`
	err := os.WriteFile(dir+"/go.mod", []byte(goMod), 0o644)
	require.NoError(t, err)

	mainGo := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`
	err = os.WriteFile(dir+"/main.go", []byte(mainGo), 0o644)
	require.NoError(t, err)
}
