// Package cli is the open scanner's command line: scan one commit, derive its
// map, serve both to AI agents over MCP, compare a scan with a base scan the
// caller passes, write its bill of materials, write its architecture docs. Other
// commands can wrap it. See https://cernly.dev/how/open-core/.
//
//	scan [--repo id] [--out file] [--commit sha] [--committed-at rfc3339] [--exclude glob]... [--include glob]... [--no-repo-config] [--config file] [--plugins dir] [--trust-key key.pub]... <dir>
//	map [--default-branch main] [--out file] [--confirmations file] <facts.json>
//	mcp [--default-branch main] <facts.json | dir>
//	sbom [--default-branch main] [--service key] [--stack key] [--out file] <facts.json>
//	gate --base <earlier.facts.json> [--default-branch main] [--format text|json|sarif] [--out file] <facts.json>
//	docs [--default-branch main] [--out file.md] <facts.json>
//	plugin keygen [--test] --out prefix
//	plugin sign --key key.key --manifest manifest.json [--out dir] <module.wasm>
//	plugin verify [--trust-key key.pub]... <dir>
//	help | -h | --help
//
// [Usage] prints the same list, built from the flag sets. The plugin commands sign and verify WebAssembly plugins.
//
// Same commit + same binary + same operator flags = byte-identical output.
// The map is derived from a facts file alone: same facts + same rules
// version + same settings = byte-identical map.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/mcp"
	"github.com/cernly/cernly/pipeline"
	"github.com/cernly/cernly/plugin"
	_ "github.com/cernly/cernly/plugins/all"
	"github.com/cernly/cernly/rules"
)

type command struct {
	name  string
	flags func(*flag.FlagSet)
	args  string
}

// commands lists every command, subcommand and its operands. Usage builds the
// flags from the same functions the commands parse with, so it cannot drift.
var commands = []command{
	{"scan", func(fs *flag.FlagSet) { scanFlags(fs) }, "<dir>"},
	{"map", func(fs *flag.FlagSet) { mapFlags(fs) }, "<facts.json>"},
	{"mcp", func(fs *flag.FlagSet) { mcpFlags(fs) }, "<facts.json | dir>"},
	{"sbom", func(fs *flag.FlagSet) { sbomFlags(fs) }, "<facts.json>"},
	{"gate", func(fs *flag.FlagSet) { gateFlags(fs) }, "<facts.json>"},
	{"docs", func(fs *flag.FlagSet) { docsFlags(fs) }, "<facts.json>"},
	{"plugin keygen", func(fs *flag.FlagSet) { keygenFlags(fs) }, ""},
	{"plugin sign", func(fs *flag.FlagSet) { signFlags(fs) }, "<module.wasm>"},
	{"plugin verify", func(fs *flag.FlagSet) { verifyFlags(fs) }, "<dir>"},
}

// MapFacts derives a map from a facts file. A wrapping command may replace it
// to accept more schema ids than the open core does.
var MapFacts = pipeline.Map

// Usage lists the open commands with every flag each accepts, for a wrapping
// command's own usage text. Each extra line is added after the open commands.
func Usage(prog string, extra ...string) string {
	var b strings.Builder
	for i, c := range commands {
		fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		c.flags(fs)
		if i == 0 {
			b.WriteString("usage: ")
		} else {
			b.WriteString("       ")
		}
		b.WriteString(prog + " " + c.name)
		fs.VisitAll(func(f *flag.Flag) {
			name, _ := flag.UnquoteUsage(f)
			open, shut := " [", "]"
			if strings.Contains(f.Usage, "(required)") {
				open, shut = " ", ""
			}
			b.WriteString(open + "--" + f.Name)
			if name != "" {
				b.WriteString(" " + name)
			}
			b.WriteString(shut)
			if _, ok := f.Value.(*Multi); ok {
				b.WriteString("...")
			}
		})
		if c.args != "" {
			b.WriteString(" " + c.args)
		}
		b.WriteString("\n")
	}
	for _, l := range extra {
		b.WriteString("       " + l + "\n")
	}
	b.WriteString("       " + prog + " help\n")
	b.WriteString("run a command with -h to describe its flags\n")
	return b.String()
}

