package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cernly/cernly/mcp"
	"github.com/cernly/cernly/rules"
)

// The MCP server over a fixture scan, through a real client session: every
// tool is read-only, answers match the facts file, and evidence comes along.
func TestMCP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "facts.json")
	if err := os.WriteFile(path, scanDir(t, "fixture-caddy"), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := LoadSource(path, "main")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, ct := sdk.NewInMemoryTransports()
	ss, err := mcp.NewServer(src, "test").Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		if tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
			t.Errorf("tool %s is not marked read-only", tl.Name)
		}
	}
	if got := strings.Join(names, " "); got != "find_facts get_fact get_map summary" {
		t.Errorf("tools = %s", got)
	}

	call := func(name string, args map[string]any, out any) *sdk.CallToolResult {
		t.Helper()
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out != nil && !res.IsError {
			raw, _ := json.Marshal(res.StructuredContent)
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		return res
	}

	var sum mcp.Summary
	call("summary", nil, &sum)
	if sum.Repo != "local/fixture-caddy" || sum.Facts != len(src.Scan.Facts) || len(sum.Kinds) == 0 {
		t.Errorf("summary = %+v", sum)
	}

	var found mcp.FindOutput
	call("find_facts", map[string]any{"kind": "route", "limit": 2}, &found)
	if found.Total == 0 || len(found.Facts) > 2 {
		t.Fatalf("find_facts route: total %d, %d returned", found.Total, len(found.Facts))
	}
	f := found.Facts[0]
	if len(f.Evidence) == 0 || f.Evidence[0].Path == "" || f.Evidence[0].Line == 0 {
		t.Errorf("fact without evidence: %+v", f)
	}

	var got mcp.GetOutput
	call("get_fact", map[string]any{"kind": f.Kind, "key": f.Key}, &got)
	if got.Fact.Key != f.Key {
		t.Errorf("get_fact = %+v", got.Fact)
	}
	if res := call("get_fact", map[string]any{"kind": "route", "key": "nope"}, nil); !res.IsError {
		t.Error("get_fact of a missing key is not an error")
	}

	var none mcp.FindOutput
	call("find_facts", map[string]any{"kind": "routes"}, &none)
	if none.Total != 0 || len(none.KindsInScan) == 0 {
		t.Errorf("a wrong kind gives %+v; want no facts and the kinds in the scan", none)
	}

	var sec mcp.MapOutput
	call("get_map", map[string]any{"section": "stacks"}, &sec)
	if sec.Total != len(src.Map.Stacks) {
		t.Errorf("get_map stacks: %d, want %d", sec.Total, len(src.Map.Stacks))
	}
	if sum.Total == nil || sum.Total.Routes != len(src.Map.Routes) {
		t.Errorf("summary metrics: %+v", sum.Total)
	}
	call("get_map", map[string]any{"section": "metrics"}, &sec)
	if sec.Total != 1 {
		t.Errorf("get_map metrics: %d items, want the one metrics object", sec.Total)
	}
	// Every section of the map is served under its map name.
	meta := map[string]bool{"schema": true, "scan": true, "rules_version": true, "catalogue_version": true, "settings": true}
	mt := reflect.TypeOf(rules.Map{})
	for i := 0; i < mt.NumField(); i++ {
		name, _, _ := strings.Cut(mt.Field(i).Tag.Get("json"), ",")
		if meta[name] {
			continue
		}
		if res := call("get_map", map[string]any{"section": name}, nil); res.IsError {
			t.Errorf("map section %q is not served by get_map", name)
		}
	}
	if res := call("get_map", map[string]any{"section": "nope"}, nil); !res.IsError {
		t.Error("an unknown map section is not an error")
	}
}

// The map's endpoints are a get_map section, and find_facts serves the endpoint
// facts.
func TestMCPEndpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "facts.json")
	if err := os.WriteFile(path, scanDir(t, "fixture-dotnet"), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := LoadSource(path, "main")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, ct := sdk.NewInMemoryTransports()
	ss, err := mcp.NewServer(src, "test").Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "get_map", Arguments: map[string]any{"section": "endpoints", "text": "/health"}})
	if err != nil || res.IsError {
		t.Fatalf("get_map endpoints: %v %v", err, res)
	}
	var sec mcp.MapOutput
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &sec); err != nil || sec.Total != 1 {
		t.Fatalf("get_map endpoints /health: %+v %v", sec, err)
	}
	res, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "find_facts", Arguments: map[string]any{"kind": "endpoint"}})
	if err != nil || res.IsError {
		t.Fatalf("find_facts endpoint: %v %v", err, res)
	}
	var found mcp.FindOutput
	raw, _ = json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &found); err != nil || found.Total != len(src.Map.Endpoints) || found.Total == 0 {
		t.Fatalf("find_facts endpoint: total %d, map has %d", found.Total, len(src.Map.Endpoints))
	}
}
