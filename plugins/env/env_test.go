package env

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cernly/cernly/repo/repotest"
)

func scan(t *testing.T, files map[string]string) map[string]string {
	t.Helper()
	r := repotest.Files(t, files)
	res := Plugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	out := map[string]string{}
	for _, f := range res.Facts {
		var ev []string
		for _, e := range f.Evidence {
			ev = append(ev, fmt.Sprintf("%s:%d@%s", e.Path, e.Line, e.Unit))
		}
		out[f.Attrs["name"].(string)] = fmt.Sprint(f.Attrs["origins"], f.Attrs["host_hints"], ev)
	}
	return out
}

func TestEnv(t *testing.T) {
	got := scan(t, map[string]string{
		".env.example":               "# comment\nDATABASE_URL=postgres://u:secret@db.internal:5432/app\nMOLLIE_API_URL=https://api.mollie.com/v2?key=x\n# OLD_NAME=1\nexport APP_URL=\"http://app.localhost\"\nCORS=https://a.example.com,https://b.com\n",
		"deploy/connections.example": "PUBLIC_URL=https://app.widgets-shop.be\nIP_URL=http://10.0.0.1/x\n",
		"config.yml.example":         "server:\n  port: 8080\n",
		"docker-compose.yml":         "services:\n  api:\n    image: acme/api:${API_TAG:-latest}\n    environment:\n      DATABASE_URL: ${DATABASE_URL}\n      LOG: $$NOT_A_VAR\n  web:\n    environment:\n      - NODE_ENV=production\n",
		"backend/go.mod":             "module example.com/api\n\ngo 1.24\n",
		"backend/cmd/api/main.go":    "package main\n\nimport goos \"os\"\n\nfunc main() {\n\t_ = goos.Getenv(\"DATABASE_URL\")\n\tk := \"DYNAMIC\"\n\t_, _ = goos.LookupEnv(k)\n}\n",
		"backend/cmd/api/x_test.go":  "package main\n\nimport \"os\"\n\nvar _ = os.Getenv(\"TEST_ONLY\")\n",
	})
	for name, want := range map[string]string{
		// Example value host dropped (reserved TLD), compose key and value, Go read.
		"DATABASE_URL":   "[code compose env_example] <nil> [.env.example:2@ backend/cmd/api/main.go:6@backend docker-compose.yml:5@api docker-compose.yml:5@api]",
		"MOLLIE_API_URL": "[env_example] [api.mollie.com] [.env.example:3@]",
		"APP_URL":        "[env_example] <nil> [.env.example:5@]",
		"CORS":           "[env_example] <nil> [.env.example:6@]",
		"PUBLIC_URL":     "[env_example] [app.widgets-shop.be] [deploy/connections.example:1@]",
		"IP_URL":         "[env_example] <nil> [deploy/connections.example:2@]",
		"API_TAG":        "[compose] <nil> [docker-compose.yml:3@api]",
		"NODE_ENV":       "[compose] <nil> [docker-compose.yml:9@web]",
	} {
		if got[name] != want {
			t.Errorf("%s = %s\n want %s", name, got[name], want)
		}
	}
	for _, name := range []string{"OLD_NAME", "NOT_A_VAR", "DYNAMIC", "TEST_ONLY", "port", "server"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s reported", name)
		}
	}
	for name, v := range got {
		if strings.Contains(v, "secret") || strings.Contains(v, "v2") {
			t.Errorf("%s leaks a value: %s", name, v)
		}
	}
}

// CRLF line endings in a dotenv file.
func TestEnvCRLF(t *testing.T) {
	got := scan(t, map[string]string{"connections.example": "# c\r\nAPI_URL=https://api.mollie.com\r\nexport DEBUG=1\r\n"})
	for name, want := range map[string]string{
		"API_URL": "[env_example] [api.mollie.com] [connections.example:2@]",
		"DEBUG":   "[env_example] <nil> [connections.example:3@]",
	} {
		if got[name] != want {
			t.Errorf("%s = %s\n want %s", name, got[name], want)
		}
	}
}
