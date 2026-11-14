// Package hostname decides which hosts are public, for the plugins that keep
// a host and drop the rest of a value (env, outbound).
package hostname

import (
	"net"
	"net/url"
	"strings"

	"github.com/cernly/cernly/defaults"
)

// Public reports a hostname that can be on the internet: a dot, not an IP
// literal, no reserved top-level domain, not one of the example domains.
// Both lists are in the shipped defaults.
func Public(h string) bool {
	if h == "" || net.ParseIP(h) != nil || !strings.Contains(h, ".") || strings.ContainsAny(h, "%{}$ ") {
		return false
	}
	d := defaults.Get().Domains
	tld := h[strings.LastIndex(h, ".")+1:]
	for _, r := range d.ReservedTLDs {
		if tld == r {
			return false
		}
	}
	for _, d := range d.Example {
		if h == d || strings.HasSuffix(h, "."+d) {
			return false
		}
	}
	return true
}

// FromURL returns the lower-case public host of a URL, else "". Scheme,
// credentials, port, path and query are dropped.
func FromURL(s string) string {
	if !strings.Contains(s, "://") || strings.ContainsAny(s, " ,") {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if !Public(h) {
		return ""
	}
	return h
}
