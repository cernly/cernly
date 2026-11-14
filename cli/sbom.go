package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/pipeline"
	"github.com/cernly/cernly/rules"
	"github.com/cernly/cernly/sbom"
)

// SBOM writes a CycloneDX 1.6 bill of materials for one scan: the whole
// repo, or one service with --service.
func SBOM(args []string) {
	fs := NewFlagSet("sbom", args)
	branch, service, stack, out := sbomFlags(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "sbom: exactly one facts file")
		os.Exit(2)
	}
	data, err := BuildSBOMScope(fs.Arg(0), *branch, sbom.Scope{Service: *service, Stack: *stack})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sbom:", err)
		os.Exit(1)
	}
	Write(*out, data)
}

func sbomFlags(fs *flag.FlagSet) (branch, service, stack, out *string) {
	branch = fs.String("default-branch", "main", "`branch` whose deploys are production")
	service = fs.String("service", "", "one service's `key` (service:compose:<stack>:<name> or ci_image:...); default the whole repo")
	stack = fs.String("stack", "", "the services of one stack by its `key` (stack:compose:<name>), e.g. a production stack")
	out = fs.String("out", "-", "output `file`, - for stdout")
	return
}

// BuildSBOM reads a facts file, derives its map and writes the BOM as
// canonical JSON.
func BuildSBOM(path, branch, service string) ([]byte, error) {
	return BuildSBOMScope(path, branch, sbom.Scope{Service: service})
}

// BuildSBOMScope is BuildSBOM for a service or a stack.
func BuildSBOMScope(path, branch string, sc sbom.Scope) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s fact.Scan
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m, err := MapFacts(raw, rules.Settings{DefaultBranch: branch})
	if err != nil {
		return nil, err
	}
	b, err := sbom.BuildScope(&s, m, sc, pipeline.Version)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "%d components, %d dependency entries\n", len(b.Components), len(b.Dependencies))
	return fact.Canonical(b)
}
