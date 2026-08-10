package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestStoreForPath creates a ConfigStore wired to a single config file
// at the given path. The file does not need to exist yet.
func newTestStoreForPath(t *testing.T, path string) *ConfigStore {
	t.Helper()
	return &ConfigStore{
		config:         &Config{},
		globalDataPath: path,
	}
}

func TestSetPermissionRule_EmptyConfigFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "anvil.json")

	store := newTestStoreForPath(t, path)
	err := store.SetPermissionRule(ScopeGlobal, "bash", "", PermissionAllow)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NotNil(t, cfg.Permissions)
	require.Len(t, cfg.Permissions.Rules, 1)
	require.Equal(t, "bash", cfg.Permissions.Rules[0].ToolPattern)
	require.Equal(t, PermissionAllow, cfg.Permissions.Rules[0].Action)
}

func TestSetPermissionRule_NonexistentFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	subdir := filepath.Join(dir, "nested", "deep")
	path := filepath.Join(subdir, "anvil.json")

	store := newTestStoreForPath(t, path)
	err := store.SetPermissionRule(ScopeGlobal, "view", "", PermissionAllow)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NotNil(t, cfg.Permissions)
	require.Len(t, cfg.Permissions.Rules, 1)
	require.Equal(t, "view", cfg.Permissions.Rules[0].ToolPattern)
}

func TestSetPermissionRule_ToolLevelRule(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "anvil.json")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))

	store := newTestStoreForPath(t, path)
	err := store.SetPermissionRule(ScopeGlobal, "bash", "", PermissionAllow)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NotNil(t, cfg.Permissions)
	require.Len(t, cfg.Permissions.Rules, 1)
	require.Equal(t, "bash", cfg.Permissions.Rules[0].ToolPattern)
	require.Equal(t, PermissionAllow, cfg.Permissions.Rules[0].Action)
	require.Empty(t, cfg.Permissions.Rules[0].SubRules)
}

func TestSetPermissionRule_InputLevelRule(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "anvil.json")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))

	store := newTestStoreForPath(t, path)
	err := store.SetPermissionRule(ScopeGlobal, "bash", "git *", PermissionAllow)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NotNil(t, cfg.Permissions)
	require.Len(t, cfg.Permissions.Rules, 1)
	require.Equal(t, "bash", cfg.Permissions.Rules[0].ToolPattern)
	require.Len(t, cfg.Permissions.Rules[0].SubRules, 1)
	require.Equal(t, "git *", cfg.Permissions.Rules[0].SubRules[0].InputPattern)
	require.Equal(t, PermissionAllow, cfg.Permissions.Rules[0].SubRules[0].Action)
}

func TestSetPermissionRule_DotsInPattern(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "anvil.json")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))

	store := newTestStoreForPath(t, path)
	err := store.SetPermissionRule(ScopeGlobal, "edit", "*.go", PermissionDeny)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NotNil(t, cfg.Permissions)
	require.Len(t, cfg.Permissions.Rules, 1)
	require.Equal(t, "edit", cfg.Permissions.Rules[0].ToolPattern)
	require.Len(t, cfg.Permissions.Rules[0].SubRules, 1)
	require.Equal(t, "*.go", cfg.Permissions.Rules[0].SubRules[0].InputPattern)
	require.Equal(t, PermissionDeny, cfg.Permissions.Rules[0].SubRules[0].Action)
}

func TestSetPermissionRule_PreservesOtherFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "anvil.json")
	initial := `{"$schema": "https://example.com/schema.json"}`
	require.NoError(t, os.WriteFile(path, []byte(initial), 0o600))

	store := newTestStoreForPath(t, path)
	err := store.SetPermissionRule(ScopeGlobal, "bash", "", PermissionAllow)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Contains(t, raw, "$schema")

	var schema string
	require.NoError(t, json.Unmarshal(raw["$schema"], &schema))
	require.Equal(t, "https://example.com/schema.json", schema)
}

