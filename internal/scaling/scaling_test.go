package scaling

import "testing"

type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Helper()               {}
func (r *recorder) Errorf(string, ...any) { r.failed = true }

var sink int

func work(n int) func() {
	return func() {
		for i := 0; i < n; i++ {
			sink += i
		}
	}
}

func TestLinear(t *testing.T) {
	r := &recorder{TB: t}
	Linear(r, 1<<20, work)
	if r.failed {
		t.Error("linear work failed the check")
	}
	r = &recorder{TB: t}
	Linear(r, 2000, func(n int) func() { return work(n * n) })
	if !r.failed {
		t.Error("quadratic work passed the check")
	}
}
