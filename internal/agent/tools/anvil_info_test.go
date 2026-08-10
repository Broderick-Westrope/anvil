package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/lsp"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/stretchr/testify/require"
)

func TestAnvilInfo_MinimalConfig(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
	})
	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.NotContains(t, output, "[providers]")
	require.NotContains(t, output, "[lsp]")
	require.NotContains(t, output, "[mcp]")
	require.NotContains(t, output, "[permissions]")
	require.NotContains(t, output, "[tools]")
}

func TestAnvilInfo_ConfigFiles(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(
		&config.Config{Providers: csync.NewMap[string, config.ProviderConfig]()},
		"/home/user/.config/anvil/anvil.json",
		"/project/.anvil/anvil.json",
	)
	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.Contains(t, output, "[config_files]")
	require.Contains(t, output, "/home/user/.config/anvil/anvil.json")
	require.Contains(t, output, "/project/.anvil/anvil.json")
}

func TestAnvilInfo_Models(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(&config.Config{
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeLarge: {Model: "claude-sonnet-4-20250514", Provider: "anthropic"},
			config.SelectedModelTypeSmall: {Model: "claude-haiku-3-20250307", Provider: "anthropic"},
		},
		Providers: csync.NewMap[string, config.ProviderConfig](),
	})
	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.Contains(t, output, "[model]")
	require.Contains(t, output, "large = claude-sonnet-4-20250514 (anthropic)")
	require.Contains(t, output, "small = claude-haiku-3-20250307 (anthropic)")
}

func TestAnvilInfo_Providers(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("openai", config.ProviderConfig{Models: make([]catwalk.Model, 8)})
	providers.Set("anthropic", config.ProviderConfig{Models: make([]catwalk.Model, 12)})

	cfg := config.NewTestStore(&config.Config{Providers: providers})
	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.Contains(t, output, "[providers]")
	anthropicIdx := strings.Index(output, "anthropic = enabled")
	openaiIdx := strings.Index(output, "openai = enabled")
	require.Greater(t, anthropicIdx, -1)
	require.Greater(t, openaiIdx, -1)
	require.Less(t, anthropicIdx, openaiIdx, "anthropic should appear before openai")
	require.Contains(t, output, "anthropic = enabled (12 models)")
	require.Contains(t, output, "openai = enabled (8 models)")
}

func TestAnvilInfo_DisabledProvidersOmitted(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("openai", config.ProviderConfig{Disable: true, Models: make([]catwalk.Model, 8)})
	providers.Set("anthropic", config.ProviderConfig{Models: make([]catwalk.Model, 12)})

	cfg := config.NewTestStore(&config.Config{Providers: providers})
	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.Contains(t, output, "anthropic = enabled")
	require.NotContains(t, output, "openai")
}

func TestAnvilInfo_LSPStates(t *testing.T) {
	t.Parallel()

	mgr := lsp.NewManager(config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
	}))
	readyClient := &lsp.Client{}
	readyClient.SetServerState(lsp.StateReady)
	mgr.Clients().Set("gopls", readyClient)

	errorClient := &lsp.Client{}
	errorClient.SetServerState(lsp.StateError)
	mgr.Clients().Set("pyright", errorClient)

	cfg := config.NewTestStore(&config.Config{Providers: csync.NewMap[string, config.ProviderConfig]()})
	output := buildAnvilInfo(cfg, mgr, nil, nil, nil)
	require.Contains(t, output, "[lsp]")
	require.Contains(t, output, "gopls = ready")
	require.Contains(t, output, "pyright = error")
	goplsIdx := strings.Index(output, "gopls = ready")
	pyrightIdx := strings.Index(output, "pyright = error")
	require.Less(t, goplsIdx, pyrightIdx, "gopls should appear before pyright")
}