func TestSetPermissionRule_UpdateExistingRule(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "anvil.json")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))

	store := newTestStoreForPath(t, path)

	// Write initial rule.
	require.NoError(t, store.SetPermissionRule(ScopeGlobal, "bash", "", PermissionAllow))

	// Update to deny.
	require.NoError(t, store.SetPermissionRule(ScopeGlobal, "bash", "", PermissionDeny))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NotNil(t, cfg.Permissions)
	require.Len(t, cfg.Permissions.Rules, 1)
	require.Equal(t, PermissionDeny, cfg.Permissions.Rules[0].Action)
}

func TestSetPermissionRule_AddSubRuleToExistingTool(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "anvil.json")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))

	store := newTestStoreForPath(t, path)

	// Add first sub-rule.
	require.NoError(t, store.SetPermissionRule(ScopeGlobal, "bash", "git *", PermissionAllow))

	// Add second sub-rule for same tool.
	require.NoError(t, store.SetPermissionRule(ScopeGlobal, "bash", "rm *", PermissionDeny))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NotNil(t, cfg.Permissions)
	require.Len(t, cfg.Permissions.Rules, 1)
	require.Len(t, cfg.Permissions.Rules[0].SubRules, 2)
	require.Equal(t, "git *", cfg.Permissions.Rules[0].SubRules[0].InputPattern)
	require.Equal(t, PermissionAllow, cfg.Permissions.Rules[0].SubRules[0].Action)
	require.Equal(t, "rm *", cfg.Permissions.Rules[0].SubRules[1].InputPattern)
	require.Equal(t, PermissionDeny, cfg.Permissions.Rules[0].SubRules[1].Action)
}

func TestSetPermissionRule_UpdateExistingSubRule(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "anvil.json")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))

	store := newTestStoreForPath(t, path)

	// Add sub-rule.
	require.NoError(t, store.SetPermissionRule(ScopeGlobal, "bash", "git *", PermissionAllow))

	// Update same sub-rule.
	require.NoError(t, store.SetPermissionRule(ScopeGlobal, "bash", "git *", PermissionDeny))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NotNil(t, cfg.Permissions)
	require.Len(t, cfg.Permissions.Rules, 1)
	require.Len(t, cfg.Permissions.Rules[0].SubRules, 1)
	require.Equal(t, "git *", cfg.Permissions.Rules[0].SubRules[0].InputPattern)
	require.Equal(t, PermissionDeny, cfg.Permissions.Rules[0].SubRules[0].Action)
}

func TestSetPermissionRule_GlobalScopeWritesUserConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	userConfigPath := filepath.Join(dir, "config", "anvil.json")
	dataPath := filepath.Join(dir, "data", "anvil.json")

	store := &ConfigStore{
		config:           &Config{},
		globalConfigPath: userConfigPath,
		globalDataPath:   dataPath,
	}

	require.NoError(t, store.SetPermissionRule(ScopeGlobal, "bash", "git *", PermissionAllow))

	// The rule must land in the user config, not the data file.
	data, err := os.ReadFile(userConfigPath)
	require.NoError(t, err)

	var cfg Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.NotNil(t, cfg.Permissions)
	require.Len(t, cfg.Permissions.Rules, 1)

	_, err = os.ReadFile(dataPath)
	require.ErrorIs(t, err, os.ErrNotExist, "data file must not be created")
}

func TestSetPermissionRule_GlobalScopeFallsBackToDataPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dataPath := filepath.Join(dir, "anvil.json")

	// No globalConfigPath set (e.g. minimal test store).
	store := newTestStoreForPath(t, dataPath)

	require.NoError(t, store.SetPermissionRule(ScopeGlobal, "bash", "", PermissionAllow))

	_, err := os.ReadFile(dataPath)
	require.NoError(t, err, "should fall back to data path when user config path is unset")
}

func TestSetPermissionRule_RejectsInvalidPatterns(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "anvil.json")
	store := newTestStoreForPath(t, path)

	// Invalid tool pattern.
	err := store.SetPermissionRule(ScopeGlobal, "{unclosed", "", PermissionAllow)
	require.ErrorContains(t, err, "invalid tool pattern")

	// Invalid input pattern.
	err = store.SetPermissionRule(ScopeGlobal, "bash", "[unclosed", PermissionAllow)
	require.ErrorContains(t, err, "invalid input pattern")

	// Nothing was written.
	_, err = os.ReadFile(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}
