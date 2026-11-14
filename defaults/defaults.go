// Package defaults holds the shipped configuration: file patterns, skipped
// folders, the data store table, heuristics, reserved domains, and the
// provider and agent catalogues. Everything that differs from repo to repo
// but has a sensible default lives here, as data, not in the plugins' code.
// See https://cernly.dev/how/config/.
package defaults

import (
	_ "embed"
	"path"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed defaults.yml
var defaultsYAML []byte

// Providers and Agents are the catalogues, versioned on their own; their
// packages parse them.
//
//go:embed providers.json
var Providers []byte

//go:embed agents.json
var Agents []byte

type Config struct {
	Version      string                       `yaml:"version"`
	MaxFileBytes int64                        `yaml:"max_file_bytes"`
	Files        Files                        `yaml:"files"`
	Engines      map[string]string            `yaml:"engines"`
	Drivers      map[string]map[string]string `yaml:"drivers"` // ecosystem -> package -> engine
	Outbound     Outbound                     `yaml:"outbound"`
	Domains      Domains                      `yaml:"domains"`
	AgentConfigs []AgentConfig                `yaml:"agent_configs"`
}

// AgentConfig maps the files an AI coding tool leaves in a repo to the tool.
type AgentConfig struct {
	Tool  string   `yaml:"tool"`
	Name  string   `yaml:"name"`
	Paths []string `yaml:"paths"`
}

type Files struct {
	Compose              []string      `yaml:"compose"`
	Dockerfile           []string      `yaml:"dockerfile"`
	Caddyfile            []string      `yaml:"caddyfile"`
	Prisma               []string      `yaml:"prisma"`
	Nginx                []string      `yaml:"nginx"`
	Traefik              []string      `yaml:"traefik"`
	SQLMigrations        SQLMigrations `yaml:"sql_migrations"`
	EnvExamples          []string      `yaml:"env_examples"`
	EnvExamplesByContent []string      `yaml:"env_examples_by_content"`
	Hooks                Hooks         `yaml:"hooks"`
	SkipDirs             []string      `yaml:"skip_dirs"`
}

type SQLMigrations struct {
	Paths    []string `yaml:"paths"`
	SetDirs  []string `yaml:"set_dirs"`
	Down     []string `yaml:"down"`
	SkipDirs []string `yaml:"skip_dirs"`
}

type Hooks struct {
	Dirs     []string `yaml:"dirs"`
	SkipDirs []string `yaml:"skip_dirs"`
	Files    []string `yaml:"files"`
}

type Outbound struct {
	ConfigNameSuffixes []string `yaml:"config_name_suffixes"`
}

type Domains struct {
	ReservedTLDs []string `yaml:"reserved_tlds"`
	Example      []string `yaml:"example"`
}

var (
	once sync.Once
	cfg  Config
)

// Get returns the shipped defaults. A broken embedded file is a programming
// error that any test run catches.
func Get() *Config {
	once.Do(func() {
		if err := yaml.Unmarshal(defaultsYAML, &cfg); err != nil {
			panic("defaults: embedded defaults.yml: " + err.Error())
		}
	})
	return &cfg
}

// Engine returns the data store engine of an image repository: the full path
// (mssql/server) first, then its last segment (postgres).
func Engine(repository string) (string, bool) {
	e := Get().Engines
	if v, ok := e[repository]; ok {
		return v, true
	}
	v, ok := e[repository[strings.LastIndex(repository, "/")+1:]]
	return v, ok
}

// MatchBase reports whether a file's base name matches one of the globs.
func MatchBase(globs []string, base string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, base); ok {
			return true
		}
	}
	return false
}

// IsCompose reports whether a path (or base name) is a Compose file.
func IsCompose(p string) bool { return MatchBase(Get().Files.Compose, path.Base(p)) }

// IsCaddyfile reports whether a path (or base name) is a Caddyfile.
func IsCaddyfile(p string) bool { return MatchBase(Get().Files.Caddyfile, path.Base(p)) }

// IsHook reports tracked hook configuration: a file in a hook folder (not in
// one of its skipped subfolders) or a named hook file.
func (c *Config) IsHook(p string) bool {
	for _, s := range c.Files.Hooks.SkipDirs {
		if strings.HasPrefix(p, s+"/") {
			return false
		}
	}
	for _, d := range c.Files.Hooks.Dirs {
		if strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	base := path.Base(p)
	for _, f := range c.Files.Hooks.Files {
		if p == f || base == f {
			return true
		}
	}
	return false
}

// ConfigName reports a const or var name that holds a configured URL.
func (c *Config) ConfigName(name string) bool {
	low := strings.ToLower(name)
	for _, s := range c.Outbound.ConfigNameSuffixes {
		if strings.HasSuffix(low, s) {
			return true
		}
	}
	return false
}