func TestAnvilInfo_MCPStates(t *testing.T) {
	t.Parallel()

	connectedAt := time.Date(2025, 1, 15, 15, 4, 5, 0, time.UTC)
	states := map[string]mcp.ClientInfo{
		"github": {
			Name:        "github",
			State:       mcp.StateConnected,
			Counts:      mcp.Counts{Tools: 42, Resources: 7},
			ConnectedAt: connectedAt,
		},
		"filesystem": {
			Name:  "filesystem",
			State: mcp.StateError,
			Error: errors.New("connection refused"),
		},
	}

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
	})

	var b strings.Builder
	writeMCP(&b, states, cfg)
	output := b.String()
	require.Contains(t, output, "[mcp]")
	require.Contains(t, output, "filesystem = error: connection refused")
	require.Contains(t, output, "github = connected (42 tools, 7 resources) since 15:04:05")
	filesystemIdx := strings.Index(output, "filesystem")
	githubIdx := strings.Index(output, "github")
	require.Less(t, filesystemIdx, githubIdx, "filesystem should appear before github")
}

func TestAnvilInfo_YoloMode(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(&config.Config{
		Providers:   csync.NewMap[string, config.ProviderConfig](),
		Permissions: &config.Permissions{},
	})
	cfg.Overrides().YoloLevel = config.YoloStandard

	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.Contains(t, output, "[permissions]")
	require.Contains(t, output, "mode = yolo (standard)")
}

func TestAnvilInfo_AllowedTools(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
		Permissions: &config.Permissions{Rules: []config.PermissionRule{
			{ToolPattern: "bash", Action: config.PermissionAllow},
			{ToolPattern: "edit", Action: config.PermissionAllow},
		}},
	})

	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.Contains(t, output, "[permissions]")
	require.Contains(t, output, "bash = allow")
	require.Contains(t, output, "edit = allow")
}

func TestAnvilInfo_DisabledTools(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
		Options:   &config.Options{DisabledTools: []string{"sourcegraph", "agentic_fetch"}},
	})

	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.Contains(t, output, "[tools]")
	require.Contains(t, output, "disabled = agentic_fetch, sourcegraph")
}

func TestAnvilInfo_Options(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
		Options: &config.Options{
			ProjectDirectory:     "/Users/user/project/.anvil",
			Debug:                true,
			DisableAutoSummarize: true,
		},
	})

	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.Contains(t, output, "[options]")
	require.Contains(t, output, "auto_lsp = true")
	require.Contains(t, output, "auto_summarize = false")
	require.Contains(t, output, "project_directory = /Users/user/project/.anvil")
	require.Contains(t, output, "debug = true")
}

func TestAnvilInfo_AutoSummarizeInversion(t *testing.T) {
	t.Parallel()

	cfgFalse := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
		Options:   &config.Options{DisableAutoSummarize: true},
	})
	outputFalse := buildAnvilInfo(cfgFalse, nil, nil, nil, nil)
	require.Contains(t, outputFalse, "auto_summarize = false")

	cfgTrue := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
		Options:   &config.Options{DisableAutoSummarize: false},
	})
	outputTrue := buildAnvilInfo(cfgTrue, nil, nil, nil, nil)
	require.Contains(t, outputTrue, "auto_summarize = true")
}

func TestAnvilInfo_NoSecrets(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("openai", config.ProviderConfig{
		APIKey: "sk-super-secret-key-12345",
		Models: make([]catwalk.Model, 8),
	})

	cfg := config.NewTestStore(&config.Config{Providers: providers})
	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.NotContains(t, output, "sk-super-secret-key-12345")
	require.NotContains(t, output, "secret")
	require.Contains(t, output, "openai = enabled (8 models)")
}

