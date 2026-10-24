package agent

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// countingResolver expands a value by prefixing it, counting each call.
type countingResolver struct {
	calls int
	err   error
}

func (r *countingResolver) ResolveValue(value string) (string, error) {
	r.calls++
	return "resolved:" + value, r.err
}

func TestResolveOnce(t *testing.T) {
	t.Parallel()

	t.Run("an empty value has nothing to resolve", func(t *testing.T) {
		t.Parallel()
		resolver := &countingResolver{}
		require.Nil(t, resolveOnce(resolver, ""))
		require.Zero(t, resolver.calls)
	})

	t.Run("resolves on first use, then reuses the result", func(t *testing.T) {
		t.Parallel()
		resolver := &countingResolver{}
		get := resolveOnce(resolver, "$(security find-generic-password -w -s brave)")
		require.Zero(t, resolver.calls, "nothing runs until a tool needs the value")

		for range 3 {
			value, err := get()
			require.NoError(t, err)
			require.Equal(t, "resolved:$(security find-generic-password -w -s brave)", value)
		}
		require.Equal(t, 1, resolver.calls)
	})

	t.Run("a failure is reported every time without running again", func(t *testing.T) {
		t.Parallel()
		resolveErr := errors.New("keychain locked")
		resolver := &countingResolver{err: resolveErr}
		get := resolveOnce(resolver, "$(false)")

		_, first := get()
		_, second := get()

		require.ErrorIs(t, first, resolveErr)
		require.ErrorIs(t, second, resolveErr)
		require.Equal(t, 1, resolver.calls)
	})
}
