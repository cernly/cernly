// Package redact removes credentials from values read from a repo before they
// are stored in a fact.
package redact

import "strings"

// Userinfo drops the userinfo (user:password@) of every URL in s: the part of
// the authority after "://" up to its last @. A bare git@ (ssh to a forge) is
// kept; any other user is dropped, with or without a password, because a
// forge token is often given as the user name alone. The rest of s is kept as
// written, so the same input always gives the same output.
func Userinfo(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "://")
		if i < 0 {
			break
		}
		start := i + 3
		end := len(s)
		if j := strings.IndexAny(s[start:], "/?#\\ \t\r\n\"'"); j >= 0 {
			end = start + j
		}
		auth := s[start:end]
		b.WriteString(s[:start])
		if at := strings.LastIndexByte(auth, '@'); at >= 0 && auth[:at] != "git" {
			auth = auth[at+1:]
		}
		b.WriteString(auth)
		s = s[end:]
	}
	b.WriteString(s)
	return b.String()
}
