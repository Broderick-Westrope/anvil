package config

import (
	"fmt"
	"strings"
)

// ParseFilterList resolves the final set of allowed items given an input
// filter list and the full set of available items.
//
//   - nil input → return allItems (default = all)
//   - ["*"] → return allItems
//   - [] (empty slice) → return empty slice
//   - All positive (["a", "b"]) → return intersection with allItems
//   - All negative (["!a", "!b"]) → return allItems minus negated items
//   - Mixed positive + negative → return error
func ParseFilterList(input []string, allItems []string) ([]string, error) {
	if input == nil {
		return allItems, nil
	}
	if len(input) == 0 {
		return []string{}, nil
	}
	if len(input) == 1 && input[0] == "*" {
		return allItems, nil
	}
	if err := ValidateFilterList(input); err != nil {
		return nil, err
	}
	// Determine mode by inspecting the first item.
	if strings.HasPrefix(input[0], "!") {
		// Exclude mode: start with all items, remove negated ones.
		excluded := make(map[string]bool, len(input))
		for _, item := range input {
			excluded[strings.TrimPrefix(item, "!")] = true
		}
		result := make([]string, 0, len(allItems))
		for _, item := range allItems {
			if !excluded[item] {
				result = append(result, item)
			}
		}
		return result, nil
	}
	// Include mode: return intersection with allItems preserving allItems order.
	allowed := make(map[string]bool, len(input))
	for _, item := range input {
		allowed[item] = true
	}
	result := make([]string, 0, len(input))
	for _, item := range allItems {
		if allowed[item] {
			result = append(result, item)
		}
	}
	return result, nil
}

// ValidateFilterList checks that an input filter list does not mix positive
// and negative entries. It does not require the full items list.
func ValidateFilterList(input []string) error {
	if len(input) == 0 {
		return nil
	}
	if len(input) == 1 && input[0] == "*" {
		return nil
	}
	hasPositive := false
	hasNegative := false
	for _, item := range input {
		if strings.HasPrefix(item, "!") {
			hasNegative = true
		} else {
			hasPositive = true
		}
		if hasPositive && hasNegative {
			return fmt.Errorf("filter list mixes positive and negative entries: %v", input)
		}
	}
	return nil
}
