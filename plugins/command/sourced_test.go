package command

import (
	"fmt"
	"testing"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/repo/repotest"
)

// runFiles writes files into a fresh folder and returns the plugin's stack
// facts by key.
func runFiles(t *testing.T, files map[string]string) map[string]fact.Fact {
	t.Helper()
	r := repotest.Files(t, files)
	res := Plugin{}.Run(r)
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	out := map[string]fact.Fact{}
	for _, f := range res.Facts {
		if f.Kind == fact.KindStack {
			out[f.Key] = f
		}
	}
	return out
}

func composeFiles(files ...string) map[string]string {
	m := map[string]string{}
	for _, f := range files {
		m[f] = "services:\n  app:\n    image: example/app:1\n"
	}
	return m
}

func with(base map[string]string, more map[string]string) map[string]string {
	for k, v := range more {
		base[k] = v
	}
	return base
}

func hasEv(f fact.Fact, p string, line int) bool {
	for _, e := range f.Evidence {
		if e.Path == p && e.Line == line {
			return true
		}
	}
	return false
}

// A script that sources a repo file sees the arrays and functions it sets up:
// a literal path from the script's folder, or one under a variable set from
// the script's own location or the repo root.
func TestSourceFollowed(t *testing.T) {
	lib := "FILES=(-f compose.base.yml -f ops/compose.live.yml)\n"
	for name, script := range map[string]string{
		"literal next to the script": "source lib/files.sh\ndocker compose \"${FILES[@]}\" up -d\n",
		"dirname of BASH_SOURCE":     "ROOT=\"$(cd \"$(dirname \"${BASH_SOURCE[0]}\")/..\" && pwd)\"\nsource \"$ROOT/ops/lib/files.sh\"\ndocker compose \"${FILES[@]}\" up -d\n",
		"dot and dirname of $0":      "DIR=$(dirname \"$0\")\n. \"$DIR/lib/files.sh\"\ndocker compose \"${FILES[@]}\" up -d\n",
		"git toplevel":               "ROOT=$(git rev-parse --show-toplevel)\nsource \"${ROOT}/ops/lib/files.sh\"\ndocker compose \"${FILES[@]}\" up -d\n",
		"script folder with %/*":     "here=${BASH_SOURCE[0]%/*}\nsource \"$here/lib/files.sh\"\ndocker compose \"${FILES[@]}\" up -d\n",
	} {
		t.Run(name, func(t *testing.T) {
			m := runFiles(t, with(composeFiles("compose.base.yml", "ops/compose.live.yml"), map[string]string{
				"ops/up.sh": script, "ops/lib/files.sh": lib,
			}))
			st, ok := m["stack:compose:live"]
			if !ok || fmt.Sprint(st.Attrs["files"]) != "[compose.base.yml ops/compose.live.yml]" || st.Attrs["unresolved"] != nil {
				t.Fatalf("stacks = %v", m)
			}
			if !hasEv(st, "ops/lib/files.sh", 1) {
				t.Errorf("the sourced file's array line is evidence: %v", st.Evidence)
			}
		})
	}
	// A -f path under the script's own location reads as the file it names.
	m0 := runFiles(t, with(composeFiles("compose.base.yml"), map[string]string{
		"ops/up.sh": "ROOT=\"$(cd \"$(dirname \"$0\")/..\" && pwd)\"\nTAG=v1\ndocker compose -f \"$ROOT/compose.base.yml\" up -d\ndocker build -t \"example/app:$TAG\" .\n",
	}))
	if st := m0["stack:compose:base"]; fmt.Sprint(st.Attrs["files"]) != "[compose.base.yml]" || st.Attrs["unresolved"] != nil {
		t.Errorf("-f under ROOT: %v", m0)
	}
	// A path the reader cannot follow leaves the expansion unresolved: no stack.
	m := runFiles(t, with(composeFiles("compose.base.yml"), map[string]string{
		"ops/up.sh": "source \"$CONFIG_DIR/files.sh\"\ndocker compose \"${FILES[@]}\" up -d\n", "ops/lib/files.sh": lib,
	}))
	if len(m) != 0 {
		t.Errorf("unknown source path: want no stack, got %v", m)
	}
}

