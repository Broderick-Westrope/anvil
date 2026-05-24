package autocomplete

import (
	"slices"
	"strings"
)

// ItemType distinguishes builtins, commands, and skills in the dropdown.
type ItemType int

const (
	// BuiltinItem represents a built-in slash command (e.g. /new, /sessions).
	BuiltinItem ItemType = iota
	// CommandItem represents a user-defined custom command.
	CommandItem
	// SkillItem represents an active skill.
	SkillItem
)

// Item represents one entry in the autocomplete dropdown.
type Item struct {
	Name         string
	DisplayName  string // May include prefix on collision.
	Description  string
	ArgumentHint string // Optional inline argument hint shown after the name.
	Type         ItemType
	// Opaque ID for execution — the handler maps this back to
	// the domain object. Avoids importing commands/skills packages.
	ID string // e.g., "cmd:commit" or "skill:grilling"
}

// Autocomplete manages the dropdown state.
type Autocomplete struct {
	items    []Item
	filtered []Item
	query    string
	selected int
	visible  bool
	maxItems int
	styles   Styles
}

// New creates a new Autocomplete with the given items and maximum visible items.
// If maxItems is 0, it defaults to 10.
func New(items []Item, maxItems int) *Autocomplete {
	if maxItems == 0 {
		maxItems = 10
	}

	filtered := make([]Item, len(items))
	copy(filtered, items)

	return &Autocomplete{
		items:    items,
		filtered: filtered,
		maxItems: maxItems,
		visible:  false,
	}
}

// SetItems replaces the item list and re-applies the current query filter.
// Used when commands or skills are loaded asynchronously.
func (a *Autocomplete) SetItems(items []Item) {
	a.items = make([]Item, len(items))
	copy(a.items, items)
	a.applyFilter(a.query)
}

// SetQuery filters items using a subsequence match against the query.
// An empty query shows all items. Selected index resets to 0.
func (a *Autocomplete) SetQuery(q string) {
	a.query = q
	a.applyFilter(q)
}

// applyFilter filters and sorts items according to query.
func (a *Autocomplete) applyFilter(q string) {
	a.selected = 0

	if q == "" {
		a.filtered = make([]Item, len(a.items))
		copy(a.filtered, a.items)
		return
	}

	type scored struct {
		item  Item
		score int
	}

	var matches []scored
	for _, item := range a.items {
		ok, score := fuzzyMatch(q, item.Name)
		if ok {
			matches = append(matches, scored{item: item, score: score})
		}
	}

	// Sort: lower score first (prefix < subsequence), then builtins
	// before commands before skills within the same tier.
	slices.SortStableFunc(matches, func(a, b scored) int {
		if a.score != b.score {
			return a.score - b.score
		}
		// Within same tier, builtins sort before commands, commands
		// before skills.
		if a.item.Type != b.item.Type {
			return int(a.item.Type) - int(b.item.Type)
		}
		return 0
	})

	a.filtered = make([]Item, 0, len(matches))
	for _, m := range matches {
		a.filtered = append(a.filtered, m.item)
	}
}

// MoveUp decrements the selected index, wrapping from first to last.
func (a *Autocomplete) MoveUp() {
	if len(a.filtered) == 0 {
		return
	}
	a.selected--
	if a.selected < 0 {
		a.selected = len(a.filtered) - 1
	}
}

// MoveDown increments the selected index, wrapping from last to first.
func (a *Autocomplete) MoveDown() {
	if len(a.filtered) == 0 {
		return
	}
	a.selected = (a.selected + 1) % len(a.filtered)
}

// Selected returns a pointer to the currently selected filtered item,
// or nil if there are no filtered items.
func (a *Autocomplete) Selected() *Item {
	if len(a.filtered) == 0 {
		return nil
	}
	item := a.filtered[a.selected]
	return &item
}

// Visible returns whether the dropdown is currently visible.
func (a *Autocomplete) Visible() bool {
	return a.visible
}

// Show makes the dropdown visible.
func (a *Autocomplete) Show() {
	a.visible = true
}

// Hide makes the dropdown not visible.
func (a *Autocomplete) Hide() {
	a.visible = false
}

// Query returns the current filter query.
func (a *Autocomplete) Query() string {
	return a.query
}

// FilteredItems returns the current filtered item slice.
func (a *Autocomplete) FilteredItems() []Item {
	return a.filtered
}

// Len returns the number of filtered items.
func (a *Autocomplete) Len() int {
	return len(a.filtered)
}

// fuzzyMatch reports whether query is a subsequence of target (case-insensitive)
// and returns a score: 0 for an exact prefix match, 1 for a subsequence match.
func fuzzyMatch(query, target string) (bool, int) {
	q := strings.ToLower(query)
	t := strings.ToLower(target)

	if q == "" {
		return true, 1
	}

	// Check exact prefix match first.
	if strings.HasPrefix(t, q) {
		return true, 0
	}

	// Subsequence match: every character of q must appear in order in t.
	qi := 0
	qRunes := []rune(q)
	for _, r := range t {
		if qi < len(qRunes) && r == qRunes[qi] {
			qi++
		}
	}
	if qi == len(qRunes) {
		return true, 1
	}

	return false, 0
}
