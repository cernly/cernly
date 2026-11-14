// Command cernly-site builds the developer docs at cernly.dev into a folder:
//
//	cernly-site [--src site] [--schema schema] [--skills skills] [--out site/public]
//
// The defaults fit the open repo's root. See https://cernly.dev/how/open-core/.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/cernly/cernly/site"
)

func main() {
	src := flag.String("src", "site", "folder with layout.html, static/ and pages/")
	schema := flag.String("schema", "schema", "folder with the JSON Schemas, served at /schema/")
	skills := flag.String("skills", "skills", "folder with agent skills, served at /skills/")
	out := flag.String("out", "site/public", "output folder, emptied first")
	flag.Parse()
	if err := site.Build(site.Options{Src: *src, Schema: *schema, Skills: *skills, Out: *out}); err != nil {
		fmt.Fprintln(os.Stderr, "cernly-site:", err)
		os.Exit(1)
	}
}
