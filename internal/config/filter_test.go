package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFilterList(t *testing.T) {
	t.Parallel()

	allItems := []string{"a", "b", "c", "d"}

	t.Run("nil returns all items", func(t *testing.T) {
		t.Parallel()
		result, err := ParseFilterList(nil, allItems)
		require.NoError(t, err)
		require.Equal(t, allItems, result)
	})

	t.Run("wildcard returns all items", func(t *testing.T) {
		t.Parallel()
		result, err := ParseFilterList([]string{"*"}, allItems)
		require.NoError(t, err)
		require.Equal(t, allItems, result)
	})

	t.Run("empty returns empty slice", func(t *testing.T) {
		t.Parallel()
		result, err := ParseFilterList([]string{}, allItems)
		require.NoError(t, err)
		require.Empty(t, result)
	})

	t.Run("positive returns intersection in allItems order", func(t *testing.T) {
		t.Parallel()
		result, err := ParseFilterList([]string{"c", "a"}, allItems)
		require.NoError(t, err)
		require.Equal(t, []string{"a", "c"}, result)
	})

	t.Run("positive with unknown items returns only known intersection", func(t *testing.T) {
		t.Parallel()
		result, err := ParseFilterList([]string{"a", "z"}, allItems)
		require.NoError(t, err)
		require.Equal(t, []string{"a"}, result)
	})

	t.Run("negative returns all minus negated", func(t *testing.T) {
		t.Parallel()
		result, err := ParseFilterList([]string{"!a", "!c"}, allItems)
		require.NoError(t, err)
		require.Equal(t, []string{"b", "d"}, result)
	})

	t.Run("single negative returns all minus that item", func(t *testing.T) {
		t.Parallel()
		result, err := ParseFilterList([]string{"!b"}, allItems)
		require.NoError(t, err)
		require.Equal(t, []string{"a", "c", "d"}, result)
	})

	t.Run("mixed positive then negative returns error", func(t *testing.T) {
		t.Parallel()
		result, err := ParseFilterList([]string{"a", "!b"}, allItems)
		require.Error(t, err)
		require.Nil(t, result)
	})

	t.Run("mixed negative then positive returns error", func(t *testing.T) {
		t.Parallel()
		result, err := ParseFilterList([]string{"!a", "b"}, allItems)
		require.Error(t, err)
		require.Nil(t, result)
	})

	t.Run("nil allItems with positive input returns empty", func(t *testing.T) {
		t.Parallel()
		result, err := ParseFilterList([]string{"a"}, nil)
		require.NoError(t, err)
		require.Empty(t, result)
	})

	t.Run("nil allItems with nil input returns nil", func(t *testing.T) {
		t.Parallel()
		result, err := ParseFilterList(nil, nil)
		require.NoError(t, err)
		require.Nil(t, result)
	})
}

func TestValidateFilterList(t *testing.T) {
	t.Parallel()

	t.Run("nil is valid", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, ValidateFilterList(nil))
	})

	t.Run("empty is valid", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, ValidateFilterList([]string{}))
	})

	t.Run("wildcard is valid", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, ValidateFilterList([]string{"*"}))
	})

	t.Run("all positive is valid", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, ValidateFilterList([]string{"a", "b", "c"}))
	})

	t.Run("single positive is valid", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, ValidateFilterList([]string{"a"}))
	})

	t.Run("all negative is valid", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, ValidateFilterList([]string{"!a", "!b", "!c"}))
	})

	t.Run("single negative is valid", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, ValidateFilterList([]string{"!a"}))
	})

	t.Run("positive then negative is invalid", func(t *testing.T) {
		t.Parallel()
		require.Error(t, ValidateFilterList([]string{"a", "!b"}))
	})

	t.Run("negative then positive is invalid", func(t *testing.T) {
		t.Parallel()
		require.Error(t, ValidateFilterList([]string{"!a", "b"}))
	})

	t.Run("mixed with many items is invalid", func(t *testing.T) {
		t.Parallel()
		require.Error(t, ValidateFilterList([]string{"a", "b", "!c", "d"}))
	})
}