// NewFlagSet returns the flag set of a command that exits on a parse error.
// Asked for help with -h or --help, it prints the flags to stdout and exits
// 0; a parse error still goes to stderr.
func NewFlagSet(name string, args []string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "-h" || a == "-help" || a == "--help" || a == "--h" {
			fs.SetOutput(os.Stdout)
			break
		}
	}
	return fs
}

// IsHelp reports whether args ask for the usage text: help, -h or --help.
func IsHelp(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "help", "-h", "--help":
		return true
	}
	return false
}

// Main runs one of the open commands and reports whether args named one.
func Main(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "scan":
		Scan(args[1:])
	case "map":
		Map(args[1:])
	case "mcp":
		MCP(args[1:])
	case "gate":
		Gate(args[1:])
	case "sbom":
		SBOM(args[1:])
	case "plugin":
		PluginCmd(args[1:])
	case "docs":
		Docs(args[1:])
	default:
		return false
	}
	return true
}

// Multi is a repeatable string flag.
type Multi []string

func (m *Multi) String() string     { return strings.Join(*m, ",") }
func (m *Multi) Set(v string) error { *m = append(*m, v); return nil }

// Scan runs every plugin on one commit and writes the facts file.
func Scan(args []string) {
	fs := NewFlagSet("scan", args)
	o := scanFlags(fs)
	repoID, out, sha, at, op, pf := o.repo, o.out, o.commit, o.at, &o.op, &o.pf
	_ = fs.Parse(args)
	ReadOperatorFile(op, *o.config)
	ps, err := pf.Plugins()
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan:", err)
		os.Exit(1)
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "scan: exactly one directory")
		os.Exit(2)
	}
	dir := fs.Arg(0)

	scan, warnings, err := pipeline.Scan(dir, pipeline.ScanOptions{RepoID: *repoID, Commit: *sha, CommittedAt: *at, Plugins: ps, Operator: *op})
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan:", err)
		os.Exit(1)
	}
	if *repoID == "" && strings.HasPrefix(scan.Repo, "local/") {
		warnings = append(warnings, fmt.Sprintf("repo id %s: no origin remote; pass --repo to give the scan a stable id", scan.Repo))
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	data, err := fact.Canonical(scan)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
	Write(*out, data)
	failed := 0
	for _, p := range scan.Plugins {
		if p.Status != "ok" {
			failed++
			fmt.Fprintf(os.Stderr, "plugin %s: %s (%d errors)\n", p.Name, p.Status, len(p.Errors))
		}
	}
	fmt.Fprintf(os.Stderr, "%d facts, %d plugins, %d not ok\n", len(scan.Facts), len(scan.Plugins), failed)
}

type scanOpts struct {
	repo, out, commit, at, config *string
	op                            config.Operator
	pf                            PluginFlags
}

func scanFlags(fs *flag.FlagSet) *scanOpts {
	o := &scanOpts{}
	o.repo = fs.String("repo", "", "canonical repository `id` (default: from git remote origin)")
	o.out = fs.String("out", "-", "output `file`, - for stdout")
	o.commit = fs.String("commit", "", "commit `sha` (default: git HEAD)")
	o.at = fs.String("committed-at", "", "committer date as `rfc3339` (default: from git)")
	fs.Var((*Multi)(&o.op.Exclude), "exclude", "operator exclusion `glob` (repeatable; recorded in the output)")
	fs.Var((*Multi)(&o.op.Include), "include", "re-include a `glob` the repo's "+config.FileName+" excluded (repeatable)")
	fs.BoolVar(&o.op.NoRepoConfig, "no-repo-config", false, "ignore the repo's "+config.FileName+" (recorded in the output)")
	o.config = fs.String("config", "", "the operator's configuration `file`, same keys as "+config.FileName+"; wins over it (recorded)")
	o.pf.Register(fs)
	return o
}

