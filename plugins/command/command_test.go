package command

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/composefile"
	"github.com/cernly/cernly/repo"
)

func TestMaskKeepsLengthAndRestores(t *testing.T) {
	src := []byte(`docker build -t "x:${{ github.sha }}" .`)
	masked, m := mask(srcWorkflow, src)
	if len(masked) != len(src) {
		t.Fatalf("length changed: %q", masked)
	}
	if got := m.restore(string(masked)); got != string(src) {
		t.Errorf("restore = %q", got)
	}
	mk, _ := mask(srcMake, []byte(`echo $(TAG) $$HOME`))
	if string(mk) != `echo ${TAG} \$HOME` {
		t.Errorf("make mask = %q", mk)
	}
}

func scanOne(t *testing.T, kind, text string) []found {
	t.Helper()
	fs, err := scan(&snippet{kind: kind, path: "x.sh", line: 10, text: []byte(text)})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestRecognise(t *testing.T) {
	fs := scanOne(t, srcScript, "set -e\nsudo docker buildx build --platform linux/amd64 --file=api/Dockerfile --target api -t a/api:1 -t a/api:latest api\n")
	if len(fs) != 1 || fs[0].line != 11 {
		t.Fatalf("found %+v", fs)
	}
	b := fs[0].build
	if b.dockerfile != "api/Dockerfile" || b.target != "api" || b.context != "api" || fmt.Sprint(b.tags) != "[a/api:1 a/api:latest]" {
		t.Errorf("build = %+v", b)
	}

	fs = scanOne(t, srcScript, "docker --context prod compose -p app --env-file .env -f a.yml -f b.yml up -d\n")
	c := fs[0].compose
	if fmt.Sprint(c.files) != "[a.yml b.yml]" || c.sub != "up" || fmt.Sprint(c.envFiles) != "[.env]" {
		t.Errorf("compose = %+v", c)
	}

	fs = scanOne(t, srcScript, "bash -c 'docker compose -f x.yml pull'\n")
	if len(fs) != 1 || fs[0].compose == nil || fs[0].compose.sub != "pull" {
		t.Errorf("bash -c: %+v", fs)
	}

	fs = scanOne(t, srcScript, "ssh -i key host bash -s <<'EOF'\ncd /srv\ndocker compose -f p.yml up -d\nEOF\n")
	if len(fs) != 1 || fs[0].line != 12 {
		t.Errorf("heredoc: %+v", fs)
	}

	// An unquoted heredoc: the shell removes \$ before sending, so the remote
	// program has a command substitution, not a stray (.
	fs = scanOne(t, srcScript, "ssh host bash -s <<REMOTE\ncd '${DIR}'\nC=(docker compose -f p.yml)\ndocker cp /tmp/d \\$(\"\\${C[@]}\" ps -q db):/tmp/d\ndocker compose -f p.yml up -d\nREMOTE\n")
	if len(fs) != 2 || fs[0].compose == nil || fs[0].compose.sub != "ps" || fs[1].compose == nil || fs[1].compose.sub != "up" || fs[1].line != 14 {
		t.Errorf("unquoted heredoc: %+v", fs)
	}

	// An ssh wrapper function: the remote program is the call's last argument
	// (a real deploy script).
	fs = scanOne(t, srcScript, "ssh_cmd() {\n  ssh ${OPTS} \"$@\"\n}\nssh_cmd \"$HOST\" \"cd /srv && docker compose --env-file e.env build && docker compose --env-file e.env up -d\"\n")
	var subs []string
	for _, f := range fs {
		if f.compose != nil {
			subs = append(subs, f.compose.sub)
		}
	}
	if fmt.Sprint(subs) != "[build up]" || fs[len(fs)-1].line != 13 {
		t.Errorf("ssh wrapper: %v %+v", subs, fs)
	}

	// An array expanding to more arguments than there are words: the -c
	// program is the array element's own word.
	if fs := scanOne(t, srcScript, "ARGS=(a b c d e)\nbash -c \"${ARGS[@]}\"\n"); len(fs) != 0 {
		t.Errorf("array after -c: %+v", fs)
	}
	fs = scanOne(t, srcScript, "ARGS=(-c 'docker compose -f x.yml pull')\nbash \"${ARGS[@]}\"\n")
	if len(fs) != 1 || fs[0].compose == nil || fs[0].compose.sub != "pull" {
		t.Errorf("array with -c: %+v", fs)
	}
}

func TestInvocations(t *testing.T) {
	cases := map[string]string{
		"./deploy/up.sh --fast":      "script [./deploy/up.sh]",
		"source lib/env.sh":          "script [lib/env.sh]",
		"task -d infra deploy":       "taskfile [deploy] infra",
		"make -C web build TAG=1":    "makefile [build] web",
		"npm run build --if-present": "npm [build] ",
	}
	for text, want := range cases {
		fs := scanOne(t, srcScript, text+"\n")
		if len(fs) != 1 || fs[0].invoke == nil {
			t.Errorf("%s: %+v", text, fs)
			continue
		}
		inv := fs[0].invoke
		got := fmt.Sprintf("%s %v", inv.kind, inv.names)
		if inv.dir != "" || inv.kind == srcNpm {
			got += " " + inv.dir
		}
		if got != want {
			t.Errorf("%s: got %q want %q", text, got, want)
		}
	}
}

func TestHelpers(t *testing.T) {
	if got := imageRepo("${REGISTRY}/ns/acme-api:${tag}"); got != "acme-api" {
		t.Errorf("imageRepo = %q", got)
	}
	if got := imageRepo("localhost:5000/app@sha256:abc"); got != "app" {
		t.Errorf("imageRepo digest = %q", got)
	}
	for files, want := range map[string]string{
		"deploy/docker-compose.edge.vps.yml": "edge.vps",
		"docker-compose.yml":                 "default",
		"compose.local.yaml":                 "local",
		"compose.override.yml":               "default",
	} {
		if got := composefile.StackName([]string{files}); got != want {
			t.Errorf("StackName(%s) = %q", files, got)
		}
	}
	if got := fmt.Sprint(ruleTargets("build test: deps")); got != "[build test]" {
		t.Errorf("ruleTargets = %s", got)
	}
	for _, l := range []string{"X := a:b", "X = a:b", ".PHONY: x", "\techo a:b"} {
		if r := ruleTargets(l); len(r) != 0 {
			t.Errorf("ruleTargets(%q) = %v", l, r)
		}
	}
	if !hasShellShebang([]byte("#!/usr/bin/env bash\n")) || hasShellShebang([]byte("#!/usr/bin/env python3\n")) {
		t.Error("shebang detection")
	}
}

// A remote build context keeps no credentials.
func TestRemoteContextCredentials(t *testing.T) {
	root := t.TempDir()
	wf := filepath.Join(root, ".github", "workflows")
	os.MkdirAll(wf, 0o755)
	os.WriteFile(filepath.Join(wf, "ci.yml"), []byte("on: push\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n"+
		"      - uses: docker/build-push-action@v6\n        with:\n          context: https://x:SECRET1@github.com/acme/api.git#main\n          tags: ghcr.io/acme/api:latest\n"+
		"      - run: docker build -t ghcr.io/acme/web:1 https://SECRET2@github.com/acme/web.git\n"), 0o644)
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range (Plugin{}).Run(r).Facts {
		if f.Kind == fact.KindImageBuild {
			got = append(got, fmt.Sprint(f.Attrs["image_repo"], f.Attrs["contexts"]))
		}
	}
	sort.Strings(got)
	if fmt.Sprint(got) != "[api[https://github.com/acme/api.git#main] web[https://github.com/acme/web.git]]" {
		t.Errorf("builds = %v", got)
	}
}

// CRLF line endings: a Makefile continuation still joins, and the evidence
// spans its lines.
func TestCRLF(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "Makefile"), []byte("build:\r\n\tdocker build \\\r\n\t  -t api ./svc\r\n"), 0o644)
	os.WriteFile(filepath.Join(root, "deploy.sh"), []byte("#!/bin/sh\r\ndocker build \\\r\n  -t web ./web\r\n"), 0o644)
	r, err := repo.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range (Plugin{}).Run(r).Facts {
		if f.Kind == fact.KindImageBuild {
			e := f.Evidence[0]
			got = append(got, fmt.Sprint(f.Attrs["image_repo"], f.Attrs["contexts"], " ", e.Path, ":", e.Line, "-", e.EndLine))
		}
	}
	sort.Strings(got)
	if fmt.Sprint(got) != "[api[./svc] Makefile:2-3 web[./web] deploy.sh:2-3]" {
		t.Errorf("builds = %v", got)
	}
}