// A function that assigns or appends to a global array sets it for the
// commands after the call, also when the array is copied into another one.
func TestFunctionArrays(t *testing.T) {
	m := runFiles(t, with(composeFiles("a.yml", "b.yml", "c.yml"), map[string]string{
		"up.sh": `files() {
  LIST=(-f a.yml)
  LIST+=(-f b.yml)
}
files
COPY=("${LIST[@]}" -f c.yml)
docker compose "${COPY[@]}" up -d
`}))
	if st := m["stack:compose:c"]; fmt.Sprint(st.Attrs["files"]) != "[a.yml b.yml c.yml]" {
		t.Errorf("stacks = %v", m)
	}
	// Before the call the array is not set: the command stays unresolved.
	m = runFiles(t, with(composeFiles("a.yml"), map[string]string{
		"up.sh": "files() { LIST=(-f a.yml); }\nCOPY=(\"${LIST[@]}\")\nfiles\ndocker compose \"${COPY[@]}\" up -d\n"}))
	if len(m) != 0 {
		t.Errorf("copy of an unset array: want no stack, got %v", m)
	}
	// A copy of an array built from an expansion is not guessed.
	m = runFiles(t, with(composeFiles("a.yml"), map[string]string{
		"up.sh": "LIST=(-f \"$1\")\nCOPY=(\"${LIST[@]}\")\ndocker compose \"${COPY[@]}\" up -d\n"}))
	if len(m) != 0 {
		t.Errorf("copy of an unreadable array: want no stack, got %v", m)
	}
}

// A chain of wrapper functions is followed to the command it runs, and each
// wrapper's line is evidence.
func TestWrapperChain(t *testing.T) {
	m := runFiles(t, with(composeFiles("a.yml", "b.yml"), map[string]string{
		"up.sh": `FILES=(-f a.yml -f b.yml)
compose() { docker compose "${FILES[@]}" "$@"; }
compose_up() { compose up -d --remove-orphans "$@"; }
deploy() { compose_up --wait "$@"; }
deploy
compose ps
`}))
	st := m["stack:compose:b"]
	if fmt.Sprint(st.Attrs["files"]) != "[a.yml b.yml]" || st.Attrs["starts"] != true {
		t.Fatalf("stacks = %v", m)
	}
	for _, line := range []int{2, 3, 4, 5, 6} {
		if !hasEv(st, "up.sh", line) {
			t.Errorf("evidence lacks up.sh:%d: %v", line, st.Evidence)
		}
	}
}

// Appends after `check || return 0`, inside if, or after `check &&` are
// conditional: kept out of files and recorded as conditional_files with the
// condition as written. A conditional replacement makes the array unknown.
func TestConditionalAppends(t *testing.T) {
	m := runFiles(t, with(composeFiles("a.yml", "b.yml", "c.yml", "d.yml"), map[string]string{
		"lib.sh": `extra_enabled() { [ -f /etc/extra ]; }
files() {
  LIST=(-f a.yml)
  if [ -n "${WITH_C:-}" ]; then
    LIST+=(-f c.yml)
  fi
  [ -f /etc/d ] && LIST+=(-f d.yml)
  extra_enabled || return 0
  LIST+=(-f b.yml)
}
`,
		"up.sh": "source ./lib.sh\nfiles\ndocker compose \"${LIST[@]}\" up -d\n",
	}))
	st, ok := m["stack:compose:a"]
	if !ok || fmt.Sprint(st.Attrs["files"]) != "[a.yml]" {
		t.Fatalf("stacks = %v", m)
	}
	got := map[string]string{}
	for _, c := range st.Attrs["conditional_files"].([]any) {
		e := c.(map[string]any)
		got[e["file"].(string)] = fmt.Sprintf("%v@%v:%v line %v", e["condition"], e["condition_path"], e["condition_line"], e["line"])
	}
	want := map[string]string{
		"b.yml": "extra_enabled || return 0@lib.sh:8 line 9",
		"c.yml": "if [ -n \"${WITH_C:-}\" ]@lib.sh:4 line 5",
		"d.yml": "[ -f /etc/d ] && LIST+=(-f d.yml)@lib.sh:7 line 7",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("conditional_files = %v, want %v", got, want)
	}
	m = runFiles(t, with(composeFiles("a.yml", "b.yml"), map[string]string{
		"up.sh": "LIST=(-f a.yml)\nif [ -f /etc/b ]; then LIST=(-f b.yml); fi\ndocker compose \"${LIST[@]}\" up -d\n"}))
	if len(m) != 0 {
		t.Errorf("conditional replacement: want no stack, got %v", m)
	}
}

// A remote command after a cd on the host is read from the repo root; the
// stack records that assumption with the ssh line.
func TestRemoteCdAssumption(t *testing.T) {
	wf := `on:
  push:
    branches: [main]
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - run: ssh deploy@host "cd '${APP_DIR}' && bash ops/up.sh"
      - run: ssh deploy@host "bash ops/other.sh"
`
	m := runFiles(t, with(composeFiles("a.yml", "b.yml"), map[string]string{
		".github/workflows/cd.yml": wf,
		"ops/up.sh":                "docker compose -f a.yml up -d\n",
		"ops/other.sh":             "docker compose -f b.yml up -d\n",
	}))
	a, b := m["stack:compose:a"], m["stack:compose:b"]
	if a.Attrs["ci_invoked"] != true || b.Attrs["ci_invoked"] != true {
		t.Fatalf("stacks = %v", m)
	}
	as, _ := a.Attrs["assumptions"].([]any)
	if len(as) != 1 || fmt.Sprint(as[0].(map[string]any)["line"]) != "8" ||
		as[0].(map[string]any)["text"] != "remote command after cd '${APP_DIR}': ops/up.sh is read from the repo root, assuming the remote folder is a checkout of this repo" {
		t.Errorf("assumptions = %v", a.Attrs["assumptions"])
	}
	if b.Attrs["assumptions"] != nil {
		t.Errorf("no cd, no assumption: %v", b.Attrs["assumptions"])
	}
}

