package plugin

import "strings"

// NamedItem is implemented by types that participate in collision detection
// (skills, commands, etc.).
type NamedItem interface {
	ItemName() string
	ItemSource() string
	SetDisplayName(string)
}

// DetectCollisions groups items by name. When multiple items share a name,
// the highest-priority item (last in the slice, since slices are ordered by
// discovery priority with "last wins") keeps the bare name. Lower-priority
// items get their display name prefixed with a short source label.
func DetectCollisions[T NamedItem](items []T) {
	// Group items by name.
	type entry struct {
		index int
		item  T
	}
	groups := make(map[string][]entry, len(items))
	for i, item := range items {
		groups[item.ItemName()] = append(groups[item.ItemName()], entry{i, item})
	}

	for _, entries := range groups {
		if len(entries) <= 1 {
			// No collision — display name stays empty (use Name).
			continue
		}
		// Last entry is highest priority (last wins in discovery order).
		// It keeps the bare name. Others get prefixed.
		for i, e := range entries {
			if i == len(entries)-1 {
				// Highest priority — bare name.
				e.item.SetDisplayName(e.item.ItemName())
			} else {
				// Lower priority — prefix with short source label.
				prefix := shortSource(e.item.ItemSource())
				e.item.SetDisplayName(prefix + ":" + e.item.ItemName())
			}
		}
	}
}

// shortSource extracts a short display label from a source string.
// "plugin:ce" → "ce", "builtin" → "builtin", "" → "user", "project" → "project".
func shortSource(source string) string {
	if source == "" {
		return "user"
	}
	if strings.HasPrefix(source, "plugin:") {
		return strings.TrimPrefix(source, "plugin:")
	}
	return source
}