func TestParseDotnetTest(t *testing.T) {
	cases := []struct {
		cmd, dir string
		nilWant  bool
	}{
		{cmd: "dotnet test", dir: ""},
		{cmd: "dotnet test tests/Shop.Tests/Shop.Tests.csproj", dir: "tests/Shop.Tests"},
		{cmd: "dotnet test ./tests/Shop.Tests/Shop.Tests.csproj", dir: "tests/Shop.Tests"},
		{cmd: "dotnet test Shop.sln", dir: "."},
		{cmd: "dotnet test src", dir: "src"},
		{cmd: "dotnet test src/ --no-build", dir: "src"},
		{cmd: "dotnet test ./", dir: "."},
		{cmd: "dotnet test --list-tests --no-build", nilWant: true},
		{cmd: "dotnet test --list-tests", nilWant: true},
		{cmd: "dotnet test -c Release tests/X/X.csproj", dir: "tests/X"},
		{cmd: "dotnet test -f net8.0 --logger trx -r linux-x64 src", dir: "src"},
		{cmd: "dotnet test --filter Category=Unit src", dir: "src"},
		{cmd: "dotnet test --configuration=Release Shop.sln", dir: "."},
		{cmd: "dotnet test --list-tests src", nilWant: true},
		{cmd: "dotnet test src --list-tests --no-build", nilWant: true},
		{cmd: "dotnet test --project tests/X/X.csproj", dir: "tests/X"},
		{cmd: "dotnet test --project=tests/X/X.csproj -c Release", dir: "tests/X"},
		{cmd: "dotnet test --solution Shop.slnx", dir: "."},
		{cmd: "dotnet test --solution=src/Shop.sln", dir: "src"},
		{cmd: "dotnet build", nilWant: true},
		{cmd: "dotnet publish", nilWant: true},
		{cmd: "dotnet test -t", nilWant: true},
		{cmd: "dotnet test -t src", nilWant: true},
		{cmd: `dotnet test .\tests\X\X.csproj`, dir: "tests/X"},
		{cmd: `dotnet test --project .\tests\X\X.csproj`, dir: "tests/X"},
		{cmd: `dotnet test src\`, dir: "src"},
		{cmd: "dotnet -d test tests/X/X.csproj", dir: "tests/X"},
		{cmd: "dotnet --diagnostics test src", dir: "src"},
	}
	for _, c := range cases {
		var args []arg
		for _, w := range strings.Fields(c.cmd) {
			args = append(args, arg{text: w, literal: true})
		}
		got := parseTest(args)
		if c.nilWant {
			if got != nil {
				t.Errorf("%q: want nil, got %+v", c.cmd, got)
			}
			continue
		}
		if got == nil || got.tool != "dotnet" || got.dir != c.dir {
			t.Errorf("%q: got %+v, want dotnet dir %q", c.cmd, got, c.dir)
		}
	}
}
