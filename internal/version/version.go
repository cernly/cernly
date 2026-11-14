// Package version classifies version strings into the schemes
// semver, calver, date, pseudo and unknown. The raw string is always kept; an
// unknown scheme is never dropped. See https://cernly.dev/how/facts/.
package version

import (
	"regexp"
	"strings"
)

const (
	SemVer  = "semver"
	CalVer  = "calver"
	Date    = "date"
	Pseudo  = "pseudo"
	Unknown = "unknown"
)

type Version struct {
	Raw        string
	Scheme     string
	Normalised string
}

// Attrs returns the version def of scan.v1.schema.json.
func (v Version) Attrs() map[string]any {
	a := map[string]any{"raw": v.Raw, "scheme": v.Scheme}
	if v.Normalised != "" {
		a["normalised"] = v.Normalised
	}
	return a
}

var (
	// Go pseudo-versions: v0.0.0-ts-hash, vX.Y.Z-pre.0.ts-hash, vX.Y.Z-0.ts-hash.
	pseudoRe  = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-(.+\.)?(0\.)?[0-9]{14}-[0-9a-f]{12}(\+incompatible)?$`)
	pseudoTS  = regexp.MustCompile(`[0-9]{14}-[0-9a-f]{12}`)
	semverRe  = regexp.MustCompile(`^v?([0-9]+)(\.[0-9]+(\.[0-9]+)?)?(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	calverRe  = regexp.MustCompile(`^v?(20[0-9]{2})\.([0-9]{1,2})(\.[0-9]+)?$`)
	releaseRe = regexp.MustCompile(`^(RELEASE\.)?([0-9]{4})-([0-9]{2})-([0-9]{2})(T([0-9]{2})-([0-9]{2})-([0-9]{2})Z)?$`)
	compactRe = regexp.MustCompile(`^([0-9]{4})([0-9]{2})([0-9]{2})$`)
)

// Parse classifies raw. Partial numeric versions (17, 3.21) are semver with
// the precision as written: 17 is not 17.0.0.
func Parse(raw string) Version {
	v := Version{Raw: raw, Scheme: Unknown}
	switch {
	case pseudoRe.MatchString(raw):
		ts := pseudoTS.FindString(raw)
		v.Scheme, v.Normalised = Pseudo, ts[0:4]+"-"+ts[4:6]+"-"+ts[6:8]+"T"+ts[8:10]+":"+ts[10:12]+":"+ts[12:14]+"Z"
	case releaseRe.MatchString(raw):
		m := releaseRe.FindStringSubmatch(raw)
		v.Scheme, v.Normalised = Date, m[2]+"-"+m[3]+"-"+m[4]
		if m[5] != "" {
			v.Normalised += "T" + m[6] + ":" + m[7] + ":" + m[8] + "Z"
		}
	case compactRe.MatchString(raw) && raw[0] == '2':
		m := compactRe.FindStringSubmatch(raw)
		v.Scheme, v.Normalised = Date, m[1]+"-"+m[2]+"-"+m[3]
	case calverRe.MatchString(raw):
		v.Scheme, v.Normalised = CalVer, strings.TrimPrefix(raw, "v")
	case semverRe.MatchString(raw):
		v.Scheme, v.Normalised = SemVer, strings.TrimPrefix(raw, "v")
	}
	return v
}

// Tag reads an image tag: the version part before a variant suffix
// (17-alpine -> 17), or the whole tag for date forms. Raw stays the tag.
func Tag(tag string) Version {
	if v := Parse(tag); v.Scheme != Unknown {
		if v.Scheme != SemVer || !strings.Contains(tag, "-") {
			return v
		}
	}
	if i := strings.Index(tag, "-"); i > 0 {
		if v := Parse(tag[:i]); v.Scheme != Unknown {
			v.Raw = tag
			return v
		}
	}
	return Version{Raw: tag, Scheme: Unknown}
}
