package herdr

import "time"

// seqGen yields strictly increasing report numbers that also exceed
// any earlier Anvil's numbers in the same pane: Herdr never resets seq
// (not even on release) and silently drops stale reports.
type seqGen struct {
	prev int64
	now  func() time.Time
}

func (s *seqGen) next() int64 {
	n := s.now().UnixMilli()
	if n <= s.prev {
		n = s.prev + 1
	}
	s.prev = n
	return n
}
