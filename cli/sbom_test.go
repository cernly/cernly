package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cernly/cernly/sbom"
)

// The bill of materials for the fixture, byte for byte: the same scan gives
// the same serial number, timestamp and order.
func TestSBOMGolden(t *testing.T) {
	facts := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(facts, scanDir(t, "fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := BuildSBOM(facts, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	golden := filepath.Join("testdata", "fixture.sbom.golden.json")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run go test -update once)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("sbom differs from %s; run go test -update and review the diff", golden)
	}

	var b sbom.BOM
	if err := json.Unmarshal(got, &b); err != nil || b.BOMFormat != "CycloneDX" || b.SpecVersion != "1.6" {
		t.Fatalf("bom: %v %+v", err, b.Metadata)
	}
	refs := map[string]bool{}
	for _, c := range b.Components {
		if refs[c.BOMRef] {
			t.Errorf("bom-ref %s twice", c.BOMRef)
		}
		refs[c.BOMRef] = true
	}
	for _, d := range b.Dependencies {
		for _, r := range append([]string{d.Ref}, d.DependsOn...) {
			if r != "repo" && !refs[r] {
				t.Errorf("dependency names %s, which is no component", r)
			}
		}
	}

	// One service: only what that service carries.
	one, err := BuildSBOM(facts, "main", "service:compose:prod:web")
	if err != nil {
		t.Fatal(err)
	}
	var ob sbom.BOM
	_ = json.Unmarshal(one, &ob)
	if len(ob.Components) >= len(b.Components) || ob.SerialNumber == b.SerialNumber {
		t.Errorf("one service: %d of %d components, serial %s", len(ob.Components), len(b.Components), ob.SerialNumber)
	}
	if _, err := BuildSBOM(facts, "main", "service:compose:prod:nope"); err == nil {
		t.Error("an unknown service must fail")
	}

	// One stack: its services and what they carry, with the stack's role.
	st, err := BuildSBOMScope(facts, "main", sbom.Scope{Stack: "stack:compose:prod"})
	if err != nil {
		t.Fatal(err)
	}
	var sb sbom.BOM
	_ = json.Unmarshal(st, &sb)
	var services, props []string
	for _, c := range sb.Components {
		if c.Type == "application" {
			services = append(services, c.BOMRef)
		}
	}
	for _, p := range sb.Metadata.Properties {
		if p.Name == "cernly:stack" || p.Name == "cernly:stack_role" {
			props = append(props, p.Value)
		}
	}
	for _, s := range services {
		if !strings.HasPrefix(s, "service:compose:prod:") {
			t.Errorf("stack BOM has %s", s)
		}
	}
	if len(services) == 0 || strings.Join(props, " ") != "stack:compose:prod none" || sb.SerialNumber == b.SerialNumber {
		t.Errorf("stack: services %v, properties %v", services, props)
	}
	if _, err := BuildSBOMScope(facts, "main", sbom.Scope{Stack: "stack:compose:nope"}); err == nil {
		t.Error("an unknown stack must fail")
	}
}

// A service built from a .NET project carries the transitive packages of that
// project's packages.lock.json: the lock facts' roots are the project folder,
// the unit the service's direct dependencies carry.
func TestSBOMServiceNugetLock(t *testing.T) {
	facts := filepath.Join(t.TempDir(), "fixture-dotnet.json")
	if err := os.WriteFile(facts, scanDir(t, "fixture-dotnet"), 0o644); err != nil {
		t.Fatal(err)
	}
	one, err := BuildSBOM(facts, "main", "service:compose:default:web")
	if err != nil {
		t.Fatal(err)
	}
	var b sbom.BOM
	if err := json.Unmarshal(one, &b); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range b.Components {
		names = append(names, c.Name)
	}
	got := strings.Join(names, " ")
	for _, want := range []string{"Serilog.Sinks.Console", "Microsoft.Data.SqlClient"} {
		if !strings.Contains(got, want) {
			t.Errorf("service BOM lacks the transitive %s: %s", want, got)
		}
	}
	// A Direct lock entry the project file does not list (an implicit SDK
	// reference) is a direct component of the service.
	found := false
	for _, c := range b.Components {
		if c.Name == "Microsoft.NET.ILLink.Tasks" {
			found = true
			for _, p := range c.Properties {
				if p.Name == "cernly:direct" && p.Value != "true" {
					t.Errorf("ILLink.Tasks not direct: %+v", c.Properties)
				}
			}
		}
	}
	if !found {
		t.Errorf("service BOM lacks the lock-only direct Microsoft.NET.ILLink.Tasks: %s", got)
	}
}
