package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/imageref"
)

// CIImage is an image a CI job builds that no Compose service of the repo
// runs. It is a box of its own on the map, so the application a repo ships
// is not missing when its deployment lives elsewhere. It is never
// production: where it runs is not in this repo.
type CIImage struct {
	Image      string    `json:"image"` // ci_image:<dockerfile>:<stage>, the subject in attached[] and parties
	Recipe     string    `json:"recipe"`
	Dockerfile string    `json:"dockerfile"`
	Target     string    `json:"target"` // the stage built: the named target, or the final stage's name
	ImageRepos []string  `json:"image_repos"`
	Builds     []CIBuild `json:"builds"`
	Pushes     []Push    `json:"pushes"`
	Contexts   []string  `json:"contexts"`
	Sources    []string  `json:"sources"`
	Precision  string    `json:"precision"`
	Narrowing  Narrowing `json:"narrowing"`
	Narrowed   []string  `json:"narrowed,omitempty"`
	Excluded   []string  `json:"excluded,omitempty"`
	TestOnly   bool      `json:"test_only"`  // every CI job that builds it is a test job
	Deployment string    `json:"deployment"` // not_seen: nothing in this repo shows where it runs
	Detail     string    `json:"detail"`
	From       []string  `json:"from"`
}

// CIBuild is one CI job that builds the image.
type CIBuild struct {
	Build    string   `json:"build"` // image_build fact key
	Workflow string   `json:"workflow"`
	Job      string   `json:"job"`
	JobName  string   `json:"job_name,omitempty"`
	JobLine  int      `json:"job_line,omitempty"`
	Line     int      `json:"line,omitempty"` // the step that builds it, or reaches the script that does
	Events   []string `json:"events"`
	Branches []string `json:"branches"`
	TestJob  *TestJob `json:"test_job,omitempty"`
}

// Push is a registry repository the image is pushed to, as written, tag
// stripped. By push the build command pushes all its tags (build-push-action
// push: true, --push); by docker_push a later step of the same job runs
// docker push with that name (line is that step's).
type Push struct {
	Target   string `json:"target"`
	By       string `json:"by"` // push or docker_push
	Build    string `json:"build"`
	Workflow string `json:"workflow,omitempty"`
	Job      string `json:"job,omitempty"`
	Line     int    `json:"line,omitempty"`
}

// DeploymentNotSeen: no deployment of the image is in the repo. Kubernetes,
// Helm or Terraform adapters may later show where an image runs; until then
// this is the only value.
const DeploymentNotSeen = "not_seen"

