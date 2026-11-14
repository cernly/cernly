// Package all links every built-in plugin into the binary. Importing it for
// its side effects registers them; add a new built-in plugin here.
package all

import (
	_ "github.com/cernly/cernly/plugins/agentconfig"
	_ "github.com/cernly/cernly/plugins/caddy"
	_ "github.com/cernly/cernly/plugins/command"
	_ "github.com/cernly/cernly/plugins/compose"
	_ "github.com/cernly/cernly/plugins/deps"
	_ "github.com/cernly/cernly/plugins/dockerfile"
	_ "github.com/cernly/cernly/plugins/dotnet"
	_ "github.com/cernly/cernly/plugins/env"
	_ "github.com/cernly/cernly/plugins/kubernetes"
	_ "github.com/cernly/cernly/plugins/migrations"
	_ "github.com/cernly/cernly/plugins/nginx"
	_ "github.com/cernly/cernly/plugins/outbound"
	_ "github.com/cernly/cernly/plugins/ownership"
	_ "github.com/cernly/cernly/plugins/prisma"
	_ "github.com/cernly/cernly/plugins/size"
	_ "github.com/cernly/cernly/plugins/tests"
	_ "github.com/cernly/cernly/plugins/traefik"
)
