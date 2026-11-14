package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/rules"
)

// Gate exit codes: 0 nothing or only warnings, 1 a finding at level block,
// 2 an error (as flag parsing already uses).
const (
	GateOK      = 0
	GateBlocked = 1
	GateError   = 2
)

// Gate compares a facts file with a base facts file the caller passes and
// exits 1 when a finding is at level block.
func Gate(args []string) {
	fs := NewFlagSet("gate", args)
	base, branch, format, out := gateFlags(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 1 || *base == "" {
		fmt.Fprintln(os.Stderr, "gate: --base <earlier.facts.json> and exactly one facts file")
		os.Exit(GateError)
	}
	res, err := RunGate(*base, fs.Arg(0), *branch)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gate:", err)
		os.Exit(GateError)
	}
	data, err := RenderGate(res, *format)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gate:", err)
		os.Exit(GateError)
	}
	Write(*out, data)
	warn, block := 0, 0
	for _, f := range res.Findings {
		if f.Level == config.LevelBlock {
			block++
		} else {
			warn++
		}
	}
	fmt.Fprintf(os.Stderr, "%d findings: %d block, %d warn\n", len(res.Findings), block, warn)
	if res.Blocked {
		os.Exit(GateBlocked)
	}
}

func gateFlags(fs *flag.FlagSet) (base, branch, format, out *string) {
	base = fs.String("base", "", "the earlier `facts.json` to compare with (required)")
	branch = fs.String("default-branch", "main", "`branch` whose deploys are production")
	format = fs.String("format", "text", "`text|json|sarif`")
	out = fs.String("out", "-", "output `file`, - for stdout")
	return
}

// RunGate reads both facts files, derives both maps with the same settings
// and compares them.
func RunGate(basePath, headPath, branch string) (*rules.GateResult, error) {
	load := func(path string) (*fact.Scan, *rules.Map, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		var s fact.Scan
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		m, err := MapFacts(raw, rules.Settings{DefaultBranch: branch})
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		return &s, m, nil
	}
	bs, bm, err := load(basePath)
	if err != nil {
		return nil, err
	}
	hs, hm, err := load(headPath)
	if err != nil {
		return nil, err
	}
	if bs.Repo != hs.Repo {
		return nil, fmt.Errorf("base is %s, head is %s: compare one repo", bs.Repo, hs.Repo)
	}
	return rules.Gate(bs, hs, bm, hm), nil
}

// RenderGate writes a result as text, canonical JSON or SARIF 2.1.0.
func RenderGate(r *rules.GateResult, format string) ([]byte, error) {
	switch format {
	case "json":
		return fact.Canonical(r)
	case "sarif":
		return json.MarshalIndent(sarif(r), "", "  ")
	case "text":
		return gateText(r), nil
	}
	return nil, fmt.Errorf("format %q: text, json or sarif", format)
}

func gateText(r *rules.GateResult) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s against %s\n", r.Head.Repo, short(r.Head.Commit), short(r.Base.Commit))
	if len(r.Findings) == 0 {
		b.WriteString("nothing new that the gate watches\n")
	}
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "%-5s %s: %s\n", f.Level, f.Rule, f.Detail)
		for _, e := range f.Evidence {
			fmt.Fprintf(&b, "      %s:%d\n", e.Path, e.Line)
		}
	}
	if r.Blocked {
		b.WriteString("blocked\n")
	}
	return []byte(strings.TrimSuffix(b.String(), "\n"))
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// SARIF 2.1.0, the subset GitHub code scanning reads.
type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name    string      `json:"name"`
	Version string      `json:"version"`
	Rules   []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID               string    `json:"id"`
	ShortDescription sarifText `json:"shortDescription"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifText       `json:"message"`
	Locations []sarifLocation `json:"locations,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
	Region           *sarifRegion  `json:"region,omitempty"`
}

type sarifArtifact struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

var gateRuleText = map[string]string{
	config.GateNewOutsideParty:    "A new outside party or a new host no one claims",
	config.GateNewSensitiveColumn: "A new column whose name looks sensitive",
	config.GateNewUnpinnedImage:   "A new image reference not pinned by digest",
	config.GateNewProductionStack: "A new production stack",
	config.GateNewDependency:      "A new direct dependency",
	config.GateNewTransitive:      "A new transitive dependency (from a lockfile)",
	config.GateLicenceNotAllowed:  "A new dependency whose declared licence is on the operator's list",
	config.GateLoosened:           "The change relaxes the gate or hides files from the scan",
}

func sarif(r *rules.GateResult) sarifLog {
	run := sarifRun{Tool: sarifTool{Driver: sarifDriver{Name: "cernly", Version: r.RulesVersion}}, Results: []sarifResult{}}
	for _, id := range config.GateRules {
		run.Tool.Driver.Rules = append(run.Tool.Driver.Rules, sarifRule{ID: id, ShortDescription: sarifText{gateRuleText[id]}})
	}
	for _, f := range r.Findings {
		level := "warning"
		if f.Level == config.LevelBlock {
			level = "error"
		}
		res := sarifResult{RuleID: f.Rule, Level: level, Message: sarifText{f.Detail}}
		for _, e := range f.Evidence {
			loc := sarifLocation{PhysicalLocation: sarifPhysical{ArtifactLocation: sarifArtifact{URI: e.Path}}}
			if e.Line > 0 {
				loc.PhysicalLocation.Region = &sarifRegion{StartLine: e.Line}
			}
			res.Locations = append(res.Locations, loc)
		}
		run.Results = append(run.Results, res)
	}
	return sarifLog{Schema: "https://json.schemastore.org/sarif-2.1.0.json", Version: "2.1.0", Runs: []sarifRun{run}}
}
