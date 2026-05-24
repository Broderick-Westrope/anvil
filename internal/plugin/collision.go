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
// the highest-priority item (last in the slice) keeps an empty display name
// (callers should fall back to ItemName()). Lower-priority items get their
// display name set to "<source>:<name>". Items with unique names are not
// modified. T must be a pointer type so that SetDisplayName mutations are
// visible to the caller.
func DetectCollisions[T NamedItem](items []T) {
	// Group items by name.
	type entry struct {
		item T
	}
	groups := make(map[string][]entry, len(items))
	for _, item := range items {
		groups[item.ItemName()] = append(groups[item.ItemName()], entry{item})
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
				// Highest priority — display name stays empty (bare Name is used).
				continue
			}
			// Lower priority — prefix with short source label.
			prefix := shortSource(e.item.ItemSource())
			e.item.SetDisplayName(prefix + ":" + e.item.ItemName())
		}
	}
}

// shortSource extracts a short display label from a source string.
// "plugin:ce" → "ce", "builtin" → "builtin", "" → "user", "project" → "project".
func shortSource(source string) string {
	if source == "" {
		return "user"
	}
	if after, ok := strings.CutPrefix(source, "plugin:"); ok {
		if after == "" {
			return "plugin"
		}
		return after
	}
	return source
}
