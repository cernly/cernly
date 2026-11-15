package redact

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/schema"
)

// words lists, per attribute name, the strings the scanner itself writes
// there. A string stays readable only when it is one of them (or, for the
// keys in checked, passes that check); any other string under the key is
// tokenized, whatever the key. The schema's enums are added at start, and the
// engines from the shipped defaults. An attribute missing here is free text.
var words = map[string]map[string]bool{
	"engine": setOf(),
	"language": setOf("c", "cpp", "csharp", "dotnet", "go", "java", "javascript", "kotlin", "php",
		"python", "ruby", "rust", "scala", "shell", "sql", "svelte", "swift", "typescript", "vue"),
	"framework": setOf("aspnet-minimal", "aspnet-mvc", "aspnet-razor", "blazor", "go-test", "xunit",
		"nunit", "mstest", "tunit", "junit", "testng", "jest", "vitest", "mocha", "playwright", "cypress",
		"pytest", "unittest", "unknown"),
	"tool": setOf("bake", "build-push-action", "docker build", "compose", "kubernetes", "helm",
		"dotnet", "go", "gradle", "maven", "npm", "pnpm", "yarn", "pytest", "unittest", "vitest",
		"jest", "playwright", "cypress"),
	"ecosystem": setOf("go", "npm", "nuget", "helm", "maven", "gradle", "pypi", "cargo", "composer",
		"rubygems", "docker", "github-actions"),
	"scope":    setOf("prod", "dev", "build", "test", "optional", "peer", "unknown"),
	"usage":    setOf("mention", "default", "request"),
	"pinning":  setOf("digest", "exact", "floating", "unknown"),
	"scheme":   setOf("semver", "calver", "unknown", "http", "https", "h2c"),
	"dialect":  setOf("postgresql", "mysql", "sqlite", "sqlserver", "unknown"),
	"provider": setOf("sqlserver", "postgresql", "npgsql", "sqlite", "mysql", "oracle", "cosmos", "inmemory", "mongodb", "cockroachdb"),
	"origins":  setOf("code", "compose", "config", "env_example"),
	"events": setOf("push", "pull_request", "pull_request_target", "workflow_dispatch", "workflow_run",
		"workflow_call", "schedule", "release", "merge_group", "repository_dispatch"),
	"lifecycle": setOf("production", "experimental", "deprecated"),
	// kind: catalog entities, Kubernetes workloads, Compose mounts, and the
	// schema's enums (rewrite and entrypoint kinds).
	"kind": setOf("component", "system", "api", "resource", "domain", "group", "user", "location", "template",
		"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Pod",
		"bind", "volume", "tmpfs"),
	// markers: the bare authorization markers; one with arguments or a
	// condition names roles and policies, and is tokenized.
	"markers": setOf("[Authorize]", "[AllowAnonymous]", "RequireAuthorization()", "AllowAnonymous()"),
	// type: owner types, catalog spec types and the scalar column types; an
	// enum's or a model's own name is tokenized (see checked).
	"type": setOf("user", "team", "email",
		"service", "website", "library", "documentation", "tool", "openapi", "grpc", "graphql", "asyncapi", "database"),
	"via":    setOf("literal_url", "sdk", "env_var"),
	"source": setOf("builtin", "wasm", "codeowners", "convention", "invocation"),
	"role":   setOf("from", "copy"),
}

// otherClass is where a string outside a key's words goes, when not a name.
var otherClass = map[string]int{"source": classPath}

// checked are keys whose readable strings follow a shape rather than a list:
// versions, and column types (a known scalar or SQL type, with an optional
// length or precision).
var checked = map[string]func(string) bool{
	"version":     isVersion,
	"normalised":  isVersion,
	"type":        isColumnType,
	"native_type": isColumnType,
}

var versionRe = regexp.MustCompile(`(?i)^v?\d+(\.\d+){0,3}([-.+]?(alpha|beta|rc|pre|preview|dev|snapshot|final|ga|lts|alpine|slim)\.?\d*)?$`)

