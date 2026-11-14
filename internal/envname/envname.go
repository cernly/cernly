// Package envname says what an environment variable name looks like.
package envname

import "regexp"

var re = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Valid is a letter or underscore, then letters, digits and underscores, any
// case.
func Valid(n string) bool { return re.MatchString(n) }