// Files that are only set under a condition, or that cannot be read, never
// fall back to compose's default file (docker-compose.yml), which docker
// would use without -f.
func TestNoDefaultFallback(t *testing.T) {
	for name, script := range map[string]string{
		"only conditional":                "LIST=()\n[ \"${1:-}\" = --extra ] && LIST+=(-f a.yml)\ndocker compose \"${LIST[@]}\" up -d\n",
		"set by a call under a condition": "files() { LIST=(-f a.yml); }\nif [ -n \"${X:-}\" ]; then files; fi\ndocker compose \"${LIST[@]}\" up -d\n",
		"unreadable":                      "LIST=(-f \"$1\")\ndocker compose \"${LIST[@]}\" up -d\n",
	} {
		t.Run(name, func(t *testing.T) {
			m := runFiles(t, with(composeFiles("docker-compose.yml", "a.yml"), map[string]string{"up.sh": script}))
			if len(m) != 0 {
				t.Errorf("want no stack, got %v", m)
			}
		})
	}
}

// An element added under the same condition the command runs under is
// certain for that command: a guard before both, or one if around both.
func TestSameConditionIsCertain(t *testing.T) {
	for name, script := range map[string]string{
		"guard":  "[ -f .env ] || exit 1\nLIST=(-f a.yml)\ndocker compose \"${LIST[@]}\" up -d\n",
		"one if": "if [ -n \"${DEPLOY:-}\" ]; then\n  LIST=(-f a.yml)\n  docker compose \"${LIST[@]}\" up -d\nfi\n",
	} {
		t.Run(name, func(t *testing.T) {
			m := runFiles(t, with(composeFiles("a.yml"), map[string]string{"up.sh": script}))
			st := m["stack:compose:a"]
			if fmt.Sprint(st.Attrs["files"]) != "[a.yml]" || st.Attrs["conditional_files"] != nil {
				t.Errorf("stacks = %v", m)
			}
		})
	}
}

// A call inside a function body sees the state at the function's call, not
// at its definition: the same wrapper with the same array is one stack.
func TestFunctionBodyAtCall(t *testing.T) {
	m := runFiles(t, with(composeFiles("a.yml", "b.yml"), map[string]string{"up.sh": `dump() { compose exec -T db pg_dump; }
FILES=(-f a.yml -f b.yml)
compose() { docker compose "${FILES[@]}" "$@"; }
compose up -d
case "${1:-}" in
  dump) dump ;;
esac
`}))
	st := m["stack:compose:b"]
	if len(m) != 1 || !hasEv(st, "up.sh", 1) || !hasEv(st, "up.sh", 4) {
		t.Errorf("stacks = %v", m)
	}
}

// One deploy never forms a variant stack per host condition: a guard before
// everything holds for every element, so an append under a further condition
// inside it stays conditional, also for a call under that same condition.
func TestNoVariantStack(t *testing.T) {
	m := runFiles(t, with(composeFiles("a.yml", "b.yml", "p.yml"), map[string]string{
		"lib.sh": "pitr_on() { [ -f /etc/pitr ]; }\nfiles() {\n  LIST=(-f a.yml -f b.yml)\n  pitr_on || return 0\n  LIST+=(-f p.yml)\n}\n",
		"up.sh": `[ -f .env ] || exit 1
source ./lib.sh
files
compose() { docker compose "${LIST[@]}" "$@"; }
if pitr_on; then compose pull db; fi
pull_pitr() {
  pitr_on || return 0
  LIST+=(-f p.yml)
  compose pull wal
}
pull_pitr
compose up -d
`}))
	st, ok := m["stack:compose:b"]
	if len(m) != 1 || !ok || fmt.Sprint(st.Attrs["files"]) != "[a.yml b.yml]" {
		t.Fatalf("want one stack a+b, got %v", m)
	}
	conds := map[string]bool{}
	for _, c := range st.Attrs["conditional_files"].([]any) {
		e := c.(map[string]any)
		conds[fmt.Sprintf("%v %v:%v", e["file"], e["condition_path"], e["condition_line"])] = true
	}
	if !conds["p.yml lib.sh:4"] || !conds["p.yml up.sh:7"] {
		t.Errorf("conditional_files = %v", st.Attrs["conditional_files"])
	}
	for _, line := range []int{5, 9, 12} {
		if !hasEv(st, "up.sh", line) {
			t.Errorf("evidence lacks up.sh:%d: %v", line, st.Evidence)
		}
	}
}
