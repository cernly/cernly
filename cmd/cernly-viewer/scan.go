//go:build js && wasm

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"syscall/js"

	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/internal/jsio"
	"github.com/cernly/cernly/localview"
	"github.com/cernly/cernly/pipeline"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/plugin/wasm"
	_ "github.com/cernly/cernly/plugins/all" // the built-in plugins, as the CLI has them
	"github.com/cernly/cernly/repo"
)

// MaxFiles is the most tracked files a browser scan reads: past it the scan
// stops with the number instead of running the tab out of memory. Measured on
// 15 Nov 2026: a checkout of 32 000 files (kubernetes) took 93 s and 2.3 GB in
// this module, against the 4 GB a WebAssembly module can have. The CLI has no
// such limit.
const MaxFiles = 40_000

// maxFiles is MaxFiles, lowered in tests.
var maxFiles = MaxFiles

// The label of a scan of a folder without git.
const (
	NoCommit   = "0000000000000000000000000000000000000000"
	NoCommitAt = "1970-01-01T00:00:00Z"
)

// Scanned is one browser scan: the facts file, byte for byte what
// `cernly scan` writes for the same checkout, the warnings it prints, and
// the scan's view. A scan the page cannot show (past localview.MaxBytes, or
// a view that fails) keeps its facts, with ViewError instead of View, so
// the reader can still save them.
type Scanned struct {
	Facts     string          `json:"facts"`
	Repo      string          `json:"repo"`
	Warnings  []string        `json:"warnings"`
	View      json.RawMessage `json:"view,omitempty"`
	ViewError string          `json:"view_error,omitempty"`
	Git       bool            `json:"git"`
	// Changed is the tracked files that differ from the index, after a dirty
	// scan (at most MaxChanged): the browser cannot tell a file from a
	// symbolic link, so the reader checks these are their own edits.
	Changed []string `json:"changed,omitempty"`
}

// MaxChanged is the most changed files a dirty scan lists.
const MaxChanged = 20

// Bundled is a signed plugin the page carries: its bundle (manifest and
// signature) as JSON and its module.
type Bundled struct {
	Bundle []byte
	Module []byte
}

// scanFolder scans the folder behind src (see jsio.FS) with the built-in
// plugins and the bundled ones that verify against Cernly's keys plus
// trustPEM (the test key in test builds, recorded as such in the scan).
func scanFolder(src js.Value, name string, bundled []Bundled, trustPEM [][]byte) (*Scanned, error) {
	fsys := jsio.NewFS(src)
	r, err := repo.OpenFSMax(fsys, name, maxFiles)
	var many *repo.TooManyFilesError
	if errors.As(err, &many) {
		return nil, fmt.Errorf("%d files to read, more than the %d a browser scan reads; scan this repository with the cernly CLI", many.Files, many.Max)
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	_, gitErr := fsys.Stat(".git")
	ps, err := plugins(bundled, trustPEM)
	if err != nil {
		return nil, err
	}
	o := pipeline.ScanOptions{Plugins: ps}
	if gitErr != nil {
		// A folder without .git has no commit: say so with the zero id and
		// date, as `cernly scan --commit 000... --committed-at 1970-01-01T00:00:00Z`.
		o.Commit, o.CommittedAt = NoCommit, NoCommitAt
	}
	s, warnings, err := pipeline.ScanOpenedFS(r, name, o)
	if err != nil {
		return nil, err
	}
	out := &Scanned{Repo: s.Repo, Git: gitErr == nil}
	if g := r.GitFS(); g != nil {
		for _, u := range g.Unread() {
			warnings = append(warnings, fmt.Sprintf("git setting %s is outside this folder and was not read; the repo id may differ from the CLI's", u))
		}
		if s.Commit.Dirty {
			if out.Changed, err = g.Changed(fsys, MaxChanged); err != nil {
				return nil, err
			}
		}
	}
	if strings.HasPrefix(s.Repo, "local/") {
		warnings = append(warnings, fmt.Sprintf("repo id %s: no origin remote, so the scan is named after the folder", s.Repo))
	}
	data, err := fact.Canonical(s)
	if err != nil {
		return nil, err
	}
	facts := append(data, '\n')
	if warnings == nil {
		warnings = []string{}
	}
	out.Facts, out.Warnings = string(facts), warnings
	if out.View, err = localview.Of(facts); err != nil {
		out.View, out.ViewError = nil, err.Error()
	}
	return out, nil
}

func plugins(bundled []Bundled, trustPEM [][]byte) ([]plugin.Plugin, error) {
	ps := plugin.All()
	if len(bundled) == 0 {
		return ps, nil
	}
	trust, err := wasm.NewTrust(trustPEM...)
	if err != nil {
		return nil, err
	}
	for _, b := range bundled {
		var bundle wasm.Bundle
		if err := json.Unmarshal(b.Bundle, &bundle); err != nil {
			return nil, fmt.Errorf("bundled plugin: %w", err)
		}
		p, err := wasm.New(bundle, b.Module, trust, wasm.Limits{})
		if err != nil {
			return nil, err
		}
		for _, q := range ps {
			if q.Name() == p.Name() {
				return nil, fmt.Errorf("plugin %s: the name is taken", p.Name())
			}
		}
		ps = append(ps, p)
	}
	// Sorted by name, as cli.LoadPlugins: the run order is the CLI's.
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name() < ps[j].Name() })
	return ps, nil
}

// promise runs work on a goroutine (it may wait on the page's promises) and
// settles a JavaScript promise with its JSON, or rejects with its error.
func promise(work func() (any, error)) js.Value {
	executor := js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			out, err := work()
			if err == nil {
				var b []byte
				if b, err = json.Marshal(out); err == nil {
					resolve.Invoke(string(b))
					return
				}
			}
			reject.Invoke(js.Global().Get("Error").New(err.Error()))
		}()
		return nil
	})
	p := js.Global().Get("Promise").New(executor)
	executor.Release()
	return p
}

// scanArgs reads scan(source, name, [{bundle, module}], [trustPEM]).
func scanArgs(args []js.Value) (js.Value, string, []Bundled, [][]byte, error) {
	if len(args) < 2 || args[1].Type() != js.TypeString {
		return js.Undefined(), "", nil, nil, errors.New("scan(source, name, plugins, trust)")
	}
	var bundled []Bundled
	if len(args) > 2 && args[2].Truthy() {
		for i := 0; i < args[2].Length(); i++ {
			e := args[2].Index(i)
			bundled = append(bundled, Bundled{Bundle: []byte(e.Get("bundle").String()), Module: jsio.Bytes(e.Get("module"))})
		}
	}
	var trust [][]byte
	if len(args) > 3 && args[3].Truthy() {
		for i := 0; i < args[3].Length(); i++ {
			trust = append(trust, []byte(args[3].Index(i).String()))
		}
	}
	return args[0], args[1].String(), bundled, trust, nil
}
