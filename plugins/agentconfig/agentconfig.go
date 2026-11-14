// Package agentconfig reports the AI coding tools a repo is set up for, from
// the configuration files they leave in it (.cursor/, AGENTS.md, CLAUDE.md,
// ...). Repo-level proof that a tool works in this repo; it does not say
// which commit a tool wrote. The table is in the shipped defaults.
package agentconfig

import (
	"sort"

	"github.com/cernly/cernly/config"
	"github.com/cernly/cernly/defaults"
	"github.com/cernly/cernly/fact"
	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/repo"
)

type Plugin struct{}

func init() { plugin.Register(Plugin{}) }

func (Plugin) Name() string    { return "agent-config" }
func (Plugin) Version() string { return "0.1.0" }

func (Plugin) Watches(p string) bool { return tool(p) != nil }

func tool(p string) *defaults.AgentConfig {
	cfgs := defaults.Get().AgentConfigs
	for i := range cfgs {
		for _, pat := range cfgs[i].Paths {
			if config.Matches(pat, p) {
				return &cfgs[i]
			}
		}
	}
	return nil
}

// Run emits one agent_config fact per tool, every matching file as evidence.
// A file is evidence as a whole, so it points at its first line.
func (Plugin) Run(r *repo.Repo) plugin.Result {
	var res plugin.Result
	byTool := map[string][]string{}
	names := map[string]string{}
	for _, f := range r.Files {
		if t := tool(f); t != nil {
			byTool[t.Tool] = append(byTool[t.Tool], f)
			names[t.Tool] = t.Name
		}
	}
	tools := make([]string, 0, len(byTool))
	for t := range byTool {
		tools = append(tools, t)
	}
	sort.Strings(tools)
	for _, t := range tools {
		files := byTool[t]
		f := fact.Fact{Kind: fact.KindAgentConfig, Key: "agent_config:" + t, Plugin: "agent-config",
			Attrs: map[string]any{"tool": t, "name": names[t], "files": len(files)}}
		for _, p := range files {
			f.Evidence = append(f.Evidence, fact.Evidence{Path: p, Line: 1})
		}
		res.Facts = append(res.Facts, f)
	}
	return res
}