// ciImages applies the rule to every image_build a CI job reaches whose
// Dockerfile and stage resolve to a recipe. A build a Compose service is
// linked through (the service runs that image) is already on the map and is
// left out. Builds of one recipe make one image.
func ciImages(m *Map, facts []fact.Fact) []CIImage {
	used := map[string]bool{} // image_build keys a link goes through
	for _, l := range m.Links {
		for _, k := range l.From {
			if strings.HasPrefix(k, fact.KindImageBuild+":") {
				used[k] = true
			}
		}
	}
	rec := recipes{}
	for _, f := range facts {
		if f.Kind != fact.KindImageRecipe {
			continue
		}
		df := f.Str("dockerfile")
		if rec[df] == nil {
			rec[df] = map[string]fact.Fact{}
		}
		rec[df][f.Str("target")] = f
		if f.Attrs["final"] == true {
			rec[df][""] = f
		}
	}
	type agg struct {
		recipe   fact.Fact
		img      CIImage
		contexts []string
		test     bool
	}
	by := map[string]*agg{}
	for _, b := range facts {
		if b.Kind != fact.KindImageBuild || b.Attrs["ci_invoked"] != true || b.Attrs["dockerfile_resolved"] != true || used[b.Key] {
			continue
		}
		r, ok := rec.find(b.Str("dockerfile"), b.Str("target"))
		if !ok {
			continue // no recipe: the build is a no_code_link case, not a box
		}
		a := by[r.Key]
		if a == nil {
			a = &agg{recipe: r, test: true, img: CIImage{Recipe: r.Key, Dockerfile: r.Str("dockerfile"), Target: r.Str("target"),
				Image: "ci_image:" + strings.TrimPrefix(r.Key, fact.KindImageRecipe+":"), Precision: "module", Deployment: DeploymentNotSeen,
				Builds: []CIBuild{}, Pushes: []Push{}}}
			by[r.Key] = a
		}
		a.img.ImageRepos = append(a.img.ImageRepos, b.Str("image_repo"))
		a.img.From = append(a.img.From, b.Key, r.Key)
		a.contexts = append(a.contexts, b.Strs("contexts")...)
		if b.Attrs["push"] == true {
			for _, t := range b.Strs("tags") {
				a.img.Pushes = append(a.img.Pushes, Push{Target: imageref.Name(t), By: "push", Build: b.Key})
			}
		}
		wfs, _ := b.Attrs["workflows"].([]any)
		for _, w := range wfs {
			wm, ok := w.(map[string]any)
			if !ok {
				continue
			}
			step, _ := wm["step"].(map[string]any)
			cb := CIBuild{Build: b.Key, Workflow: anyStr(wm["path"]), Job: anyStr(wm["job"]), JobName: anyStr(wm["job_name"]),
				JobLine: anyInt(wm["job_line"]), Line: anyInt(step["line"]), Events: orEmpty(anyStrs(wm["events"])),
				Branches: orEmpty(anyStrs(wm["branches"])), TestJob: testJob(wm)}
			if cb.TestJob == nil {
				a.test = false
			}
			a.img.Builds = append(a.img.Builds, cb)
			a.img.Pushes = append(a.img.Pushes, dockerPushes(wm, b.Str("image_repo"), b.Key, cb)...)
		}
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []CIImage{}
	for _, k := range keys {
		a := by[k]
		img := a.img
		img.ImageRepos = uniq(img.ImageRepos)
		img.From = uniq(img.From)
		img.Contexts = uniq(a.contexts)
		img.Sources = inContexts(a.recipe.Strs("sources"), img.Contexts)
		img.Narrowing, img.Narrowed, img.Excluded = narrow(a.recipe, img.Sources, facts)
		img.Precision = precisionOf(img.Narrowing)
		img.TestOnly = a.test && len(img.Builds) > 0
		sort.Slice(img.Builds, func(i, j int) bool {
			x, y := img.Builds[i], img.Builds[j]
			if x.Workflow != y.Workflow {
				return x.Workflow < y.Workflow
			}
			if x.Job != y.Job {
				return x.Job < y.Job
			}
			return x.Build < y.Build
		})
		img.Pushes = uniqPushes(img.Pushes)
		img.Detail = ciDetail(img)
		out = append(out, img)
	}
	return out
}

func orEmpty(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// dockerPushes finds docker push (or docker image push) commands in the steps
// after the build step whose image has the build's image repo.
func dockerPushes(w map[string]any, repo, build string, cb CIBuild) []Push {
	var out []Push
	after, _ := w["steps_after"].([]any)
	for _, s := range after {
		sm, _ := s.(map[string]any)
		for _, c := range commands(anyStr(sm["run"])) {
			f := strings.Fields(c)
			switch {
			case len(f) > 2 && f[0] == "docker" && f[1] == "push":
				f = f[2:]
			case len(f) > 3 && f[0] == "docker" && f[1] == "image" && f[2] == "push":
				f = f[3:]
			default:
				continue
			}
			for _, a := range f {
				if strings.HasPrefix(a, "-") {
					continue
				}
				ref := strings.Trim(a, `"'`)
				if imageref.Repo(ref) == repo {
					out = append(out, Push{Target: imageref.Name(ref), By: "docker_push", Build: build, Workflow: cb.Workflow, Job: cb.Job, Line: anyInt(sm["line"])})
				}
			}
		}
	}
	return out
}

func uniqPushes(ps []Push) []Push {
	sort.Slice(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		if a.By != b.By {
			return a.By < b.By
		}
		if a.Build != b.Build {
			return a.Build < b.Build
		}
		if a.Workflow != b.Workflow {
			return a.Workflow < b.Workflow
		}
		if a.Job != b.Job {
			return a.Job < b.Job
		}
		return a.Line < b.Line
	})
	out := []Push{}
	for i, p := range ps {
		if i == 0 || p != ps[i-1] {
			out = append(out, p)
		}
	}
	return out
}

// ciDetail is the image in words, the same everywhere it is shown.
func ciDetail(img CIImage) string {
	var b strings.Builder
	target := img.Target
	if target == "_final" {
		target = "" // an unnamed final stage
	}
	fmt.Fprintf(&b, "built in CI from %s", stageRef(img.Dockerfile, target))
	if len(img.Builds) > 0 {
		x := img.Builds[0]
		line := x.Line
		if line == 0 {
			line = x.JobLine
		}
		fmt.Fprintf(&b, " by job %s (%s:%d)", x.Job, x.Workflow, line)
		if n := len(img.Builds) - 1; n > 0 {
			fmt.Fprintf(&b, " and %d more", n)
		}
	}
	if img.TestOnly {
		x := img.Builds[0]
		at := fmt.Sprintf("%s:%d", x.Workflow, x.TestJob.Line)
		switch x.TestJob.By {
		case "step_name":
			fmt.Fprintf(&b, "; test only: built by test step %q in job %s (%s)", x.TestJob.Match, x.Job, at)
		case "runner":
			fmt.Fprintf(&b, "; test only: job %s only runs tests after the build (%s at %s)", x.Job, x.TestJob.Match, at)
		default:
			fmt.Fprintf(&b, "; test only: built by test job %s (%s)", x.Job, at)
		}
		if len(img.Builds) > 1 {
			b.WriteString(", as is every other job that builds it")
		}
	}
	if len(img.Pushes) > 0 {
		var ts []string
		for _, p := range img.Pushes {
			ts = append(ts, p.Target)
		}
		fmt.Fprintf(&b, "; pushed to %s", strings.Join(uniq(ts), ", "))
	}
	b.WriteString("; deployment not seen in this repo")
	return b.String()
}
