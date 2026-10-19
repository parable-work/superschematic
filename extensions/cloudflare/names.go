package cloudflare

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// How the target names what it makes for a deployable (section 18.2): after
// the deployable, in kebab case, with each parameter's name and value
// appended under a parameterized environment (section 5.4), so each member
// of a preview gets its own Worker, D1 database and R2 bucket.

// MaxWorkerName is the longest name a Worker takes: one DNS label, since
// workers.dev serves it at `<name>.<subdomain>.workers.dev`.
const MaxWorkerName = 63

// suffixed names something of a member of a parameterized environment:
// the base, then for each parameter the separator, the parameter's name and
// its value, so `shop-api` is `shop-api-pr123` when pr is 123.
func suffixed(env registry.StackEnvironment, base, sep string) any {
	parts := []any{base}
	for _, param := range env.Parameters {
		parts = append(parts, sep, param, ir.Parameter(param))
	}
	return join(parts...)
}

// literalLength returns the number of characters a name holds outside its
// references, and whether it holds any.
func literalLength(name any) (n int, refs bool) {
	switch name := name.(type) {
	case string:
		return len(name), false
	case ir.Concat:
		for _, part := range name {
			if s, ok := part.(string); ok {
				n += len(s)
			} else {
				refs = true
			}
		}
		return n, refs
	}
	return 0, true
}

// checkWorkerName refuses, as unrealizable, a Worker name longer than
// MaxWorkerName, or under a parameter one that leaves no room for a
// character of the value. what names the Worker (`the Worker of server
// shop-api`), rename what names it anew (`the deployable`).
func checkWorkerName(what string, name any, rename string) error {
	n, refs := literalLength(name)
	switch {
	case !refs && n > MaxWorkerName:
		return registry.Unrealizable("%s would be named %q, %d characters, and a Worker's name is at most %d, one DNS label under workers.dev; give %s a shorter name", what, name, n, MaxWorkerName, rename)
	case refs && n >= MaxWorkerName:
		return registry.Unrealizable("%s would be named with %d characters before its parameter values, and a Worker's name is at most %d, one DNS label under workers.dev; give %s a shorter name", what, n, MaxWorkerName, rename)
	}
	return nil
}
