package gcp

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// join joins strings and references into one value: a string when every
// part is a string, else an ir.Concat with adjacent strings merged.
func join(parts ...any) any {
	var out ir.Concat
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, buf.String())
			buf.Reset()
		}
	}
	var add func(any)
	add = func(part any) {
		switch part := part.(type) {
		case string:
			buf.WriteString(part)
		case ir.Concat:
			for _, inner := range part {
				add(inner)
			}
		default:
			flush()
			out = append(out, part)
		}
	}
	for _, part := range parts {
		add(part)
	}
	flush()
	switch {
	case len(out) == 0:
		return ""
	case len(out) == 1:
		if s, ok := out[0].(string); ok {
			return s
		}
	}
	return out
}

// suffixed names something of a member of a parameterized environment:
// the base, then for each parameter the separator, the parameter's name and
// its value, so `shop-api` is `shop-api-pr123` and `shop_db` is
// `shop_db_pr123` when pr is 123 (section 5.4).
func suffixed(env registry.StackEnvironment, base, sep string) any {
	parts := []any{base}
	for _, param := range env.Parameters {
		parts = append(parts, sep, param, ir.Parameter(param))
	}
	return join(parts...)
}

// kebab lowercases a name and joins its words with hyphens: Orders is
// orders, ShopAPIServer is shop-api-server, shop-api stays shop-api.
func kebab(name string) string {
	upper := func(i int) bool { return i >= 0 && i < len(name) && name[i] >= 'A' && name[i] <= 'Z' }
	lower := func(i int) bool {
		return i >= 0 && i < len(name) && (name[i] >= 'a' && name[i] <= 'z' || name[i] >= '0' && name[i] <= '9')
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case upper(i):
			if lower(i-1) || upper(i-1) && lower(i+1) {
				b.WriteByte('-')
			}
			b.WriteByte(c - 'A' + 'a')
		case c == '_':
			b.WriteByte('-')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// snake is kebab with underscores, for a Postgres database's name.
func snake(name string) string { return strings.ReplaceAll(kebab(name), "-", "_") }

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

// checkLength refuses a name a GCP resource cannot take: shorter than min
// or longer than max characters, or under a parameter longer than leaves
// room for one character of the value. rename is what names it anew, which
// the refusal tells the engineer to change: `the deployable`, or for a
// job, whose name its API and class make (D52), `the job's class or its
// API`.
func checkLength(what string, name any, min, max int, rename string) error {
	n, refs := literalLength(name)
	switch {
	case !refs && n < min:
		return fmt.Errorf("%s %q is %d characters, and GCP needs at least %d; give %s a longer name", what, name, n, min, rename)
	case !refs && n > max:
		return fmt.Errorf("%s %q is %d characters, and GCP allows %d; give %s a shorter name", what, name, n, max, rename)
	case refs && n >= max:
		return fmt.Errorf("%s is %d characters before its parameter values, and GCP allows %d in all; give %s a shorter name", what, n, max, rename)
	}
	return nil
}

// renameOf is what names a deployable anew, for checkLength: a job's name
// is its API's and its class's (ir.JobDeployableName), as a worker's is
// (ir.WorkerDeployableName, D53), every other deployable's its own.
func renameOf(d ir.ResolvedDeployable) string {
	switch d.Kind {
	case ir.DeployableJob:
		return "the job's class or its API"
	case ir.DeployableWorker:
		return "the worker's class or its API"
	}
	return "the deployable"
}
