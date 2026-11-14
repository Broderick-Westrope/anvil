package secrets

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests read the detector's cache directly: whether a result came
// from the cache or was computed again has no exported way to observe it.
// Behaviour callers can see is tested in secrets_test.go.

var cacheTestKey = []byte("012345" + "6789ab" + "cdef01" + "234567" + "89abcd" + "ef")

const cacheTestStripe = "sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn"

func TestCache_KnownValueAddedAgainKeepsIt(t *testing.T) {
	t.Parallel()

	d := New(nil, cacheTestKey)
	d.AddKnown(Known{Kind: "k", Value: "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"})
	d.Mask("key " + cacheTestStripe)
	forms := len(d.forms)

	d.AddKnown(Known{Kind: "k", Value: "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"})
	require.Len(t, d.forms, forms)
	require.Len(t, d.masked, 1)
}

func TestCache_NewKnownValueEmptiesIt(t *testing.T) {
	t.Parallel()

	d := New(nil, cacheTestKey)
	d.Mask("key " + cacheTestStripe)
	d.Mask("nothing here")
	d.AddKnown(Known{Kind: "k", Value: "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"})
	require.Empty(t, d.masked)
	require.Empty(t, d.clean)
	require.Zero(t, d.maskedBytes)
}

// A value added while a result is computed means it's computed again, so
// the text returned is masked with the value, not just left uncached.
func TestCache_ValueAddedDuringComputeIsUsed(t *testing.T) {
	t.Parallel()

	d := New(nil, cacheTestKey)
	value := "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"
	added := false
	got, findings := d.cached(textDomain, "token "+value, false, func(forms []knownForm) (string, []Finding) {
		if !added {
			added = true
			d.AddKnown(Known{Kind: "k", Value: value})
		}
		return d.mask("token "+value, forms)
	})
	require.NotContains(t, got, value)
	require.Len(t, findings, 1)
	require.Len(t, d.masked, 1)
}

func TestCache_CleanTextIsScannedOnce(t *testing.T) {
	t.Parallel()

	d := New(nil, cacheTestKey)
	runs := 0
	compute := func(forms []knownForm) (string, []Finding) {
		runs++
		return d.mask("plain text", forms)
	}
	d.cached(textDomain, "plain text", false, compute)
	got, findings := d.cached(textDomain, "plain text", false, compute)
	require.Equal(t, 1, runs)
	require.Equal(t, "plain text", got)
	require.Empty(t, findings)

	d.cached(jsonDomain, "plain text", false, compute)
	require.Equal(t, 2, runs, "another domain is cached apart")
}

func TestCache_OversizedResultIsNotKept(t *testing.T) {
	t.Parallel()

	d := New(nil, cacheTestKey)
	d.maxMaskedBytes = 10
	d.Mask("a long text with " + cacheTestStripe)
	require.Empty(t, d.masked)
	require.Zero(t, d.maskedBytes)
}

func TestCache_Bounds(t *testing.T) {
	t.Parallel()

	d := New(nil, cacheTestKey)
	d.maxClean = 2
	d.Mask("a1")
	d.Mask("b2")
	require.Len(t, d.clean, 2, "the cache fills to its bound")
	d.Mask("c3")
	require.Len(t, d.clean, 1, "the full cache restarts with the third text")

	one, two := "one "+cacheTestStripe, "two "+cacheTestStripe
	first, _ := d.Mask(one)
	second, _ := d.Mask(two)
	d.maxMaskedBytes = len(first) + len(second)
	clear(d.masked)
	d.maskedBytes = 0
	d.Mask(one)
	require.Equal(t, len(first), d.maskedBytes)
	d.Mask(one)
	require.Equal(t, len(first), d.maskedBytes, "a cached result isn't counted twice")
	d.Mask(two)
	require.Len(t, d.masked, 2, "results that exactly fill the bound are kept")
	require.Equal(t, len(first)+len(second), d.maskedBytes)

	d.Mask("three " + cacheTestStripe)
	require.Len(t, d.masked, 1, "the next result doesn't fit, so the cache restarts")
}

func TestCache_ResultTheSizeOfTheBoundIsKept(t *testing.T) {
	t.Parallel()

	d := New(nil, cacheTestKey)
	masked, _ := d.Mask("x " + cacheTestStripe)
	clear(d.masked)
	d.maskedBytes = 0
	d.maxMaskedBytes = len(masked)
	d.Mask("x " + cacheTestStripe)
	require.Len(t, d.masked, 1)
}
