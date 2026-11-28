package servergen

import (
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
)

// The runtime pieces a TypeScript entrypoint wires itself (#702; D159, D153,
// D157, D152, D155, D156, D158), which the API package's providers.ts and
// runtime.ts build (tsrestgen/runtime.go). Every entrypoint of the API, a
// server's main.ts and fetch.ts, a job's and a worker's main.ts and
// drain.ts, builds Deps the same way (ts-deps): the providers its config
// names, then Deps but its runtime pieces as DepsBase, the
// implementation's runtime options and agent options over it, and Deps
// with withRuntime. A server mounts the pieces' routes beside the router
// (runtimeRoutes), serves their MCP tools on the API's endpoint, wraps the
// implementation's authenticator in grantAuthenticator and gives the
// router the access log (ts-runtime-routes, RouterOptions). A worker of a
// piece's queue hands each message to the piece's handler.

// planRuntime sets what api wires of o's runtime pieces. background is a
// job's, a worker's or a deriver's entrypoint, which mounts no routes.
func (api *TypeScriptAPI) planRuntime(o *tsrestgen.APIOutput, stem string, taken names, background bool) {
	api.Providers = o.HasProviders() && api.Config != ""
	if o.HasAgents() {
		api.Agents = true
		api.AgentOptions = taken.take(stem + "AgentOptions")
	}
	if o.HasRuntime() {
		api.Runtime = true
		api.DepsBase = taken.take(stem + "DepsBase")
		api.RuntimeOptions = taken.take(stem + "RuntimeOptions")
		if !background {
			api.RuntimeRoutes = taken.take(stem + "Routes")
			api.RuntimeMCP = o.HasRuntimeMCP() && api.MCP
		}
	}
	if !background {
		api.Grants = o.Deps.Routes.Grants && api.Identity == ""
		api.AccessLog = o.Deps.Routes.AccessLog
	}
}

// planRuntimeHandler sets the handler of a worker of w, a @worker of o,
// whose queue a runtime piece drains.
func (w *TypeScriptWorker) planRuntimeHandler(o *tsrestgen.APIOutput, info tsrestgen.WorkerInfo) {
	switch info.Runtime {
	case tsrestgen.WorkerAgents:
		w.Handler = "dispatchAgentMessage"
		w.Transcripts = o.Deps.Agents != nil && info.Queue == o.Deps.Agents.Requests
	case tsrestgen.WorkerNotifier:
		w.Handler = "notifyMessage"
		w.Upkeep = true
	case tsrestgen.WorkerSearch:
		w.Handler = "indexSearchMessage"
		w.HandlerOptions = true
	}
}

// HandlerPiece names the runtime piece whose handler a worker's messages
// go to, for its main's comment.
func (w *TypeScriptWorker) HandlerPiece() string {
	switch w.Handler {
	case "dispatchAgentMessage":
		return "the agent dispatcher (D183)"
	case "notifyMessage":
		return "the notifier (D153)"
	case "indexSearchMessage":
		return "search's indexing (D155)"
	}
	return ""
}

// RouteAuthenticator is the expression a server's runtime routes
// authenticate their caller with, from the request: the router's
// authenticator, the identity service's, or none, which answers 401 to a
// route that needs a caller.
func (a *TypeScriptAPI) RouteAuthenticator() string {
	switch {
	case a.Authenticate != "":
		return "requestAuthenticator(" + a.Authenticate + ")"
	case a.Identity != "":
		return "requestAuthenticator(identityAuthenticator(" + a.Identity + "))"
	}
	return "async () => null"
}

// AuthenticatorFactory is the expression a server builds the router's
// authenticator with: the implementation's authenticate over Deps, inside
// grantAuthenticator for a database with grants (D156), which hands a run's
// token on to it (D194).
func (a *TypeScriptAPI) AuthenticatorFactory() string {
	if a.Grants {
		return "async () => grantAuthenticator({ orm: " + a.Deps + ".orm, inner: await " + a.Impl + ".authenticate(" + a.Deps + "), runTokens: 'inner' })"
	}
	return "() => " + a.Impl + ".authenticate(" + a.Deps + ")"
}

// runtimeRouterOptions are the router options of the runtime pieces: the
// access log (D156).
func (a *TypeScriptAPI) runtimeRouterOptions() []string {
	if a.AccessLog {
		return []string{"accessLog: accessLogOf(" + a.Deps + ".orm)"}
	}
	return nil
}

// runtimeMCPOptions are the MCP endpoint's options of the runtime pieces:
// their tools, served beside the routes' (D152, D155).
func (a *TypeScriptAPI) runtimeMCPOptions() []string {
	if a.RuntimeMCP {
		return []string{"tools: " + a.Module + ".runtimeMcpTools(" + a.Deps + ", " + a.RuntimeOptions + ")"}
	}
	return nil
}

// AccessEntry is the entry of the HTTP runtime an entrypoint imports its
// APIs' grants and access log from: ./postgres beside a pool, and ./d1,
// which imports no driver, for a server whose databases list sqlite first
// (#702).
func (s *TypeScriptServer) AccessEntry() string {
	if len(s.PostgresDatabases()) > 0 {
		return "/postgres"
	}
	return "/d1"
}

// AccessImports are the names an entrypoint imports from the HTTP
// runtime's AccessEntry for its APIs' grants and access log (D156).
func (s *TypeScriptServer) AccessImports() []string {
	var grants, log bool
	for _, a := range s.APIs {
		grants = grants || (a.Grants && a.Authenticate != "")
		log = log || a.AccessLog
	}
	var out []string
	if log {
		out = append(out, "accessLogOf")
	}
	if grants {
		out = append(out, "grantAuthenticator")
	}
	return out
}

// routesAuthenticate reports whether an API's runtime routes authenticate
// through requestAuthenticator, which the entrypoint imports, and
// identityAuthenticator, for one whose identity service establishes its
// callers.
func (s *TypeScriptServer) routesAuthenticate() (request, identity bool) {
	for _, a := range s.APIs {
		if a.RuntimeRoutes == "" {
			continue
		}
		request = request || a.Authenticate != "" || a.Identity != ""
		identity = identity || (a.Authenticate == "" && a.Identity != "")
	}
	return request, identity
}
