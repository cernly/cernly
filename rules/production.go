package rules

import (
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/ghfilter"
)

// roles applies the production rule to every stack. A job that would deploy but
// is a test job gives none, with the test-job evidence as its reason. A
// declared role replaces the derived one, which stays beside it; the
// declaration is always shown with who made it.
func roles(m *Map, facts []fact.Fact, branch string, decl *fact.Declared) {
	for _, st := range facts {
		if st.Kind != fact.KindStack {
			continue
		}
		sr := StackRole{Stack: st.Key, Role: RoleNone, Reasons: []Reason{}}
		wfs, _ := st.Attrs["workflows"].([]any)
		var unreadable []string
		for _, w := range wfs {
			wm, ok := w.(map[string]any)
			if !ok {
				continue
			}
			r := judge(wm, branch)
			if r.Role != RoleNone && wm["starts"] == false {
				// Absent in scans from before 4 Nov: judged as before.
				r.Role, r.Detail, r.Unreadable = RoleNone, RendersOnly, nil
			}
			if r.Role != RoleNone {
				if t := testJob(wm); t != nil {
					r.Role, r.Detail, r.Unreadable, r.TestJob = RoleNone, t.detail(r.Job, r.Workflow), nil, t
				}
			}
			if wm["manual"] == true {
				sr.Manual = true
			}
			if rank(r.Role) > rank(sr.Role) {
				sr.Role = r.Role
			}
			unreadable = append(unreadable, r.Unreadable...)
			sr.Reasons = append(sr.Reasons, r)
		}
		sort.Slice(sr.Reasons, func(i, j int) bool {
			if sr.Reasons[i].Workflow != sr.Reasons[j].Workflow {
				return sr.Reasons[i].Workflow < sr.Reasons[j].Workflow
			}
			return sr.Reasons[i].Job < sr.Reasons[j].Job
		})
		if decl != nil {
			for _, d := range decl.Stacks {
				if d.Stack != st.Str("name") {
					continue
				}
				sr.Name = d.Name
				if d.Role != "" {
					sr.DerivedRole, sr.Role = sr.Role, d.Role
				}
				sr.Declared = &Declarant{By: d.By, Line: d.Line}
			}
		}
		m.Stacks = append(m.Stacks, sr)
		if sr.Role == RoleUnconfirmed {
			d := "confirm once that this stack is production"
			if u := uniq(unreadable); len(u) > 0 {
				d += "; conditions the rules cannot read: " + strings.Join(u, "; ")
			}
			m.Findings = append(m.Findings, Finding{Rule: FindingProductionUnconfirmed, Subject: st.Key, Detail: d, From: []string{st.Key}})
		}
	}
}

// RendersOnly is the reason of a job that reaches a stack but never starts
// it: helm template or lint, kustomize build, compose config, --dry-run.
const RendersOnly = "renders only, does not deploy"

func rank(role string) int {
	switch role {
	case RoleProduction:
		return 2
	case RoleUnconfirmed:
		return 1
	}
	return 0
}

type tri int

const (
	no      tri = iota
	unknown     // may run on the default branch; a branch gate cannot settle it
	open        // runs on branches nobody filtered; a gate on the default branch makes it yes
	yes
)

// judge decides one workflow job, per trigger event: the trigger must run
// on the default branch and every condition of the job and the jobs it
// needs must admit it. The most production-like event wins.
func judge(w map[string]any, branch string) Reason {
	r := Reason{Workflow: anyStr(w["path"]), Job: anyStr(w["job"]), Role: RoleNone}
	events := anyStrs(w["events"])
	if len(events) == 1 && events[0] == "workflow_dispatch" {
		r.Detail = "manual only (workflow_dispatch)"
		return r
	}
	var unread []string
	conds := parseConds(w["conditions"], func(t string) { unread = append(unread, t) })
	r.Detail = "no trigger runs on " + branch
	picked := false
	for _, e := range events {
		t, d := no, ""
		switch e {
		case "workflow_dispatch":
			continue
		case "pull_request", "pull_request_target":
			continue
		case "push":
			bs := anyStrs(w["branches"])
			ign, hasIgn := w["branches_ignore"]
			_, hasTags := w["tags"]
			_, hasTagsIgn := w["tags_ignore"]
			switch {
			case len(bs) > 0:
				if ghfilter.Filter(bs, branch) {
					t, d = yes, "push on "+branch
				}
			case hasIgn:
				if !ghfilter.Filter(anyStrs(ign), branch) {
					t, d = yes, "push on "+branch+" (not in branches-ignore)"
				}
			case hasTags || hasTagsIgn:
				// Tags only: no branch push starts it.
			default:
				// Facts from before branches_ignore and tags were recorded.
				t, d = unknown, "push filter not readable (tags or branches-ignore)"
			}
		case "workflow_run":
			ab := anyStrs(w["after_branches"])
			ign, hasIgn := w["after_branches_ignore"]
			switch {
			case len(ab) > 0:
				if ghfilter.Filter(ab, branch) {
					t, d = yes, "workflow_run on "+branch
				}
			case hasIgn && ghfilter.Filter(anyStrs(ign), branch):
			default:
				t, d = open, "workflow_run after "+strings.Join(anyStrs(w["after"]), ", ")+" on any branch"
				if hasIgn {
					d += " not in branches-ignore"
				}
			}
		default:
			t, d = unknown, "trigger "+e+" not readable"
		}
		if t == no {
			continue
		}
		role := RoleUnconfirmed
		onMain := conds.eval(world{e, branch, branch}, func(t string) { unread = append(unread, t) })
		gated := onMain == kT && len(conds) > 0 && conds.eval(world{e, "", branch}, nil) == kF
		switch {
		case onMain == kF:
			role, d = RoleNone, d+"; if: excludes "+branch
		case onMain == kU:
		case t == yes, t == open && gated:
			role = RoleProduction
		}
		if gated {
			d += "; gated on " + branch
		}
		if !picked || rank(role) > rank(r.Role) {
			r.Role, r.Detail, picked = role, d, true
		}
	}
	r.Unreadable = uniq(unread)
	return r
}

// conds are the if: of a job, of every job it needs and of the step that
// reaches the stack; all must hold.
type conds []*exprNode

// parseConds parses each condition; one that cannot be parsed is listed as
// not readable as a whole and kept as nil.
func parseConds(v any, unread func(string)) conds {
	var out conds
	cs, _ := v.([]any)
	for _, c := range cs {
		cm, _ := c.(map[string]any)
		s := anyStr(cm["if"])
		n, err := parseIf(s)
		if err != nil {
			unread(strings.Join(strings.Fields(strings.ReplaceAll(s, `"`, `'`)), " "))
		}
		out = append(out, n)
	}
	return out
}

func (cs conds) eval(w world, unread func(string)) kleene {
	k := kT
	for _, c := range cs {
		if c == nil {
			k = k.and(kU)
			continue
		}
		k = k.and(c.eval(w, unread))
	}
	return k
}

func anyStr(v any) string { s, _ := v.(string); return s }

func anyStrs(v any) []string {
	xs, _ := v.([]any)
	var out []string
	for _, x := range xs {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
