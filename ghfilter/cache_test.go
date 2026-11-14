package ghfilter

import (
	"fmt"
	"testing"
)

// The compiled-pattern cache stays bounded across many distinct patterns.
func TestCacheBounded(t *testing.T) {
	for i := range 3 * maxCached {
		Match(fmt.Sprintf("b%d/*", i), "b1/x")
	}
	if len(cache) > maxCached {
		t.Errorf("cache holds %d patterns, max %d", len(cache), maxCached)
	}
	if !Match("b1/*", "b1/x") {
		t.Error("match wrong after eviction")
	}
}
