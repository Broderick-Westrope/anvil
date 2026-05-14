package anthropic

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBetasForModel_Default(t *testing.T) {
	t.Parallel()

	// A generic model should receive all four default betas.
	betas := BetasForModel("claude-3-sonnet-20240229")
	require.Equal(t, DefaultBetas, betas)
}

func TestBetasForModel_Haiku(t *testing.T) {
	t.Parallel()

	betas := BetasForModel("claude-3-haiku-20240307")

	// Haiku must not include interleaved-thinking.
	for _, b := range betas {
		require.NotEqual(t, "interleaved-thinking-2025-05-14", b,
			"haiku should not include interleaved-thinking beta")
	}

	// All other default betas must be present.
	for _, d := range DefaultBetas {
		if d == "interleaved-thinking-2025-05-14" {
			continue
		}
		require.Contains(t, betas, d)
	}
}

func TestBetasForModel_46(t *testing.T) {
	t.Parallel()

	betas := BetasForModel("claude-sonnet-4-6-20250514")

	// Should include the effort beta.
	require.Contains(t, betas, "effort-2025-11-24")

	// Should still include all default betas (including interleaved-thinking,
	// since this is not a haiku model).
	for _, d := range DefaultBetas {
		require.Contains(t, betas, d)
	}
}

func TestBetasForModel_47(t *testing.T) {
	t.Parallel()

	betas := BetasForModel("claude-opus-4-7-20251201")
	require.Contains(t, betas, "effort-2025-11-24")
}

func TestBetasForModel_HaikuNoEffort(t *testing.T) {
	t.Parallel()

	// A haiku 4-6 model: excludes interleaved-thinking but adds effort.
	betas := BetasForModel("claude-haiku-4-6-20250514")
	require.Contains(t, betas, "effort-2025-11-24")
	for _, b := range betas {
		require.NotEqual(t, "interleaved-thinking-2025-05-14", b)
	}
}

func TestMergeBetas_NoDuplicates(t *testing.T) {
	t.Parallel()

	existing := "claude-code-20250219,oauth-2025-04-20"
	modelBetas := []string{"oauth-2025-04-20", "effort-2025-11-24"}

	got := MergeBetas(existing, modelBetas)

	parts := strings.Split(got, ",")
	seen := make(map[string]int)
	for _, p := range parts {
		seen[p]++
	}
	for beta, count := range seen {
		require.Equal(t, 1, count, "beta %q appeared more than once", beta)
	}

	require.Contains(t, got, "claude-code-20250219")
	require.Contains(t, got, "oauth-2025-04-20")
	require.Contains(t, got, "effort-2025-11-24")
}

func TestMergeBetas_EmptyExisting(t *testing.T) {
	t.Parallel()

	got := MergeBetas("", []string{"beta-a", "beta-b"})
	require.Equal(t, "beta-a,beta-b", got)
}

func TestMergeBetas_EmptyModel(t *testing.T) {
	t.Parallel()

	got := MergeBetas("beta-a,beta-b", []string{})
	require.Equal(t, "beta-a,beta-b", got)
}

func TestMergeBetas_BothEmpty(t *testing.T) {
	t.Parallel()

	got := MergeBetas("", []string{})
	require.Equal(t, "", got)
}

func TestMergeBetas_PreservesOrder(t *testing.T) {
	t.Parallel()

	// Existing betas come first, then model-specific additions.
	got := MergeBetas("first,second", []string{"third", "first"})
	parts := strings.Split(got, ",")
	require.Equal(t, []string{"first", "second", "third"}, parts)
}
