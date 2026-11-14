package main

import (
	"flag"
	"testing"

	"github.com/cernly/cernly/plugin"
	"github.com/cernly/cernly/plugin/plugintest"
	_ "github.com/cernly/cernly/plugins/all"
)

var update = flag.Bool("update", false, "rewrite golden files")

// The plugin built for wasip1, signed, and run through the scanner's runtime
// on the example repo; with the built-in plugins, so merges with env,
// outbound and migrations-sql are in the golden too.
func TestGolden(t *testing.T) {
	java := plugintest.Load(t, ".", plugintest.Manifest(t, "manifest.json"))
	ps := append(plugin.All(), java)
	plugintest.Golden(t, "testdata/shop", "testdata/shop.golden.json", *update, ps...)
}
