package plugin

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
	assert.Empty(t, items[0].displayName)
	assert.Empty(t, items[1].displayName)
	assert.Empty(t, items[2].displayName)
}

func TestDetectCollisions_TwoPluginsSameName(t *testing.T) {
	t.Parallel()
	items := []*testItem{
		{name: "foo", source: "plugin:a"},
		{name: "foo", source: "plugin:b"},
	}
	DetectCollisions(items)
	assert.Equal(t, "a:foo", items[0].displayName)
	assert.Equal(t, "foo", items[1].displayName)
}

func TestDetectCollisions_PluginVsUser(t *testing.T) {
	t.Parallel()
	// User (empty source) is last → highest priority.
	items := []*testItem{
		{name: "foo", source: "plugin:ce"},
		{name: "foo", source: ""},
	}
	DetectCollisions(items)
	assert.Equal(t, "ce:foo", items[0].displayName)
	assert.Equal(t, "foo", items[1].displayName)
}

func TestDetectCollisions_PluginVsBuiltin(t *testing.T) {
	t.Parallel()
	// Plugin is last → highest priority.
	items := []*testItem{
		{name: "foo", source: "builtin"},
		{name: "foo", source: "plugin:ce"},
	}
	DetectCollisions(items)
	assert.Equal(t, "builtin:foo", items[0].displayName)
	assert.Equal(t, "foo", items[1].displayName)
}

func TestDetectCollisions_ThreeWay(t *testing.T) {
	t.Parallel()
	items := []*testItem{
		{name: "foo", source: "builtin"},
		{name: "foo", source: "plugin:ce"},
		{name: "foo", source: ""},
	}
	DetectCollisions(items)
	assert.Equal(t, "builtin:foo", items[0].displayName)
	assert.Equal(t, "ce:foo", items[1].displayName)
	assert.Equal(t, "foo", items[2].displayName)
}

func TestShortSource(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "ce", shortSource("plugin:ce"))
	assert.Equal(t, "builtin", shortSource("builtin"))
	assert.Equal(t, "user", shortSource(""))
	assert.Equal(t, "project", shortSource("project"))
}
