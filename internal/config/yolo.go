package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/tidwall/gjson"
)

// YoloLevel controls how permission checks are handled.
type YoloLevel int

const (
	// YoloOff means all permissions are enforced normally.
	YoloOff YoloLevel = iota
	// YoloStandard promotes ask → allow, but deny is still respected.
	// With an enforcing bouncer, the bouncer decides first
	// and yolo only approves what it would escalate to the human.
	YoloStandard
	// YoloFull bypasses all permissions entirely.
	YoloFull
)

func (y YoloLevel) String() string {
	switch y {
	case YoloOff:
		return "off"
	case YoloStandard:
		return "standard"
	case YoloFull:
		return "full"
	default:
		return fmt.Sprintf("YoloLevel(%d)", int(y))
	}
}

// YoloMode is the config spelling of a YoloLevel, used for the default
// level a session starts at when no --yolo flag is given.
type YoloMode string

const (
	YoloModeOff      YoloMode = "off"
	YoloModeStandard YoloMode = "standard"
	YoloModeFull     YoloMode = "full"
)

// Level returns the YoloLevel for m. An unset mode is YoloOff.
func (m YoloMode) Level() (YoloLevel, error) {
	switch m {
	case "", YoloModeOff:
		return YoloOff, nil
	case YoloModeStandard:
		return YoloStandard, nil
	case YoloModeFull:
		return YoloFull, nil
	default:
		return YoloOff, fmt.Errorf("yolo %q must be one of off, standard, full", string(m))
	}
}

// loadTrustedYolo reads the yolo default from the trusted user-level config
// paths, later paths winning. It returns "" when no path sets it.
func loadTrustedYolo(paths []string) (YoloMode, error) {
	var mode YoloMode
	for _, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("failed to open config file %s: %w", path, err)
		}
		if len(data) == 0 {
			continue
		}
		if !json.Valid(data) {
			return "", fmt.Errorf("invalid JSON in config file %s", path)
		}
		value := gjson.GetBytes(data, "yolo")
		switch {
		case !value.Exists() || value.Type == gjson.Null:
			continue
		case value.Type != gjson.String:
			return "", fmt.Errorf("yolo in %s must be a string", path)
		}
		mode = YoloMode(value.Str)
		if _, err := mode.Level(); err != nil {
			return "", fmt.Errorf("invalid config file %s: %w", path, err)
		}
	}
	return mode, nil
}

// applyTrustedYolo replaces whatever yolo default the full merge produced
// with the trusted one, warning when a non-trusted file tried to change it.
func (c *Config) applyTrustedYolo(trusted YoloMode) {
	if c.Yolo != trusted {
		slog.Warn("Ignoring yolo from project config; it is only read from user-level config")
	}
	c.Yolo = trusted
}

// ParseYoloLevel parses a string flag value into a YoloLevel.
// "" or "false" → YoloOff, "true" → YoloStandard, "full" → YoloFull.
func ParseYoloLevel(s string) (YoloLevel, error) {
	switch s {
	case "", "false":
		return YoloOff, nil
	case "true":
		return YoloStandard, nil
	case "full":
		return YoloFull, nil
	default:
		return YoloOff, fmt.Errorf("invalid yolo level: %q (valid values: true, false, full)", s)
	}
}
