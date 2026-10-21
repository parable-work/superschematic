package cloudflare

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// How the target names what it makes in an account (section 18.2): one
// account may hold several stacks, and several environments of each, so
// every name of an environment's Worker, D1 database, R2 bucket and bucket
// token holds the stack, the environment and what it is for, each in
// kebab case, joined by hyphens: `<stack>-<env>-<deployable>`
// (`calls-stack-staging-shop-pricing`). A member of a parameterized
// environment appends each parameter's name and value (section 5.4), so
// each member of a preview gets its own: `calls-stack-preview-shop-pricing-pr7`.
// A custom domain's host is not an account's name but one under the
// environment's domain, so it keeps the deployable's alone (hostLabel).

// MaxWorkerName is the longest name a Worker takes: one DNS label, since
// workers.dev serves it at `<name>.<subdomain>.workers.dev`.
const MaxWorkerName = 63

// AccountName is the name an environment's resource takes in its account
// before any parameter: the stack's name, the environment's and each of
// parts, each in kebab case, joined by hyphens, with nothing but lowercase
// letters, digits and single hyphens, which a Worker, a D1 database and an
// R2 bucket all take: AccountName("calls-stack", "Staging", "shop-pricing")
// is `calls-stack-staging-shop-pricing`.
func AccountName(stack, environment string, parts ...string) string {
	var words []string
	for _, word := range append([]string{stack, environment}, parts...) {
		if word = normal(word); word != "" {
			words = append(words, word)
		}
	}
	return strings.Join(words, "-")
}

// normal is a name in kebab case with nothing but lowercase letters,
// digits and single hyphens between them.
func normal(name string) string {
	var b strings.Builder
	hyphen := false
	for _, c := range kebab(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteRune(c)
		default:
			hyphen = true
		}
	}
	return b.String()
}

// accountName is AccountName for env, with each parameter's name and value
// appended under a parameterized environment.
func accountName(env registry.StackEnvironment, parts ...string) any {
	return suffixed(env, AccountName(env.Stack, env.Name, parts...), "-")
}

// hostLabel is the label of an exposed Worker's host under the
// environment's domain, `<label>.<domain>`: the deployable's name, with
// each parameter's name and value under a parameterized environment. The
// domain is the environment's, so the label needs neither the stack nor
// the environment, as gcp's host under a domain does not.
func hostLabel(env registry.StackEnvironment, deployable string) any {
	return suffixed(env, kebab(deployable), "-")
}

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

// namePart is one part a name joins, as a refusal names it: `the stack`
// and its text in the name.
type namePart struct{ what, text string }

// partsOf are the parts of an account's name for env: the stack, the
// environment, then what (`the deployable`) and each of its texts, and
// each parameter.
func partsOf(env registry.StackEnvironment, what string, texts ...string) []namePart {
	parts := []namePart{{"the stack", normal(env.Stack)}, {"the environment", normal(env.Name)}}
	for _, text := range texts {
		parts = append(parts, namePart{what, normal(text)})
	}
	for _, param := range env.Parameters {
		parts = append(parts, namePart{"parameter " + param, strings.ToLower(param)})
	}
	return parts
}

// shorten says which parts a name joins, each with its length, and which
// to shorten: the longest.
func shorten(parts []namePart) string {
	var said []string
	longest := 0
	for i, p := range parts {
		said = append(said, fmt.Sprintf("%s's %q (%d)", strings.TrimPrefix(p.what, "the "), p.text, len(p.text)))
		if len(p.text) > len(parts[longest].text) {
			longest = i
		}
	}
	return fmt.Sprintf("it joins %s with hyphens; shorten %s's name, the longest, or another of them", strings.Join(said, ", "), parts[longest].what)
}

// checkWorkerName refuses, as unrealizable, a Worker name longer than
// MaxWorkerName, or under a parameter one that leaves no room for a
// character of the value. what names the Worker (`the Worker of server
// shop-api`), parts what the name joins.
func checkWorkerName(what string, name any, parts []namePart) error {
	n, refs := literalLength(name)
	switch {
	case !refs && n > MaxWorkerName:
		return registry.Unrealizable("%s would be named %q, %d characters, and a Worker's name is at most %d, one DNS label under workers.dev: %s", what, name, n, MaxWorkerName, shorten(parts))
	case refs && n >= MaxWorkerName:
		return registry.Unrealizable("%s would be named with %d characters before its parameter values, and a Worker's name is at most %d, one DNS label under workers.dev: %s", what, n, MaxWorkerName, shorten(parts))
	}
	return nil
}
