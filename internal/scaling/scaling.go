// Package scaling checks in tests that work grows linearly with the input.
// It compares a run on n items with a run on 4n, so the verdict does not
// depend on how fast the machine is: linear work takes about 4 times as
// long, quadratic work 16 times.
package scaling

import (
	"testing"
	"time"
)

// maxRatio sits between linear (4) and quadratic (16) growth.
const maxRatio = 10

// Linear fails the test when the work at 4n takes more than maxRatio times
// the work at n. setup builds the input for a size outside the timing and
// returns the work to time. Each size runs five times and the fastest run
// counts, so a pause in the machine does not decide the result. Pick n so
// the small run takes some milliseconds, or timer noise decides instead.
func Linear(t testing.TB, n int, setup func(n int) func()) {
	t.Helper()
	small, large := fastest(setup, n), fastest(setup, 4*n)
	if small <= 0 {
		small = time.Nanosecond
	}
	if r := float64(large) / float64(small); r > maxRatio {
		t.Errorf("work at %d took %v, at %d %v: %.1f times, want linear", n, small, 4*n, large, r)
	}
}

func fastest(setup func(int) func(), n int) time.Duration {
	var best time.Duration
	for i := 0; i < 5; i++ {
		run := setup(n)
		start := time.Now()
		run()
		if d := time.Since(start); i == 0 || d < best {
			best = d
		}
	}
	return best
}
