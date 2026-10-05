package verify

import (
	ir "github.com/parable-work/superschematic/ir"
)

// checkMiddleware refuses a @rateLimit, @bodyLimit or @timeout below 1 on
// an operation set or an operation. The TypeScript reader refuses one when
// it reads the decorator; this catches a schema authored as IR (YAML or
// JSON). No server can apply such a limit: the Go runtime would refuse
// every request with 429 or 504, and the TypeScript and Rust routers would
// drop the directive.
func checkMiddleware(schema *ir.Schema, r *Result) {
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		checkMiddlewareValues(set.Name, set.Middleware, r)
		for _, op := range set.Operations {
			if op != nil {
				checkMiddlewareValues(set.Name+"."+op.Name, op.Middleware, r)
			}
		}
	}
}

func checkMiddlewareValues(owner string, mw *ir.MiddlewareConfig, r *Result) {
	if mw == nil {
		return
	}
	for _, v := range []struct {
		decorator, key string
		value          *int
	}{
		{"rateLimit", "requestsPerMinute", mw.RateLimit},
		{"bodyLimit", "megabytes", mw.BodyLimit},
		{"timeout", "seconds", mw.Timeout},
	} {
		if v.value != nil && *v.value < 1 {
			r.errorf("", "%s: @%s %s must be at least 1, not %d", owner, v.decorator, v.key, *v.value)
		}
	}
}
