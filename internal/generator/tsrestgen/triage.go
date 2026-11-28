package tsrestgen

import (
	"fmt"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	ir "github.com/parable-work/superschematic/ir"
)

// TriageRoute is a triage operation (#746, D157) as the router mounts it:
// an operation of the inbound path's triage, which the loader added for a
// @triageOperations set, run by the runtime's triageOperation over
// RouterOptions.inbound, Deps.inbound, as the request's caller.
type TriageRoute struct {
	// Set is the @triageOperations set that serves the operation.
	Set string
	// Database, Messages, Party and Operation are the loader's marker: the
	// API's database, its messages table, the party table a settle
	// settles onto and the operation.
	Database  string
	Messages  string
	Party     string
	Operation string
}

// Literal is the operation as router.ts's table of them holds it, a
// TriageOperation: { messages: 'Message', party: 'Contact', operation: 'triage' }.
func (r *TriageRoute) Literal() string {
	return "{ messages: " + tsString(r.Messages) + ", party: " + tsString(r.Party) + ", operation: " + tsString(r.Operation) + " }"
}

// TriageRoutes are the triage operations the router mounts, in the order
// of Endpoints.
func (o *APIOutput) TriageRoutes() []*TriageRoute {
	var out []*TriageRoute
	for _, endpoint := range o.Endpoints {
		if endpoint.Triage != nil {
			out = append(out, endpoint.Triage)
		}
	}
	return out
}

// HasTriage reports whether the API serves the inbound path's triage
// (#746), which the router runs over RouterOptions.inbound.
func (o *APIOutput) HasTriage() bool {
	return len(o.TriageRoutes()) > 0
}

// checkTriageDeps refuses deps for an API that serves the inbound path's
// triage when they hold no inbound path, which the router runs it
// through: a database whose conversations, notifier and run requests the
// generated runtime builds it from (#702).
func (o *APIOutput) checkTriageDeps(deps DepsInfo) error {
	routes := o.TriageRoutes()
	if len(routes) == 0 || deps.Inbound != nil {
		return nil
	}
	return fmt.Errorf("tsrestgen: %s serves the inbound path's triage (@triageOperations on %s), which runs through Deps.inbound, and its Deps holds no inbound path: %s declares the @conversations tables, and the inbound path needs its notifier and its @runRequests queue too (D157)", o.SchemaName, routes[0].Set, routes[0].Database)
}

// triageEndpoint is a triage operation (#746) as the router mounts it: its
// route, its parameters, its input and its result, decoded as an
// implemented endpoint's, and the marker it runs. Its types are rendered
// by a builder of their own, so they add no import to interfaces.ts, which
// has no method for it, and none but its input's validators to router.ts.
// The loader fills the marker; IR that did not go through it is refused
// here, naming the operation.
func (b *builder) triageEndpoint(ep apigen.EndpointInfo, op *ir.TriageOperation) (EndpointInfo, error) {
	own := newBuilder(b.schema, b.opts)
	endpoint, err := own.endpoint(ep)
	if err != nil {
		return endpoint, err
	}
	owner := behaviorSet(b.schema, ep.Operation)
	if !slices.Contains(ir.TriageOperationNames, op.Operation) || op.Messages == "" || op.Party == "" || op.Database == "" {
		return endpoint, fmt.Errorf("tsrestgen: %s.%s is a triage operation (#746) whose marker is not whole (operation %q, messages %q, party %q, database %q); the loader fills it, so load the schema before generating", owner, ep.Operation.Name, op.Operation, op.Messages, op.Party, op.Database)
	}
	if op.Operation != ir.TriageOpList && !slices.ContainsFunc(endpoint.PathParams, func(p ParamInfo) bool { return p.Name == ir.TriageMessageParam }) {
		return endpoint, fmt.Errorf("tsrestgen: triage operation %s.%s is routed at %s %s, which has no path parameter %s, the held message", owner, ep.Operation.Name, endpoint.Method, endpoint.Path, ir.TriageMessageParam)
	}
	endpoint.Triage = &TriageRoute{
		Set:       owner,
		Database:  op.Database,
		Messages:  op.Messages,
		Party:     op.Party,
		Operation: op.Operation,
	}
	for pkg, symbols := range own.validatorImports {
		for symbol := range symbols {
			b.addImport(b.validatorImports, pkg, symbol)
		}
	}
	return endpoint, nil
}
