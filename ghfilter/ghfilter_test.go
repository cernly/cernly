package ghfilter

import "testing"

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, s string
		want       bool
	}{
		{"main", "main", true},
		{"main", "mainline", false},
		{"*", "main", true},
		{"*", "feature/x", false},
		{"**", "feature/x/y", true},
		{"release/**", "main", false},
		{"release/**", "release/1/2", true},
		{"release/*", "release/1", true},
		{"release/*", "release/1/2", false},
		{"feature/*/ci", "feature/x/ci", true},
		{"**-prod", "eu/x-prod", true},
		{"mains?", "main", true},
		{"mains?", "mains", true},
		{"mains?", "mainss", false},
		{"ma+in", "maaain", true},
		{"ma+in", "min", false},
		{"v[12].[0-9]", "v2.7", true},
		{"v[12].[0-9]", "v3.7", false},
		{"v[12]+", "v1212", true},
		{"a.b", "axb", false},
		{`\*`, "*", true},
		{`\*`, "x", false},
		{"[", "[", true},
		{"[z-a]", "[z-a]", true},
		{"*?", "x?", true},
		{"héllo*", "héllo/", false},
		{"héllo*", "héllo-x", true},
	} {
		if got := Match(c.pattern, c.s); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestFilter(t *testing.T) {
	for name, c := range map[string]struct {
		patterns []string
		s        string
		want     bool
	}{
		"plain":                {[]string{"main"}, "main", true},
		"none":                 {nil, "main", false},
		"negated":              {[]string{"**", "!main"}, "main", false},
		"negated other":        {[]string{"**", "!main"}, "dev", true},
		"re-included":          {[]string{"releases/**", "!releases/**-alpha", "releases/x-alpha"}, "releases/x-alpha", true},
		"excluded after":       {[]string{"releases/**", "!releases/**-alpha"}, "releases/x-alpha", false},
		"negation before only": {[]string{"!main", "**"}, "main", true},
	} {
		if got := Filter(c.patterns, c.s); got != c.want {
			t.Errorf("%s: Filter(%q, %q) = %v, want %v", name, c.patterns, c.s, got, c.want)
		}
	}
}
