//go:build !js

package wasm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// engine runs a module with wazero: compiled once per plugin, instantiated
// once per run.
type engine struct {
	once     sync.Once
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	initErr  error
}

// cache holds compiled modules by hash for the life of the process, so a
// caller that scans many commits compiles each module once.
var cache = wazero.NewCompilationCache()

func (p *Plugin) load() error {
	p.once.Do(func() {
		ctx := context.Background()
		cfg := wazero.NewRuntimeConfig().
			WithCompilationCache(cache).
			WithMemoryLimitPages(p.limits.MemoryPages).
			WithCloseOnContextDone(true)
		p.rt = wazero.NewRuntimeWithConfig(ctx, cfg)
		if _, err := wasi_snapshot_preview1.Instantiate(ctx, p.rt); err != nil {
			p.initErr = err
			return
		}
		p.compiled, p.initErr = p.rt.CompileModule(ctx, p.module)
		if p.initErr == nil {
			if imp := p.foreignImports(); imp != "" {
				p.initErr = fmt.Errorf("imports %s; only wasi_snapshot_preview1 is provided", imp)
			}
		}
	})
	return p.initErr
}

// foreignImports names the first import outside WASI preview 1.
func (p *Plugin) foreignImports() string {
	for _, f := range p.compiled.ImportedFunctions() {
		mod, name, _ := f.Import()
		if mod != wasi_snapshot_preview1.ModuleName {
			return mod + "." + name
		}
	}
	return ""
}

// Close releases the compiled module.
func (p *Plugin) Close() error {
	if p.rt != nil {
		return p.rt.Close(context.Background())
	}
	return nil
}

func (p *Plugin) exec(stdin []byte) runOut {
	stdout := &capped{max: p.limits.Output}
	stderr := &capped{max: 4096}
	ctx, cancel := context.WithTimeout(context.Background(), p.limits.Timeout)
	defer cancel()
	// No WithFS, no WithEnv, no WithSysWalltime/Nanotime, no WithRandSource:
	// wazero's defaults are no files, no environment, a fake clock and a
	// fixed random source. web/viewer/wasi.js matches them.
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithArgs(p.m.Name).
		WithStdin(bytes.NewReader(stdin)).
		WithStdout(stdout).
		WithStderr(stderr)
	mod, err := p.rt.InstantiateModule(ctx, p.compiled, cfg)
	if mod != nil {
		_ = mod.Close(context.Background())
	}
	o := runOut{stdout: stdout.Bytes(), stderr: stderr.Bytes(), overOut: stdout.over}
	if err != nil {
		var exit *sys.ExitError
		switch {
		case ctx.Err() != nil:
			o.timedOut = true
		case errors.As(err, &exit):
			o.exit = int(exit.ExitCode())
		default:
			o.err = err
		}
	}
	return o
}
