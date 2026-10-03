package assessor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTruncate(t *testing.T) {
	t.Parallel()

	require.Equal(t, "héll", truncate("héllo", 4))
	require.Equal(t, "héllo", truncate("héllo", 10))
	require.Empty(t, truncate("héllo", 0))
}
