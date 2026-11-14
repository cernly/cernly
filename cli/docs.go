package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/cernly/cernly/archdoc"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/pipeline"
	"github.com/cernly/cernly/rules"
)

// Docs writes the architecture documentation of one scan as Markdown: arc42
// sections, C4 context and container views in Mermaid, every statement with
// a footnote naming its facts, file and line.
func Docs(args []string) {
	fs := NewFlagSet("docs", args)
	branch, out := docsFlags(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "docs: exactly one facts file")
		os.Exit(2)
	}
	data, err := BuildDocs(fs.Arg(0), *branch, archdoc.Options{Version: pipeline.Version})
	if err != nil {
		fmt.Fprintln(os.Stderr, "docs:", err)
		os.Exit(1)
	}
	if *out == "-" {
		_, err = os.Stdout.Write(data)
	} else {
		err = os.WriteFile(*out, data, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
}

func docsFlags(fs *flag.FlagSet) (branch, out *string) {
	branch = fs.String("default-branch", "main", "`branch` whose deploys are production")
	out = fs.String("out", "-", "output `file`, - for stdout")
	return
}

// BuildDocs reads a facts file, derives its map and writes the Markdown.
func BuildDocs(path, branch string, o archdoc.Options) ([]byte, error) {
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
	return archdoc.Build(&s, m, o), nil
}
