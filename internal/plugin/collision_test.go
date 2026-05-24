package plugin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// testItem implements NamedItem for testing.
type testItem struct {
	name        string
	source      string
	displayName string
}

func (t *testItem) ItemName() string        { return t.name }
func (t *testItem) ItemSource() string      { return t.source }
func (t *testItem) SetDisplayName(n string) { t.displayName = n }

func TestDetectCollisions_NoCollision(t *testing.T) {
	t.Parallel()
	items := []*testItem{
		{name: "foo", source: "plugin:a"},
		{name: "bar", source: "plugin:b"},
		{name: "baz", source: ""},
	}
	DetectCollisions(items)
	require.Empty(t, items[0].displayName)
	require.Empty(t, items[1].displayName)
	require.Empty(t, items[2].displayName)
}

func TestDetectCollisions_TwoPluginsSameName(t *testing.T) {
	t.Parallel()
	items := []*testItem{
		{name: "foo", source: "plugin:a"},
		{name: "foo", source: "plugin:b"},
	}
	DetectCollisions(items)
	require.Equal(t, "a:foo", items[0].displayName)
	require.Empty(t, items[1].displayName)
}

func TestDetectCollisions_PluginVsUser(t *testing.T) {
	t.Parallel()
	// User (empty source) is last → highest priority.
	items := []*testItem{
		{name: "foo", source: "plugin:ce"},
		{name: "foo", source: ""},
	}
	DetectCollisions(items)
	require.Equal(t, "ce:foo", items[0].displayName)
	require.Empty(t, items[1].displayName)
}

func TestDetectCollisions_PluginVsBuiltin(t *testing.T) {
	t.Parallel()
	// Plugin is last → highest priority.
	items := []*testItem{
		{name: "foo", source: "builtin"},
		{name: "foo", source: "plugin:ce"},
	}
	DetectCollisions(items)
	require.Equal(t, "builtin:foo", items[0].displayName)
	require.Empty(t, items[1].displayName)
}

func TestDetectCollisions_ThreeWay(t *testing.T) {
	t.Parallel()
	items := []*testItem{
		{name: "foo", source: "builtin"},
		{name: "foo", source: "plugin:ce"},
		{name: "foo", source: ""},
	}
	DetectCollisions(items)
	require.Equal(t, "builtin:foo", items[0].displayName)
	require.Equal(t, "ce:foo", items[1].displayName)
	require.Empty(t, items[2].displayName)
}

func TestShortSource(t *testing.T) {
	t.Parallel()
	require.Equal(t, "ce", shortSource("plugin:ce"))
	require.Equal(t, "builtin", shortSource("builtin"))
	require.Equal(t, "user", shortSource(""))
	require.Equal(t, "project", shortSource("project"))
	require.Equal(t, "plugin", shortSource("plugin:"))
}

func TestDetectCollisions_EmptySlice(t *testing.T) {
	t.Parallel()

	// An empty slice should not panic.
	require.NotPanics(t, func() {
		DetectCollisions([]*testItem{})
	})
}

func TestDetectCollisions_SingleItem(t *testing.T) {
	t.Parallel()

	// A single item has no collision partner, so DisplayName must stay empty.
	item := &testItem{name: "foo", source: "plugin:a"}
	DetectCollisions([]*testItem{item})
	require.Empty(t, item.displayName)
}
