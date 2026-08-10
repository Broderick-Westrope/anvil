package config

import "fmt"

// YoloLevel controls how permission checks are handled.
type YoloLevel int

const (
	// YoloOff means all permissions are enforced normally.
	YoloOff YoloLevel = iota
	// YoloStandard promotes ask → allow, but deny is still respected.
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
