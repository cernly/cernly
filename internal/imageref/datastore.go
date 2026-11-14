package imageref

import (
	"strings"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/internal/version"
)

// Datastore classifies an image reference such as
// "docker.io/library/postgres:16-alpine@sha256:..." as postgres, version 16.
// The version comes from the tag through the version scheme parser, so
// RELEASE.<date> tags keep a date.
func Datastore(image string) (engine string, v version.Version, ok bool) {
	if image == "" {
		return "", v, false
	}
	r := Parse(image)
	// The image repository decides (full path, then last segment), by the
	// engines table in the shipped defaults. Anything else is a service.
	engine, ok = defaults.Engine(r.Repository)
	if !ok {
		return "", v, false
	}
	if r.Tag != "" && !strings.Contains(r.Tag, "${") {
		v = version.Tag(r.Tag)
	}
	return engine, v, true
}
