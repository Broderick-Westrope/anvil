package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/app"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/stretchr/testify/require"
)

const reloadConfigJSON = `{
	"options": {"disable_provider_auto_update": true},
	"models": {"large": {"provider": "openai", "model": "gpt-4"}},
	"providers": {
		"openai": {"api_key": "test-key", "models": [{"id": "gpt-4", "name": "GPT-4"}]}
	}%s
}`

// rulesPermissions records the config rules pushed into it.
type rulesPermissions struct {
	permission.Service

	mu    sync.Mutex
	rules []config.PermissionRule
}

func (p *rulesPermissions) SetConfigRules(rules []config.PermissionRule) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = rules
}

func (p *rulesPermissions) ResetBouncerCache() {}

func (p *rulesPermissions) toolPatterns() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var patterns []string
	for _, r := range p.rules {
		patterns = append(patterns, r.ToolPattern)
	}
	return patterns
}

// reloadCoordinator stubs the coordinator calls a reload makes.
type reloadCoordinator struct {
	agent.Coordinator

	pauseErr  error
	reloadErr error
	warnings  []agent.PluginWarning

	mu      sync.Mutex
	reloads int
	resumes int
}

func (c *reloadCoordinator) Pause(context.Context, time.Duration) (func(), error) {
	if c.pauseErr != nil {
		return nil, c.pauseErr
	}
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.resumes++
	}, nil
}

func (c *reloadCoordinator) ReloadPlugins(context.Context) ([]agent.PluginWarning, error) {
	c.mu.Lock()
	c.reloads++
	c.mu.Unlock()
	if c.reloadErr != nil {
		return nil, c.reloadErr
	}
	return c.warnings, nil
}

type reloadFixture struct {
	ws         *AppWorkspace
	store      *config.ConfigStore
	perms      *rulesPermissions
	configPath string
}

func newReloadFixture(t *testing.T, coord agent.Coordinator) *reloadFixture {
	t.Helper()
	t.Setenv("ANVIL_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("ANVIL_GLOBAL_DATA", t.TempDir())
	t.Setenv("ANVIL_CACHE_DIR", t.TempDir())

	dir := t.TempDir()
	configPath := filepath.Join(dir, "anvil.json")
	writeReloadConfig(t, configPath, "")
	store, err := config.Load(dir, dir, false)
	require.NoError(t, err)
	require.True(t, store.Config().IsConfigured())

	perms := &rulesPermissions{}
	a := &app.App{Permissions: perms, AgentCoordinator: coord}
	return &reloadFixture{
		ws:         NewAppWorkspace(a, store),
		store:      store,
		perms:      perms,
		configPath: configPath,
	}
}

func writeReloadConfig(t *testing.T, path, extra string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(reloadConfigJSON, extra)), 0o600))
}

const allowViewRule = `,
	"permissions": {"view": "allow"}`

func TestReloadConfigAndPlugins_AppliesNewRule(t *testing.T) {
	coord := &reloadCoordinator{warnings: []agent.PluginWarning{{Path: "bad/plugin.json", Err: errors.New("bad")}}}
	f := newReloadFixture(t, coord)

	writeReloadConfig(t, f.configPath, allowViewRule)
	r := f.ws.ReloadConfigAndPlugins(t.Context())

	require.NoError(t, r.ConfigErr)
	require.NoError(t, r.ApplyErr)
	require.NoError(t, r.PluginsErr)
	require.False(t, r.PluginsSkipped)
	require.False(t, r.PluginsBusy)
	require.Empty(t, r.RestartRequired)
	require.Equal(t, coord.warnings, r.PluginWarnings)
	require.Equal(t, []string{"view"}, f.perms.toolPatterns())
	require.Equal(t, 1, coord.reloads)
	require.Equal(t, 1, coord.resumes, "a successful pause must be resumed")
}

