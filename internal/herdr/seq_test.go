package herdr

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSeqGen(t *testing.T) {
	t.Parallel()

	t.Run("first value is epoch millis", func(t *testing.T) {
		t.Parallel()
		at := time.UnixMilli(1_700_000_000_123)
		s := seqGen{now: func() time.Time { return at }}
		require.Equal(t, at.UnixMilli(), s.next())
	})

	t.Run("frozen clock increases by one", func(t *testing.T) {
		t.Parallel()
		at := time.UnixMilli(1_700_000_000_000)
		s := seqGen{now: func() time.Time { return at }}
		a, b, c := s.next(), s.next(), s.next()
		require.Equal(t, a+1, b)
		require.Equal(t, b+1, c)
	})

	t.Run("clock going backwards still increases", func(t *testing.T) {
		t.Parallel()
		times := []time.Time{
			time.UnixMilli(2_000),
			time.UnixMilli(1_000),
			time.UnixMilli(500),
		}
		i := 0
		s := seqGen{now: func() time.Time {
			ts := times[i]
			i++
			return ts
		}}
		a, b, c := s.next(), s.next(), s.next()
		require.Equal(t, int64(2_000), a)
		require.Greater(t, b, a)
		require.Greater(t, c, b)
	})
}