func TestAnvilInfo_DeterministicOrdering(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("zebra", config.ProviderConfig{Models: make([]catwalk.Model, 1)})
	providers.Set("alpha", config.ProviderConfig{Models: make([]catwalk.Model, 2)})
	providers.Set("middle", config.ProviderConfig{Models: make([]catwalk.Model, 3)})

	states := map[string]mcp.ClientInfo{
		"z-mcp": {Name: "z-mcp", State: mcp.StateConnected, Counts: mcp.Counts{Tools: 1}},
		"a-mcp": {Name: "a-mcp", State: mcp.StateConnected, Counts: mcp.Counts{Tools: 2}},
	}

	cfg := config.NewTestStore(&config.Config{
		Providers: providers,
		Options:   &config.Options{DisabledTools: []string{"z-tool", "a-tool"}},
		Permissions: &config.Permissions{
			Rules: []config.PermissionRule{
				{ToolPattern: "z-perm", Action: config.PermissionAllow},
				{ToolPattern: "a-perm", Action: config.PermissionAllow},
			},
		},
	})
	cfg.Overrides().YoloLevel = config.YoloStandard

	// Test MCP ordering via writeMCP directly.
	var mcpBuf strings.Builder
	writeMCP(&mcpBuf, states, cfg)
	mcpOutput := mcpBuf.String()
	aMcpIdx := strings.Index(mcpOutput, "a-mcp = connected")
	zMcpIdx := strings.Index(mcpOutput, "z-mcp = connected")
	require.Less(t, aMcpIdx, zMcpIdx)

	output := buildAnvilInfo(cfg, nil, nil, nil, nil)

	alphaIdx := strings.Index(output, "alpha = enabled")
	middleIdx := strings.Index(output, "middle = enabled")
	zebraIdx := strings.Index(output, "zebra = enabled")
	require.Less(t, alphaIdx, middleIdx)
	require.Less(t, middleIdx, zebraIdx)

	require.Contains(t, output, "disabled = a-tool, z-tool")
	require.Contains(t, output, "z-perm = allow")
	require.Contains(t, output, "a-perm = allow")

	// Verify insertion order is preserved (z-perm before a-perm).
	zIdx := strings.Index(output, "z-perm = allow")
	aIdx := strings.Index(output, "a-perm = allow")
	require.Less(t, zIdx, aIdx, "permission rules should preserve insertion order")
}

func TestAnvilInfo_EmptySectionsOmitted(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(&config.Config{
		Providers:   csync.NewMap[string, config.ProviderConfig](),
		Permissions: &config.Permissions{},
		Options:     &config.Options{},
	})

	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.NotContains(t, output, "[tools]")
	require.NotContains(t, output, "[permissions]")
	require.NotContains(t, output, "[lsp]")
	require.NotContains(t, output, "[mcp]")
	require.NotContains(t, output, "[skills]")
}

func TestAnvilInfo_ConfigStaleness_Clean(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "anvil.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0o600))

	store := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
	}, configPath)

	// Capture snapshot (normally done in Load)
	store.CaptureStalenessSnapshot([]string{configPath})

	output := buildAnvilInfo(store, nil, nil, nil, nil)
	require.Contains(t, output, "[config]")
	require.Contains(t, output, "dirty = false")
	require.NotContains(t, output, "changed_paths")
	require.NotContains(t, output, "missing_paths")
}

func TestAnvilInfo_ConfigStaleness_Dirty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "anvil.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": false}`), 0o600))

	store := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
	}, configPath)

	// Capture initial snapshot
	store.CaptureStalenessSnapshot([]string{configPath})

	// Modify file to trigger dirty state
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(configPath, []byte(`{"debug": true}`), 0o600))

	output := buildAnvilInfo(store, nil, nil, nil, nil)
	require.Contains(t, output, "[config]")
	require.Contains(t, output, "dirty = true")
	require.Contains(t, output, "changed_paths")
	require.Contains(t, output, configPath)
}

func TestAnvilInfo_ConfigStaleness_MissingPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "anvil.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{}`), 0o600))

	store := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
	}, configPath)

	// Capture initial snapshot
	store.CaptureStalenessSnapshot([]string{configPath})

	// Delete file to trigger missing state
	require.NoError(t, os.Remove(configPath))

	output := buildAnvilInfo(store, nil, nil, nil, nil)
	require.Contains(t, output, "[config]")
	require.Contains(t, output, "dirty = true")
	require.Contains(t, output, "missing_paths")
	require.Contains(t, output, configPath)
}

func TestAnvilInfo_Skills_NoSkills(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
	})
	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.NotContains(t, output, "[skills]")
}