func TestReloadConfigAndPlugins_InvalidJSON(t *testing.T) {
	coord := &reloadCoordinator{}
	f := newReloadFixture(t, coord)
	before := f.store.Config()

	require.NoError(t, os.WriteFile(f.configPath, []byte(`{"options": `), 0o600))
	r := f.ws.ReloadConfigAndPlugins(t.Context())

	require.Error(t, r.ConfigErr)
	require.NoError(t, r.ApplyErr)
	require.NoError(t, r.PluginsErr)
	require.Same(t, before, f.store.Config(), "the previous config stays live")
	require.Nil(t, f.perms.toolPatterns(), "a failed config reload applies nothing")
	require.Equal(t, 1, coord.reloads, "plugins still reload against the old config")
}

func TestReloadConfigAndPlugins_PluginErrorKeepsConfigAndAgents(t *testing.T) {
	coord := &reloadCoordinator{reloadErr: errors.New("delegates_to: unknown agent")}
	f := newReloadFixture(t, coord)
	f.store.SetAgentDefaults(map[string]config.Agent{"reviewer": {ID: "reviewer", Name: "Reviewer"}})

	writeReloadConfig(t, f.configPath, allowViewRule)
	r := f.ws.ReloadConfigAndPlugins(t.Context())

	require.NoError(t, r.ConfigErr)
	require.ErrorContains(t, r.PluginsErr, "delegates_to")
	require.Equal(t, []string{"view"}, f.perms.toolPatterns(), "the config change is kept")
	require.Contains(t, f.store.Config().Agents, "reviewer", "plugin agents are kept")
	require.Equal(t, 1, coord.resumes)
}

func TestReloadConfigAndPlugins_NewMCPReportedEachTime(t *testing.T) {
	f := newReloadFixture(t, &reloadCoordinator{})

	writeReloadConfig(t, f.configPath, `,
	"mcp": {"extra": {"type": "stdio", "command": "extra"}}`)
	for range 2 {
		r := f.ws.ReloadConfigAndPlugins(t.Context())
		require.NoError(t, r.ConfigErr)
		require.Equal(t, []string{"mcp"}, r.RestartRequired)
	}
}

func TestReloadConfigAndPlugins_NoCoordinator(t *testing.T) {
	f := newReloadFixture(t, nil)

	writeReloadConfig(t, f.configPath, allowViewRule)
	r := f.ws.ReloadConfigAndPlugins(t.Context())

	require.NoError(t, r.ConfigErr)
	require.True(t, r.PluginsSkipped)
	require.NoError(t, r.PluginsErr)
	require.Equal(t, []string{"view"}, f.perms.toolPatterns())
}

func TestReloadConfigAndPlugins_Busy(t *testing.T) {
	coord := &reloadCoordinator{pauseErr: agent.ErrBusy}
	f := newReloadFixture(t, coord)

	writeReloadConfig(t, f.configPath, allowViewRule)
	r := f.ws.ReloadConfigAndPlugins(t.Context())

	require.NoError(t, r.ConfigErr)
	require.NoError(t, r.PluginsErr)
	require.True(t, r.PluginsBusy)
	require.Equal(t, []string{"view"}, f.perms.toolPatterns(), "config applies while busy")
	require.Zero(t, coord.reloads, "plugins are not rebuilt without a pause")
}

func TestReloadConfigAndPlugins_Concurrent(t *testing.T) {
	coord := &reloadCoordinator{}
	f := newReloadFixture(t, coord)
	writeReloadConfig(t, f.configPath, allowViewRule)

	reports := make([]ReloadReport, 2)
	var wg sync.WaitGroup
	for i := range reports {
		wg.Go(func() {
			reports[i] = f.ws.ReloadConfigAndPlugins(context.Background())
		})
	}
	wg.Wait()

	for _, r := range reports {
		require.NoError(t, r.ConfigErr)
		require.NoError(t, r.ApplyErr)
		require.NoError(t, r.PluginsErr)
	}
	require.Equal(t, 2, coord.reloads)
	require.Equal(t, []string{"view"}, f.perms.toolPatterns())
}
