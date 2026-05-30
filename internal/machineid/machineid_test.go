package machineid

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGet_ReturnsNonEmpty(t *testing.T) {
	t.Parallel()
	got := Get()
	require.NotEmpty(t, got)
}

func TestGet_ReturnsCachedValue(t *testing.T) {
	t.Parallel()
	first := Get()
	second := Get()
	require.Equal(t, first, second)
}