func TestAnvilInfo_Skills_MixedLoadedUnloaded(t *testing.T) {
	t.Parallel()

	allSkills := []*skills.Skill{
		{Name: "go-doc"},
		{Name: "bash"},
		{Name: "anvil-config", Source: skills.SourceBuiltin},
	}
	activeSkills := allSkills

	tracker := skills.NewTracker(activeSkills)
	tracker.MarkLoaded("bash")
	tracker.MarkLoaded("anvil-config")

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
	})
	output := buildAnvilInfo(cfg, nil, allSkills, activeSkills, tracker)
	require.Contains(t, output, "[skills]")
	require.Contains(t, output, "bash = user, loaded")
	require.Contains(t, output, "anvil-config = builtin, loaded")
	require.Contains(t, output, "go-doc = user, unloaded")
}

func TestAnvilInfo_Skills_DisabledSkills(t *testing.T) {
	t.Parallel()

	allSkills := []*skills.Skill{
		{Name: "bash"},
		{Name: "anvil-config", Source: skills.SourceBuiltin},
		{Name: "image-convert"},
	}
	activeSkills := []*skills.Skill{
		{Name: "bash"},
		{Name: "anvil-config", Source: skills.SourceBuiltin},
	}

	tracker := skills.NewTracker(activeSkills)

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
		Options:   &config.Options{DisabledSkills: []string{"image-convert"}},
	})
	output := buildAnvilInfo(cfg, nil, allSkills, activeSkills, tracker)
	require.Contains(t, output, "[skills]")
	require.Contains(t, output, "bash = user, unloaded")
	require.Contains(t, output, "anvil-config = builtin, unloaded")
	require.Contains(t, output, "image-convert = user, disabled")
}

func TestAnvilInfo_Skills_Ordering(t *testing.T) {
	t.Parallel()

	allSkills := []*skills.Skill{
		{Name: "z-skill"},
		{Name: "a-skill", Source: skills.SourceBuiltin},
		{Name: "m-skill"},
	}
	activeSkills := allSkills
	tracker := skills.NewTracker(activeSkills)

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
	})
	output := buildAnvilInfo(cfg, nil, allSkills, activeSkills, tracker)

	aIdx := strings.Index(output, "a-skill")
	mIdx := strings.Index(output, "m-skill")
	zIdx := strings.Index(output, "z-skill")
	require.Less(t, aIdx, mIdx)
	require.Less(t, mIdx, zIdx)
}

func TestAnvilInfo_Skills_BuiltinOrigin(t *testing.T) {
	t.Parallel()

	allSkills := []*skills.Skill{
		{Name: "anvil-config", Source: skills.SourceBuiltin},
		{Name: "my-skill"},
	}
	activeSkills := allSkills
	tracker := skills.NewTracker(activeSkills)

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
	})
	output := buildAnvilInfo(cfg, nil, allSkills, activeSkills, tracker)
	require.Contains(t, output, "anvil-config = builtin, unloaded")
	require.Contains(t, output, "my-skill = user, unloaded")
}

func TestAnvilInfo_Skills_PluginSource(t *testing.T) {
	t.Parallel()

	allSkills := []*skills.Skill{
		{Name: "ce-helpers", Source: "plugin:ce"},
		{Name: "my-skill"},
	}
	activeSkills := allSkills
	tracker := skills.NewTracker(activeSkills)

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
	})
	output := buildAnvilInfo(cfg, nil, allSkills, activeSkills, tracker)
	require.Contains(t, output, "[skills]")
	// Plugin source should show short form "ce", not full "plugin:ce".
	require.Contains(t, output, "ce-helpers = ce, unloaded")
	require.NotContains(t, output, "plugin:ce")
	require.Contains(t, output, "my-skill = user, unloaded")
}

func TestAnvilInfo_Hooks(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
		Hooks: map[string][]config.HookConfig{
			"PreToolUse": {
				{Command: "check-privates.sh", Matcher: "edit|write"},
				{Command: "audit.sh"},
			},
		},
	})

	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.Contains(t, output, "[hooks]")
	require.Contains(t, output, "PreToolUse (matcher: edit|write) = check-privates.sh")
	require.Contains(t, output, "PreToolUse = audit.sh")
}

func TestAnvilInfo_Hooks_NoHooks(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(&config.Config{
		Providers: csync.NewMap[string, config.ProviderConfig](),
	})

	output := buildAnvilInfo(cfg, nil, nil, nil, nil)
	require.NotContains(t, output, "[hooks]")
}
