package anthropic

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputeCCH(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"hello", "hello"},
		{"longer text", "the quick brown fox jumps over the lazy dog"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := ComputeCCH(tc.input)

			// Should always be exactly 5 hex characters.
			require.Len(t, got, 5)

			// Should be valid lowercase hex characters.
			for _, c := range got {
				require.True(t, (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'),
					"expected hex char, got %q in %q", c, got)
			}

			// Should be deterministic.
			require.Equal(t, got, ComputeCCH(tc.input))

			// Should match manual SHA-256 computation.
			h := sha256.Sum256([]byte(tc.input))
			expected := fmt.Sprintf("%x", h)[:5]
			require.Equal(t, expected, got)
		})
	}
}

func TestComputeCCH_KnownValues(t *testing.T) {
	t.Parallel()

	// SHA-256("hello") = 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824.
	require.Equal(t, "2cf24", ComputeCCH("hello"))

	// SHA-256("") = e3b0c44298fc1c149afbf4c8996fb924...
	require.Equal(t, "e3b0c", ComputeCCH(""))
}

func TestComputeVersionSuffix(t *testing.T) {
	t.Parallel()

	t.Run("short text uses fallback zeros", func(t *testing.T) {
		t.Parallel()

		// "hello" has len 5; indices 4 is in range ('o'), 7 and 20 are not.
		got := ComputeVersionSuffix("hello", CLIVersion)
		require.Len(t, got, 3)

		// Verify manually.
		sampled := string([]byte{'o', '0', '0'})
		h := sha256.Sum256([]byte(BillingSalt + sampled + CLIVersion))
		expected := fmt.Sprintf("%x", h)[:3]
		require.Equal(t, expected, got)
	})

	t.Run("long text samples real chars", func(t *testing.T) {
		t.Parallel()

		// Text long enough to cover indices 4, 7, 20.
		text := "abcdefghijklmnopqrstuvwxyz"
		got := ComputeVersionSuffix(text, CLIVersion)
		require.Len(t, got, 3)

		// indices: 4='e', 7='h', 20='u'
		sampled := string([]byte{text[4], text[7], text[20]})
		h := sha256.Sum256([]byte(BillingSalt + sampled + CLIVersion))
		expected := fmt.Sprintf("%x", h)[:3]
		require.Equal(t, expected, got)
	})

	t.Run("deterministic", func(t *testing.T) {
		t.Parallel()

		text := "some system prompt text here"
		require.Equal(t,
			ComputeVersionSuffix(text, CLIVersion),
			ComputeVersionSuffix(text, CLIVersion),
		)
	})
}

func TestBuildBillingValue(t *testing.T) {
	t.Parallel()

	text := "You are a helpful assistant."
	got := BuildBillingValue(text)

	// Must start with the header name.
	require.True(t, strings.HasPrefix(got, "x-anthropic-billing-header: "), "expected header prefix")

	// Must contain all required fields.
	require.Contains(t, got, "cc_version="+CLIVersion+".")
	require.Contains(t, got, "cc_entrypoint="+Entrypoint)
	require.Contains(t, got, "cch=")

	// The CCH component must match ComputeCCH.
	cch := ComputeCCH(text)
	require.Contains(t, got, "cch="+cch)

	// The version suffix must match ComputeVersionSuffix.
	suffix := ComputeVersionSuffix(text, CLIVersion)
	require.Contains(t, got, "cc_version="+CLIVersion+"."+suffix)

	// Must end with a semicolon.
	require.True(t, strings.HasSuffix(got, ";"), "expected trailing semicolon")
}

func TestBuildBillingValue_EmptyText(t *testing.T) {
	t.Parallel()

	got := BuildBillingValue("")
	require.Contains(t, got, "cch="+ComputeCCH(""))
	require.True(t, strings.HasPrefix(got, "x-anthropic-billing-header: "))
}
