// Package composefile holds what the compose and command plugins share about
// Compose files: the names Compose picks by default and how a stack is named.
package composefile

import (
	"path"
	"strings"
)

// Default and Override are the file names Compose reads without -f, in order
// of preference.
var (
	Default  = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}
	Override = []string{"compose.override.yaml", "compose.override.yml", "docker-compose.override.yaml", "docker-compose.override.yml"}
)

// StackName is the suffix of the last file: docker-compose.edge.vps.yml ->
// edge.vps, docker-compose.yml -> default. An override file names the stack it
// overrides, so the last file being an override also gives default.
func StackName(files []string) string {
	if len(files) == 0 {
		return "default"
	}
	b := path.Base(files[len(files)-1])
	b = strings.TrimSuffix(strings.TrimSuffix(b, ".yml"), ".yaml")
	b = strings.TrimPrefix(strings.TrimPrefix(b, "docker-compose"), "compose")
	b = strings.TrimPrefix(b, ".")
	if b == "" || b == "override" {
		return "default"
	}
	return b
}
