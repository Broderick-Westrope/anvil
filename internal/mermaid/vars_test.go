package mermaid

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// resolveVarsWithin fails the test instead of hanging when resolveVars
// doesn't return.
func resolveVarsWithin(t *testing.T, svg string, theme Theme) (string, error) {
	t.Helper()
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := resolveVars(svg, theme)
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-time.After(2 * time.Second):
		t.Fatal("resolveVars did not finish")
		return "", nil
	}
}

func TestResolveVars(t *testing.T) {
	t.Parallel()

	theme := Theme{"a": "red"}
	tests := map[string]struct {
		svg, want string
	}{
		"text after the last tag":       {svg: `<svg fill="var(--a)"></svg>` + "\n", want: `<svg fill="red"></svg>` + "\n"},
		"unterminated tag":              {svg: `<svg fill="var(--a)" x`, want: `<svg fill="red" x`},
		"empty value before a var":      {svg: `<rect a="" fill="var(--a)"/>`, want: `<rect a="" fill="red"/>`},
		"single-quoted value":           {svg: `<rect fill='var(--a)'/>`, want: `<rect fill='red'/>`},
		"unterminated value":            {svg: `<rect fill="var(--a)`, want: `<rect fill="var(--a)`},
		"unclosed var":                  {svg: `<rect fill="x var(--a"/>`, want: `<rect fill="x var(--a"/>`},
		"declaration without a colon":   {svg: `<svg style="--b;--c" fill="var(--b, blue)"/>`, want: `<svg style="--b;--c" fill="blue"/>`},
		"declaration with a nested var": {svg: `<svg style="--b:var(--a);--c:x" fill="var(--b)"/>`, want: `<svg style="--b:red;--c:x" fill="red"/>`},
	}
	for name, tc := range tests {
		got, err := resolveVarsWithin(t, tc.svg, theme)
		require.NoError(t, err, name)
		require.Equal(t, tc.want, got, name)
	}
}

func TestResolveVarsLimit(t *testing.T) {
	t.Parallel()

	const svg = `<svg fill="var(--a)"/>`
	// The output is checked after each closing quote: `<svg fill="`, the
	// value and `"`.
	const overhead = len(`<svg fill="`) + len(`"`)
	tests := map[string]struct {
		svg     string
		value   int
		wantErr bool
	}{
		"a small diagram may reach 1 MiB": {svg: svg, value: 1<<20 - overhead},
		"but not pass it":                 {svg: svg, value: 1<<20 - overhead + 1, wantErr: true},
		// A large diagram may grow to four times its size.
		"a large diagram may grow past 1 MiB": {svg: `<svg>` + strings.Repeat(" ", 300_000) + svg, value: 850_000},
	}
	for name, tc := range tests {
		_, err := resolveVarsWithin(t, tc.svg, Theme{"a": strings.Repeat("x", tc.value)})
		if tc.wantErr {
			require.ErrorIs(t, err, errVarsTooLarge, name)
		} else {
			require.NoError(t, err, name)
		}
	}
}

func TestVarResolverAllowsAValueExactlyAtTheLimit(t *testing.T) {
	t.Parallel()

	newResolver := func(limit int) *varResolver {
		return &varResolver{
			defs:      map[string]string{"a": "abcde"},
			resolved:  map[string]string{},
			resolving: map[string]bool{},
			limit:     limit,
		}
	}
	got, err := newResolver(5).substitute("var(--a)")
	require.NoError(t, err)
	require.Equal(t, "abcde", got)

	_, err = newResolver(4).substitute("var(--a)")
	require.ErrorIs(t, err, errVarsTooLarge)
}

func TestResolveVarsRejectsDeepChains(t *testing.T) {
	t.Parallel()

	chain := func(links int) string {
		var decls strings.Builder
		for i := range links {
			fmt.Fprintf(&decls, "--v%d:var(--v%d);", i, i+1)
		}
		return fmt.Sprintf(`<svg fill="var(--v0)" style="%s--v%d:red"/>`, decls.String(), links)
	}
	got, err := resolveVarsWithin(t, chain(maxVarDepth-1), nil)
	require.NoError(t, err)
	require.Contains(t, got, `fill="red"`)

	_, err = resolveVarsWithin(t, chain(maxVarDepth), nil)
	require.ErrorIs(t, err, errVarsTooDeep)
}
