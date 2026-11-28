package requestctx

import (
	"context"
	"errors"
)

// A route's check (D98): the MCP endpoint of a generated API sends the
// request a call of a tool that asks would make through the tool's route
// before it asks the person (runtime/http/go/mcp), so a call its route
// would refuse answers the route's refusal at once and mints no
// confirmation. The request runs every step of the route and its decoding,
// and stops where the handler would call the implementation: there
// CheckContext answers ErrRouteReached, which the generated handler's
// RespondAppError answers 204 with no body. The rate limit, which counts
// the runs alone, and the idempotency step, whose key is the run's, let a
// check through untouched.

// ErrRouteReached is CheckContext's answer to a route's check that reached
// the implementation.
var ErrRouteReached = errors.New("the route's check reached its implementation")

type routeCheckKey struct{}

// WithRouteCheck is ctx marking its request as a route's check, whose
// reached flag CheckContext sets when the request reaches the
// implementation.
func WithRouteCheck(ctx context.Context, reached *bool) context.Context {
	return context.WithValue(ctx, routeCheckKey{}, reached)
}

// IsRouteCheck reports whether ctx's request is a route's check.
func IsRouteCheck(ctx context.Context) bool {
	reached, _ := ctx.Value(routeCheckKey{}).(*bool)
	return reached != nil
}

// reachRoute marks ctx's check reached; false for a request that is no
// check.
func reachRoute(ctx context.Context) bool {
	reached, _ := ctx.Value(routeCheckKey{}).(*bool)
	if reached == nil {
		return false
	}
	*reached = true
	return true
}
