// Command mcp serves the Model Context Protocol endpoint (/mcp) that AI agents
// use to work with the CRM. It only validates tokens; it never issues them.
package main

import (
	"net/http"

	"github.com/jrcoronel-moon/hilo-crm/internal/app"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/mcpserver"
)

// Set at build time with -ldflags.
var (
	gitSHA    = "dev"
	buildTime = ""
)

func main() {
	app.Run(app.Build{Service: "mcp", Commit: gitSHA, Time: buildTime}, func(env app.Env) (http.Handler, []string, error) {
		srv := mcpserver.New(env.Cfg, env.DB, crm.New(env.DB, env.Log), env.Log)
		mux := http.NewServeMux()
		mux.Handle("/mcp", srv.Handler())
		return mux, []string{"mcp " + mcpserver.Version, "oauth bearer", "pat"}, nil
	})
}
