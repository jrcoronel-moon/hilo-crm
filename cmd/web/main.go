// Command web serves the human side of Hilo (sign-in, consent, board,
// dashboard, settings), the OAuth 2.1 authorization server and the public
// lead-form endpoint.
package main

import (
	"net/http"

	"github.com/jrcoronel-moon/hilo-crm/internal/app"
	"github.com/jrcoronel-moon/hilo-crm/internal/web"
)

// Set at build time with -ldflags.
var (
	gitSHA    = "dev"
	buildTime = ""
)

func main() {
	app.Run(app.Build{Service: "web", Commit: gitSHA, Time: buildTime}, func(env app.Env) (http.Handler, []string, error) {
		mux, err := web.Mount(env.Ctx, env.Cfg, env.DB, env.Log)
		return mux, append(env.Cfg.Providers(), "oauth 2.1", "lead forms"), err
	})
}