// ReadOperatorFile loads --config into the operator settings.
func ReadOperatorFile(op *config.Operator, name string) {
	if name == "" {
		return
	}
	data, err := os.ReadFile(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	op.File, op.FileName = data, filepath.Base(name)
}

// Map derives the map from a facts file.
func Map(args []string) {
	fs := NewFlagSet("map", args)
	branch, out, confirmations := mapFlags(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "map: exactly one facts file")
		os.Exit(2)
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "map:", err)
		os.Exit(1)
	}
	set := rules.Settings{DefaultBranch: *branch}
	if *confirmations != "" {
		if set.Confirmations, err = ReadConfirmations(*confirmations); err != nil {
			fmt.Fprintln(os.Stderr, "map:", err)
			os.Exit(1)
		}
	}
	m, err := MapFacts(raw, set)
	if err != nil {
		fmt.Fprintln(os.Stderr, "map:", err)
		os.Exit(1)
	}
	data, err := fact.Canonical(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
	Write(*out, data)
	fmt.Fprintf(os.Stderr, "%d stacks, %d links, %d third-party images, %d images built in CI, %d findings\n", len(m.Stacks), len(m.Links), len(m.Images), len(m.CIImages), len(m.Findings))
}

func mapFlags(fs *flag.FlagSet) (branch, out, confirmations *string) {
	branch = fs.String("default-branch", "main", "`branch` whose deploys are production (recorded in the output)")
	out = fs.String("out", "-", "output `file`, - for stdout")
	confirmations = fs.String("confirmations", "", "`file` with a JSON array of confirmations made outside the repo, recorded in the output")
	return
}

// ReadConfirmations reads a JSON array of confirmations, as a map's
// settings.confirmations holds them, so a map can be derived again.
func ReadConfirmations(path string) ([]rules.Confirmation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cs []rules.Confirmation
	if err := json.Unmarshal(data, &cs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cs, nil
}

// MCP serves one scan over stdio. Given a directory it scans the commit at
// HEAD first, with the repo's own hint file; given a facts file it serves
// that file.
func MCP(args []string) {
	fs := NewFlagSet("mcp", args)
	branch := mcpFlags(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "mcp: exactly one facts file or directory")
		os.Exit(2)
	}
	src, err := LoadSource(fs.Arg(0), *branch)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcp.NewServer(src, pipeline.Version).Run(ctx, &sdk.StdioTransport{}); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		os.Exit(1)
	}
}

func mcpFlags(fs *flag.FlagSet) *string {
	return fs.String("default-branch", "main", "`branch` whose deploys are production")
}

// LoadSource reads a facts file, or scans a directory's HEAD, and derives the
// map. Scan warnings go to stderr: stdout belongs to the protocol.
func LoadSource(path, branch string) (*mcp.Source, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if st.IsDir() {
		s, warnings, err := pipeline.Scan(path, pipeline.ScanOptions{Plugins: plugin.All()})
		if err != nil {
			return nil, err
		}
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
		if raw, err = fact.Canonical(s); err != nil {
			return nil, err
		}
		raw = append(raw, '\n') // as scan writes it, so the map names the same sha256
	} else if raw, err = os.ReadFile(path); err != nil {
		return nil, err
	}
	var s fact.Scan
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	m, err := MapFacts(raw, rules.Settings{DefaultBranch: branch})
	if err != nil {
		return nil, err
	}
	return &mcp.Source{Scan: &s, Map: m}, nil
}

// Write writes data plus a newline to a file, or to stdout for "-".
func Write(out string, data []byte) {
	data = append(data, '\n')
	var err error
	if out == "-" {
		_, err = os.Stdout.Write(data)
	} else {
		err = os.WriteFile(out, data, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
}
