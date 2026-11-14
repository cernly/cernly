package rules

import (
	"fmt"
	"regexp"
	"strings"
)

// TestJob says why a CI job is a test job. A test job never makes a stack
// production.
type TestJob struct {
	By    string `json:"by"`    // job_id, job_name, step_name or runner
	Match string `json:"match"` // the name, or the first test runner command, as written
	Line  int    `json:"line"`  // line of the job or step in the workflow file
}

// testWords are whole words or segments of a name; "ci-check" is two
// segments in a row.
var testWords = map[string]bool{"test": true, "tests": true, "e2e": true, "integration": true}

// deployWords in any of the names keep a job from counting as a test job by
// its name: a job that tests and then deploys keeps today's verdict.
var deployWords = map[string]bool{"deploy": true, "deployment": true, "release": true, "publish": true,
	"production": true, "prod": true, "cd": true, "rollout": true, "ship": true}

var segRe = regexp.MustCompile(`[^a-z0-9]+`)

func segments(name string) []string {
	var out []string
	for _, s := range segRe.Split(strings.ToLower(name), -1) {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func hasTestWord(name string) bool {
	seg := segments(name)
	for i, s := range seg {
		if testWords[s] || (s == "ci" && i+1 < len(seg) && seg[i+1] == "check") {
			return true
		}
	}
	return false
}

func hasDeployWord(name string) bool {
	for _, s := range segments(name) {
		if deployWords[s] {
			return true
		}
	}
	return false
}

// testJob decides whether a workflow job (a workflows[] entry of a stack fact)
// is a test job: by name, or because every command after the stack starts is a
// setup command or a test runner, with at least one runner.
// A job with a GitHub environment, or a deploy word in any name, is never a
// test job by name; the runner test still applies to it.
func testJob(w map[string]any) *TestJob {
	step, _ := w["step"].(map[string]any)
	jobLine := anyInt(w["job_line"])
	names := []struct {
		by, name string
		line     int
	}{
		{"job_id", anyStr(w["job"]), jobLine},
		{"job_name", anyStr(w["job_name"]), jobLine},
		{"step_name", anyStr(step["name"]), anyInt(step["line"])},
	}
	veto := anyStr(w["environment"]) != ""
	for _, n := range names {
		veto = veto || hasDeployWord(n.name)
	}
	if !veto {
		for _, n := range names {
			if n.name != "" && n.line > 0 && hasTestWord(n.name) {
				return &TestJob{By: n.by, Match: n.name, Line: n.line}
			}
		}
	}
	if step == nil {
		return nil
	}
	after, _ := w["steps_after"].([]any)
	var first *TestJob
	for _, a := range after {
		sm, _ := a.(map[string]any)
		if u := anyStr(sm["uses"]); u != "" {
			if !strings.HasPrefix(u, "actions/") {
				return nil
			}
			continue
		}
		for _, c := range commands(anyStr(sm["run"])) {
			switch {
			case isRunner(c):
				if first == nil {
					first = &TestJob{By: "runner", Match: c, Line: anyInt(sm["line"])}
				}
			case isSetup(c):
			default:
				return nil
			}
		}
	}
	return first
}

// Detail is the reason in words, the same everywhere it is shown.
func (t *TestJob) detail(job, workflow string) string {
	at := fmt.Sprintf("%s:%d", workflow, t.Line)
	switch t.By {
	case "step_name":
		return fmt.Sprintf("not production: started by test step %q in job %s (%s)", t.Match, job, at)
	case "runner":
		return fmt.Sprintf("not production: job %s only runs tests after starting the stack (%s at %s)", job, t.Match, at)
	}
	return fmt.Sprintf("not production: started by test job %s (%s)", job, at)
}

func anyInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

var cmdSplit = regexp.MustCompile(`&&|;|\n`)

// commands splits a run: script into commands: lines (backslash continuations
// joined), then on && and ;. Comments and blank lines are dropped; leading
// VAR=value assignments are removed; whitespace is collapsed.
func commands(run string) []string {
	run = strings.ReplaceAll(run, "\\\n", " ")
	var out []string
	for _, c := range cmdSplit.Split(run, -1) {
		f := strings.Fields(c)
		for len(f) > 0 && strings.Contains(f[0], "=") && !strings.HasPrefix(f[0], "-") {
			f = f[1:]
		}
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		out = append(out, strings.Join(f, " "))
	}
	return out
}

// runnerTools are run directly or through npx, bunx, pnpm exec, pnpm dlx or
// yarn; the value is the subcommand they need, if any.
var runnerTools = map[string]string{"playwright": "test", "jest": "", "vitest": "", "cypress": "run"}

var pkgTest = regexp.MustCompile(`^(npm|pnpm|yarn|bun) (test|run test|run test:\S+)( |$)`)

// isRunner recognises the test runners:
// npm/pnpm/yarn/bun test, playwright test, go test, pytest, jest, vitest,
// cypress run.
func isRunner(c string) bool {
	if pkgTest.MatchString(c) || c == "npm t" || strings.HasPrefix(c, "npm t ") || strings.HasPrefix(c, "go test") && (len(c) == 7 || c[7] == ' ') {
		return true
	}
	f := strings.Fields(c)
	switch {
	case len(f) > 0 && (f[0] == "npx" || f[0] == "bunx" || f[0] == "yarn"):
		f = f[1:]
	case len(f) > 1 && f[0] == "pnpm" && (f[1] == "exec" || f[1] == "dlx"):
		f = f[2:]
	case len(f) > 2 && (f[0] == "python" || f[0] == "python3") && f[1] == "-m":
		f = f[2:]
	}
	if len(f) == 0 {
		return false
	}
	if f[0] == "pytest" {
		return true
	}
	sub, ok := runnerTools[f[0]]
	return ok && (sub == "" || len(f) > 1 && f[1] == sub)
}

var setupRe = regexp.MustCompile(`^(` +
	`(cd|echo|export|mkdir|cp|ls|cat|sleep|true|pwd)( .*)?` +
	`|npm (ci|install|i)( .*)?|pnpm (install|i)( .*)?|yarn( install( .*)?)?|bun install( .*)?` +
	`|(python3? -m )?pip3? install .*|go mod download( .*)?` +
	`|(npx |pnpm exec |yarn |bunx )?playwright install( .*)?` +
	`|(docker compose|docker-compose)( .*)? (ps|logs|down|stop|wait)( .*)?` +
	`)$`)

// isSetup recognises commands that prepare or tear down, never deploy.
func isSetup(c string) bool { return setupRe.MatchString(c) }
