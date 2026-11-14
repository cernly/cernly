// Command cernly is the open scanner: scan one commit, derive its map, serve
// both to AI agents over MCP. See package cli.
package main

import (
	"fmt"
	"os"

	"github.com/cernly/cernly/cli"
)

func main() {
	if cli.IsHelp(os.Args[1:]) {
		fmt.Print(cli.Usage("cernly"))
		return
	}
	if cli.Main(os.Args[1:]) {
		return
	}
	fmt.Fprint(os.Stderr, cli.Usage("cernly"))
	os.Exit(2)
}
