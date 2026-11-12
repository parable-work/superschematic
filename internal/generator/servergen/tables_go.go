package servergen

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/apigen"
)

// checkTableCallers refuses the Go server of o, an API that serves table
// operations (D106), when one of them needs an end user (@auth or
// @requirePermission) and the server authenticates none: o is neither
// public nor over a user model, so its Config takes no auth middleware
// (apigen.APIOutput.AuthWired), and its protected routes run with no
// caller on the request. An authored route of such an API runs the
// project's implementation, which may check the caller itself; a table
// operation's handler is generated, and answers a request with no caller
// 401, so the server would serve the table to no one. A server the project
// writes may mount RegisterRoutes behind a middleware of its own. It is
// checkBehaviorCallers for the table operations.
func checkTableCallers(in Input, o *apigen.APIOutput) error {
	if o.AuthWired() {
		return nil
	}
	for _, ep := range o.ServedTableEndpoints() {
		to := ep.TableOperation()
		if !ep.RequiresAuth {
			continue
		}
		return fmt.Errorf("stack %s: server %s serves %s, whose route %s %s serves the %s of %s, a table of %s (D106), for the caller the operation requires, and the Go server authenticates no caller: %s is not public and its authDb has no User table, so its Config takes no auth middleware, and the generated handler would answer no one; make %s public, whose implementation's AuthMiddleware authenticates the caller, or set its authDb to a database with a User table (D50), or mount RegisterRoutes in a server of your own behind a middleware that puts the caller on the request", in.Stack, in.Server, o.SchemaName, ep.Method, ep.Path, to.Kind, to.Type, to.Database, o.SchemaName, o.SchemaName)
	}
	return nil
}
