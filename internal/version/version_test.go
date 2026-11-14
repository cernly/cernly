package version

import "testing"

func TestParse(t *testing.T) {
	for raw, want := range map[string]string{
		"v1.12.0":                               "semver 1.12.0",
		"1.1.0-alpha.2":                         "semver 1.1.0-alpha.2",
		"17":                                    "semver 17",
		"v0.0.0-20200617195104-da1b6568686e":    "pseudo 2020-06-17T19:51:04Z",
		"v2.11.1-0.20261003002531-6258dcebc4e2": "pseudo 2026-10-03T00:25:31Z",
		"v1.2.3-pre.0.20261003002531-6258dcebc4e2": "pseudo 2026-10-03T00:25:31Z",
		"2026.7.3":                     "calver 2026.7.3",
		"RELEASE.2025-04-22T22-12-26Z": "date 2025-04-22T22:12:26Z",
		"20250422":                     "date 2025-04-22",
		"^4.1.0":                       "unknown ",
		"*":                            "unknown ",
		"latest":                       "unknown ",
	} {
		if v := Parse(raw); v.Scheme+" "+v.Normalised != want {
			t.Errorf("Parse(%q) = %q, want %q", raw, v.Scheme+" "+v.Normalised, want)
		}
	}
	for tag, want := range map[string]string{
		"17-alpine":                    "semver 17",
		"1.27.3-alpine":                "semver 1.27.3",
		"RELEASE.2025-04-22T22-12-26Z": "date 2025-04-22T22:12:26Z",
		"7.2":                          "semver 7.2",
		"latest":                       "unknown ",
	} {
		if v := Tag(tag); v.Scheme+" "+v.Normalised != want || v.Raw != tag {
			t.Errorf("Tag(%q) = %+v, want %q", tag, v, want)
		}
	}
}
