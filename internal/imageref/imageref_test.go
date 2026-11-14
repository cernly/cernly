package imageref

import "testing"

func TestRepo(t *testing.T) {
	for ref, want := range map[string]string{
		"caddy:2.9-alpine":                                "caddy",
		"cloudflare/cloudflared:2026.7.3":                 "cloudflared",
		"docker.io/library/postgres:16-alpine@sha256:abc": "postgres",
		"localhost:5000/acme/api":                         "api",
		"${REGISTRY:-localhost:5000}/${NS:-acme}/acme-api:${API_IMAGE_TAG:-latest}": "acme-api",
		"${REGISTRY}/acme/api:${tag}": "api",
		"acme-stack/api:local":        "api",
		"redis":                       "redis",
	} {
		if got := Repo(ref); got != want {
			t.Errorf("Repo(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestParse(t *testing.T) {
	for raw, want := range map[string]string{
		"postgres:17-alpine":                       "docker.io library/postgres 17-alpine floating",
		"postgres":                                 "docker.io library/postgres  floating",
		"redis:latest":                             "docker.io library/redis latest floating",
		"caddy:2.9-alpine":                         "docker.io library/caddy 2.9-alpine floating",
		"golang:1.27.3-alpine":                     "docker.io library/golang 1.27.3-alpine exact",
		"cloudflare/cloudflared:2026.7.3":          "docker.io cloudflare/cloudflared 2026.7.3 exact",
		"minio/minio:RELEASE.2025-04-22T22-12-26Z": "docker.io minio/minio RELEASE.2025-04-22T22-12-26Z exact",
		"ghcr.io/acme/api:1.4.0@sha256:abc":        "ghcr.io acme/api 1.4.0 digest",
		"acme-stack/api:local":                     "docker.io acme-stack/api local floating",
		"${REGISTRY}/acme/api:${tag}":              "${REGISTRY} acme/api ${tag} unknown",
		"${REGISTRY:-localhost:5000}/${IMAGE_NAMESPACE:-acme}/acme-api:${API_IMAGE_TAG:-${IMAGE_TAG:-latest}}": "localhost:5000 acme/acme-api latest floating",
	} {
		r := Parse(raw)
		if got := r.Registry + " " + r.Repository + " " + r.Tag + " " + r.Pinning; got != want {
			t.Errorf("Parse(%q) = %q, want %q", raw, got, want)
		}
	}
	if got := Parse("${REGISTRY:-localhost:5000}/a:${A:-${B:-1}}").Templated; len(got) != 3 || got[0] != "A" || got[1] != "B" || got[2] != "REGISTRY" {
		t.Errorf("templated = %v", got)
	}
}

func TestName(t *testing.T) {
	for ref, want := range map[string]string{
		"ghcr.io/acme/api:${TAG}":                "ghcr.io/acme/api",
		"localhost:5000/acme/api":                "localhost:5000/acme/api",
		"localhost:5000/acme/api:1.2":            "localhost:5000/acme/api",
		"acme/api-$PLATFORM:latest":              "acme/api-$PLATFORM",
		"${REGISTRY:-localhost:5000}/acme/api:1": "${REGISTRY:-localhost:5000}/acme/api",
		"acme/api@sha256:abc":                    "acme/api",
		"redis":                                  "redis",
	} {
		if got := Name(ref); got != want {
			t.Errorf("Name(%q) = %q, want %q", ref, got, want)
		}
	}
}
