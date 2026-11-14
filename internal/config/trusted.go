package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/qjebbs/go-jsons"
	"github.com/tidwall/gjson"
)

// trustedConfigPaths returns the user-level config files user-only settings
// may be read from. It reads the process env, so it must only be called
// before any config-provided env has been applied.
func trustedConfigPaths() []string {
	return []string{systemConfigPath, GlobalConfigData(), GlobalConfig()}
}

// userSettings are the settings only user-level config files may set,
// besides the bouncer, whose API key capture makes it its own case.
type userSettings struct {
	yolo    YoloMode
	secrets *Secrets
}

// loadUserSettings reads the user-only settings from the trusted paths.
func loadUserSettings(paths []string) (userSettings, error) {
	yolo, err := loadTrustedYolo(paths)
	if err != nil {
		return userSettings{}, err
	}
	secrets, err := loadTrustedSecrets(paths)
	if err != nil {
		return userSettings{}, err
	}
	return userSettings{yolo: yolo, secrets: secrets}, nil
}

// applyUserSettings replaces whatever the full merge produced for the
// user-only settings with the trusted values.
func (c *Config) applyUserSettings(u userSettings) {
	c.applyTrustedYolo(u.yolo)
	c.applyTrustedSecrets(u.secrets)
}

// loadTrustedBlock merges the top-level object named key from each of
// paths, later paths winning. It returns nil when no path sets it.
func loadTrustedBlock(paths []string, key string) ([]byte, error) {
	var blocks [][]byte
	for _, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("failed to open config file %s: %w", path, err)
		}
		if len(data) == 0 {
			continue
		}
		if !json.Valid(data) {
			return nil, fmt.Errorf("invalid JSON in config file %s", path)
		}
		block := gjson.GetBytes(data, key)
		switch {
		case !block.Exists() || block.Type == gjson.Null:
			continue
		case !block.IsObject():
			return nil, fmt.Errorf("%s in %s must be an object", key, path)
		}
		blocks = append(blocks, []byte(block.Raw))
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	merged, err := jsons.Merge(blocks)
	if err != nil {
		return nil, fmt.Errorf("failed to merge %s: %w", key, err)
	}
	return merged, nil
}
