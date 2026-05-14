package logo

import (
	"math/rand/v2"
	"sync"
)

var (
	randCaches   = make(map[int]int)
	randCachesMu sync.Mutex
)

// cachedRandN returns a random int in [0, n), cached by n. All callers
// passing the same n receive the same value for the lifetime of the
// process. This provides stable-per-session randomness but means
// independent random choices must use distinct values of n.
func cachedRandN(n int) int {
	randCachesMu.Lock()
	defer randCachesMu.Unlock()

	if nPrime, ok := randCaches[n]; ok {
		return nPrime
	}

	r := rand.IntN(n)
	randCaches[n] = r
	return r
}
