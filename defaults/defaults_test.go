package defaults

import "testing"

// The shipped patterns match what the plugins recognised before they moved
// out of code.
func TestDefaults(t *testing.T) {
	c := Get()
	if c.Version == "" || c.MaxFileBytes <= 0 || len(c.Engines) == 0 || len(Providers) == 0 || len(Agents) == 0 {
		t.Fatal("defaults incomplete")
	}
	for name, want := range map[string]bool{
		"docker-compose.yml": true, "compose.yaml": true, "docker-compose.edge.vps.yml": true, "compose.override.yml": true,
		"docker-compose.yml.bak": false, "my-compose.yml": false, "compose.json": false,
	} {
		if got := MatchBase(c.Files.Compose, name); got != want {
			t.Errorf("compose %s = %v", name, got)
		}
	}
	for name, want := range map[string]bool{
		"Dockerfile": true, "Containerfile": true, "Dockerfile.dev": true, "api.Dockerfile": true, "web.dockerfile": true,
		"dockerfile": false, "Dockerfile-old": false,
	} {
		if got := MatchBase(c.Files.Dockerfile, name); got != want {
			t.Errorf("dockerfile %s = %v", name, got)
		}
	}
	for name, want := range map[string]bool{
		".env.example": true, "env.example": true, "prod.env.example": true, "env.prod.example": true, ".env.prod.sample": true,
		".env.dist": true, "config.example": false, ".env": false, "env.prod": false,
	} {
		if got := MatchBase(c.Files.EnvExamples, name); got != want {
			t.Errorf("env example %s = %v", name, got)
		}
	}
	for p, want := range map[string]bool{
		".githooks/pre-push": true, ".husky/pre-commit": true, ".husky/_/husky.sh": false, "lefthook.yml": true,
		".pre-commit-config.yaml": true, "scripts/hooks/pre-push": false,
	} {
		if got := c.IsHook(p); got != want {
			t.Errorf("hook %s = %v", p, got)
		}
	}
	if !c.ConfigName("defaultChiftAPIBaseURL") || !c.ConfigName("S3Endpoint") || c.ConfigName("urlPath") {
		t.Error("config names")
	}
}

// The engine lookup tries the full repository path, then its last segment.
func TestEngineFullPath(t *testing.T) {
	for repo, want := range map[string]string{
		"mssql/server": "sqlserver", "library/postgres": "postgres", "azure-sql-edge": "sqlserver",
	} {
		if got, ok := Engine(repo); !ok || got != want {
			t.Errorf("Engine(%q) = %q, %v; want %q", repo, got, ok, want)
		}
	}
	if got, ok := Engine("acme/server"); ok || got != "" {
		t.Errorf("Engine(acme/server) = %q, %v; want none", got, ok)
	}
}