func isVersion(s string) bool { return s == "latest" || versionRe.MatchString(s) }

var (
	columnRe = regexp.MustCompile(`^([A-Za-z][A-Za-z ]*?)\s*(\(\s*(\d+|max|MAX)\s*(,\s*\d+\s*)?\))?(\[\])?(\?)?$`)
	// scalars are column types of Prisma, of .NET (EF Core) and of SQL
	// dialects, compared in lower case.
	scalars = setOf(
		"string", "int", "bigint", "float", "decimal", "boolean", "datetime", "json", "bytes",
		"bool", "byte", "sbyte", "short", "ushort", "uint", "long", "ulong", "double", "char", "guid",
		"datetimeoffset", "timespan", "dateonly", "timeonly", "object",
		"integer", "smallint", "tinyint", "mediumint", "serial", "bigserial", "smallserial", "real",
		"double precision", "numeric", "money", "smallmoney", "bit", "varchar", "nvarchar", "char",
		"nchar", "character", "character varying", "text", "ntext", "citext", "uuid", "uniqueidentifier",
		"date", "time", "timestamp", "timestamptz", "timestamp with time zone", "timestamp without time zone",
		"time with time zone", "datetime2", "smalldatetime", "interval", "jsonb", "xml", "bytea", "blob",
		"binary", "varbinary", "image", "inet", "cidr", "macaddr", "tsvector", "enum",
	)
)

func isColumnType(s string) bool {
	m := columnRe.FindStringSubmatch(s)
	return m != nil && scalars[strings.ToLower(strings.TrimSpace(m[1]))]
}

// extensions are the file extensions that stay readable on a path's last
// segment; any other suffix is part of the name.
var extensions = setOf(
	".go", ".mod", ".sum", ".cs", ".csproj", ".sln", ".props", ".targets", ".config", ".cshtml", ".razor",
	".java", ".kt", ".kts", ".gradle", ".scala", ".py", ".rb", ".php", ".rs", ".swift", ".c", ".h", ".cpp",
	".js", ".mjs", ".cjs", ".jsx", ".ts", ".mts", ".cts", ".tsx", ".vue", ".svelte", ".css", ".scss", ".html",
	".json", ".jsonc", ".yaml", ".yml", ".toml", ".xml", ".ini", ".env", ".properties", ".lock",
	".md", ".txt", ".sql", ".prisma", ".proto", ".graphql", ".sh", ".bash", ".ps1",
	".dockerfile", ".caddyfile", ".caddy", ".conf", ".tf", ".hcl", ".tpl",
)

func init() {
	var doc any
	if err := json.Unmarshal(schema.ScanV1, &doc); err != nil {
		panic("redact: the embedded scan schema: " + err.Error())
	}
	for name, vals := range schemaEnums(doc) {
		if words[name] == nil {
			words[name] = setOf()
		}
		for v := range vals {
			words[name][v] = true
		}
	}
	for _, e := range defaults.Get().Engines {
		words["engine"][e] = true
	}
}

// schemaEnums collects, per property name anywhere under the facts' attrs,
// the string values of its enum (or its items' enum).
func schemaEnums(doc any) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	var enum func(m map[string]any) []any
	enum = func(m map[string]any) []any {
		if e, ok := m["enum"].([]any); ok {
			return e
		}
		if it, ok := m["items"].(map[string]any); ok {
			return enum(it)
		}
		return nil
	}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if props, ok := x["properties"].(map[string]any); ok && strings.Contains(path, "attrs_") {
				for name, sub := range props {
					m, _ := sub.(map[string]any)
					for _, e := range enum(m) {
						if s, ok := e.(string); ok {
							if out[name] == nil {
								out[name] = map[string]bool{}
							}
							out[name][s] = true
						}
					}
				}
			}
			for k, c := range x {
				walk(path+"/"+k, c)
			}
		case []any:
			for _, c := range x {
				walk(path, c)
			}
		}
	}
	walk("", doc)
	return out
}
