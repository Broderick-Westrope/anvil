package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ValidateFiles checks that the config files Load would read for
// workingDir and dataDir parse and pass structural validation. It is safe to
// run against untrusted project config: it never constructs a variable
// resolver (so no `$(...)` runs), never applies config env, never resolves
// providers or reaches the network, never opens the database, and never
// writes. Provider and model configuration is skipped because it needs the
// resolver, so a config that only fails there passes.
func ValidateFiles(workingDir, dataDir string, validateBouncer func(*Bouncer) error) error {
	// Resolve the trusted paths first, as Load does.
	bouncer, err := loadBouncerBlock(trustedConfigPaths())
	if err != nil {
		return err
	}
	if bouncer != nil && validateBouncer != nil {
		if err := validateBouncer(bouncer); err != nil {
			return fmt.Errorf("invalid bouncer configuration: %w", err)
		}
	}

	// Permission rules are checked while unmarshalling the merged config
	// (Permissions.UnmarshalJSON), and agent filter lists inside
	// loadFromConfigPaths.
	configPaths := lookupConfigs(workingDir)
	cfg, _, err := loadFromConfigPaths(configPaths)
	if err != nil {
		return fmt.Errorf("failed to load config from paths %v: %w", configPaths, err)
	}
	cfg.setDefaults(workingDir, dataDir)

	// Mirror Load's workspace merge, including ignoring a merge error.
	workspacePath := filepath.Join(cfg.Options.ProjectDirectory, fmt.Sprintf("%s.json", appName))
	if wsData, err := os.ReadFile(workspacePath); err == nil && len(wsData) > 0 {
		if !json.Valid(wsData) {
			return fmt.Errorf("invalid JSON in config file %s", workspacePath)
		}
		merged, mergeErr := loadFromBytes(append([][]byte{mustMarshalConfig(cfg)}, wsData))
		if mergeErr == nil {
			projectDir := cfg.Options.ProjectDirectory
			*cfg = *merged
			cfg.setDefaults(workingDir, projectDir)
		}
	}

	if err := cfg.ValidateHooks(); err != nil {
		return fmt.Errorf("invalid hook configuration: %w", err)
	}
	if err := cfg.ValidateMCPAuth(); err != nil {
		return fmt.Errorf("invalid MCP auth configuration: %w", err)
	}
	return nil
}
