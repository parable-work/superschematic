package gcp

import (
	"crypto/sha256"
	"encoding/hex"
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

// Derived names (D97). A name the target makes from a service's, a
// class's or the stack's name can be longer than the resource it names
// takes: a job's deployable, named after its API and its class, is the id
// of its service account, which holds 30 characters. Such a name is
// shortened rather than refused, to a prefix of it and a hash of the
// whole, so one name always shortens alike and two names shorten apart.
// A name that fits is never shortened, so what a deploy made before
// keeps its name. A shortened resource that takes labels keeps its
// deployable's whole name in nameLabel.
const (
	// nameHashDigits is how many hex digits of the whole name's SHA-256
	// end a shortened one.
	nameHashDigits = 7

	// parameterRoom is how many characters a shortened name leaves for
	// the parameters' values in a member of a parameterized environment:
	// a pull request's number, for a long while.
	parameterRoom = 6

	// accountIDMax is how many characters a service account's id holds,
	// the fewest of the names a workload's deployable makes.
	accountIDMax = 30

	// nameLabel is the label that holds a shortened resource's
	// deployable, and labelMax how many characters a label's value holds.
	nameLabel = "superschematic-deployable"
	labelMax  = 63
)

// shorten returns name when it holds at most max characters, else its
// first characters, less a hyphen or underscore they end with, then sep
// and the first nameHashDigits hex digits of the SHA-256 of the whole
// name, within max: finances-api-sync-all-plaid-items in 30 characters is
// finances-api-sync-all, a hyphen and the hash. The prefix keeps the
// name's first character, a letter, and the hash ends the name with a
// letter or a digit, as every name it shortens must.
func shorten(name string, max int, sep string) string {
	if len(name) <= max {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	prefix := strings.TrimRight(name[:max-len(sep)-nameHashDigits], "-_")
	return prefix + sep + hex.EncodeToString(sum[:])[:nameHashDigits]
}

// fit returns part, a literal piece of name, shortened so name fits in
// max characters: under a parameter, with parameterRoom characters left
// for the values. A name that fits, as checkLength says, keeps its part,
// and so does one that would keep too little of it, which checkLength
// then refuses. The second result says whether part was shortened.
func fit(name any, part, sep string, max int) (string, bool) {
	n, refs := literalLength(name)
	if !refs && n <= max || refs && n < max {
		return part, false
	}
	keep := max - (n - len(part))
	if refs {
		keep -= parameterRoom
	}
	if keep < len(sep)+nameHashDigits+1 {
		return part, false
	}
	return shorten(part, keep, sep), true
}

// fitted is suffixed's name for base, with base fit to max characters.
func fitted(env registry.StackEnvironment, base, sep string, max int) (any, bool) {
	part, short := fit(suffixed(env, base, sep), base, sep, max)
	return suffixed(env, part, sep), short
}

// labelValue is name as a label's value: each character a label does not
// take made a hyphen, shortened to labelMax.
func labelValue(name string) string {
	b := []byte(name)
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			b[i] = '-'
		}
	}
	return shorten(string(b), labelMax, "-")
}

// nameLabels are the labels of a resource named after deployable d: its
// whole name in nameLabel when the resource's name was shortened, and
// none when it fits, so such a resource is as a deploy made it before.
func nameLabels(d ir.ResolvedDeployable, short bool) map[string]any {
	if !short {
		return nil
	}
	return map[string]any{nameLabel: labelValue(kebab(d.Name))}
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
// (ir.WorkerDeployableName, D53), a deriver's (ir.DeriverDeployableName,
// D74) and an app's (ir.AppDeployableName, D78), an engine's runner's its database's (ir.RunnerDeployableName,
// D58), every other deployable's its own.
func renameOf(d ir.ResolvedDeployable) string {
	switch d.Kind {
	case ir.DeployableJob:
		return "the job's class or its API"
	case ir.DeployableWorker:
		return "the worker's class or its API"
	case ir.DeployableDeriver:
		return "the deriver's class or its API"
	case ir.DeployableApp:
		return "the app's class or its API"
	case ir.DeployableRunner:
		return "the engine database the runner is named after"
	}
	return "the deployable"
}
