package servergen

// The runtime a Go entrypoint wires over an API's database (D190, D192,
// D187, D180; apigen's wiring.go). Every entrypoint that builds the API's
// Deps asks the implementation's NewWiring for the wiring's options, when
// the database has any (apigen.DepsWiring.Constructs), and builds the
// notifier, the dispatcher and the inbound path into Deps with the API
// package's Wire. A server establishes the callers of the API's routes
// with the API package's Authenticator, the API's own auth step with a
// grant's and a run's token before it, and serves the API package's
// Routes beside the API's router: the inbox, the relays' callbacks, the
// agent routes, the calendar and search. A worker of a queue the runtime
// handles runs the queue the API package builds for it, and calls no
// NewWorkers.

// planWiring gives each API the runtime its entrypoint wires: a server
// every API's whose database has one, a job and a worker only an API's
// that Deps holds the notifier of, or whose worker's queue the runtime
// handles. A behavior worker that builds no Deps wires none. The wiring is
// the same over a pool and over a SQLite file (#717): the API package's
// functions take the ORM the entrypoint opened.
func (s *Server) planWiring(in Input) {
	for i, a := range s.APIs {
		w := in.APIs[i].Output.Deps.Wiring
		// A wiring of the file processor's pass alone is the
		// implementation's to call (apigen.WithFiles).
		if !w.Wires() || a.Database == nil {
			continue
		}
		switch {
		case s.BehaviorWorker() && !s.EventWorker():
			continue
		case s.Worker != nil && s.Worker.Runtime != "":
		case s.Background() && !w.Builds():
			continue
		}
		a.Wiring = w
	}
}

// WiringVar is the variable of the API's wiring's options in run.
func (a *API) WiringVar() string { return a.Var + "Wiring" }

// AuthenticateVar is the variable of the auth step of the API's routes
// in run, the API package's Authenticator.
func (a *API) AuthenticateVar() string { return a.Var + "Authenticate" }

// WiredVar is the variable of the router of the routes the runtime serves
// beside the API's own, the API package's Routes.
func (a *API) WiredVar() string { return a.Var + "Wired" }

// UsesAuthenticate reports whether the server establishes the callers of
// the API's routes with the API package's Authenticator: a server's API
// whose database keeps grants or the agent dispatcher, or whose routes the
// runtime serves beside it.
func (a *API) UsesAuthenticate() bool {
	return a.Wiring != nil && (a.Wiring.Authenticates() || a.Wiring.Serves())
}

// RouteAuthVar is the variable of the auth step the API's routes take,
// which a provider's key wraps (#744): the API package's Authenticator's,
// its AuthMiddleware's, or for an API with neither the key's alone, which
// its router uses.
func (a *API) RouteAuthVar() string {
	if a.UsesAuthenticate() {
		return a.AuthenticateVar()
	}
	return a.Var + "Auth"
}

// DeclaresRouteAuth reports whether the provider's key is the only auth
// step of the API's routes, which main.go declares (#744).
func (a *API) DeclaresRouteAuth() bool { return !a.Public && !a.UsesAuthenticate() }
