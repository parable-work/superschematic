// Package stackinit is what `superschematic stack init` writes: a Stack
// service over a tree that already has API services (docs/stack-model.md,
// section 11.1, D59). It derives the stack from the tree, renders the
// service's files through the TypeScript schema writer, and adds the
// ignore rules the stack's build and `stack dev` need. The CLI asks for
// the values and writes the files; this package decides what they hold.
package stackinit

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	ir "github.com/parable-work/superschematic/ir"
)

// Plan is the stack init writes.
type Plan struct {
	// Name is the stack service's name, which the stack takes.
	Name string

	// Deploy names the stack's entry points: every API and every Site
	// service of the tree, APIs first, each by name. The DB and Bucket
	// services join through the APIs' edges.
	Deploy []ir.ServiceRef

	// Expose names what is reachable from outside each environment
	// (Expose).
	Expose []ir.ServiceRef

	// ExposeNamed is whether a person named Expose in place of the list
	// the rule gives.
	ExposeNamed bool

	// ExposeApps are the apps the stack exposes after Expose: every @app
	// of a deployed API (AppsOf, D78).
	ExposeApps []AppRef

	// Environments are the stack's environments, in their order: the
	// local one first.
	Environments []Environment

	// CI names the CI renderers the stack's config turns on
	// (`outputs.ci`), none when empty.
	CI []string
}

// Environment is one environment init writes.
type Environment struct {
	// Name is the environment's class name.
	Name string

	// Target is the target that places its deployables.
	Target string

	// Values are the target's values, under the target's name.
	Values map[string]any

	// Comment is the comment written above the class.
	Comment string
}

// LocalEnvironment is the name of the local environment init writes.
const LocalEnvironment = "Dev"

// DefaultCloudEnvironment is the name of the cloud environment init writes
// when it is given none.
const DefaultCloudEnvironment = "Staging"

// namePattern is a stack service's name: lower-case words joined by
// hyphens, as a service's directory and every resource name it prefixes
// take it.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// environmentPattern is an environment's name: a class name.
var environmentPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

// CheckName refuses a stack name that is not lower-case words joined by
// hyphens.
func CheckName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("stack name %q: want lower-case letters and digits in words joined by hyphens, such as shop-stack", name)
	}
	return nil
}

// CheckEnvironment refuses an environment name that is no class name.
func CheckEnvironment(name string) error {
	if !environmentPattern.MatchString(name) {
		return fmt.Errorf("environment name %q: want a class name, a capital letter then letters and digits, such as Staging", name)
	}
	return nil
}

// DefaultName derives the stack's name from the tree: the first word the
// names of the services it deploys share, then "-stack" (shop-api and
// shop-orders give shop-stack); else the naming file's npm scope when it
// sets one (@acme gives acme-stack); else the directory that holds the
// schemas root.
func DefaultName(deployed []string, names naming.Naming, schemasRoot string) string {
	if word := sharedFirstWord(deployed); word != "" {
		return word + "-stack"
	}
	if scope := strings.TrimPrefix(names.NpmScope, "@"); scope != "" && names.NpmScope != naming.Default().NpmScope {
		if word := kebab(scope); word != "" {
			return word + "-stack"
		}
	}
	if word := kebab(filepath.Base(filepath.Dir(schemasRoot))); word != "" {
		return word + "-stack"
	}
	return "stack"
}

// sharedFirstWord returns the first hyphen-separated word every name
// shares, or "".
func sharedFirstWord(names []string) string {
	word := ""
	for i, name := range names {
		first, _, _ := strings.Cut(name, "-")
		switch {
		case i == 0:
			word = first
		case first != word:
			return ""
		}
	}
	if !namePattern.MatchString(word) {
		return ""
	}
	return word
}

// kebab turns s into lower-case words joined by hyphens, or "".
func kebab(s string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9' && b.Len() > 0:
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteRune(r)
		default:
			hyphen = true
		}
	}
	out := b.String()
	if !namePattern.MatchString(out) {
		return ""
	}
	return out
}

// ClassName is the @stack class's name: the stack's name without its
// "-stack" suffix, as a class (shop-stack gives Shop), with "Stack" added
// while it is one of taken, the imported handles and the environments.
func ClassName(name string, taken []string) string {
	class := tsutil.ToClassName(strings.TrimSuffix(name, "-stack"))
	if class == "" {
		class = "Stack"
	}
	for slices.Contains(taken, class) {
		class += "Stack"
	}
	return class
}

// Exposure is why an API is exposed or internal.
type Exposure struct {
	// Exposed is whether the API is reachable from outside.
	Exposed bool

	// Reason says why, for the summary init prints.
	Reason string
}

// Expose decides whether an API is exposed: it is when an end user or a
// third party can call one of its operations, an operation whose service
// clause, its own or its set's, is none or @allowService. An API whose
// every operation is @requireService, or that has none, only other
// servers call, and is internal. Each session route @userSessions adds is
// an operation like any other.
func Expose(schema *ir.Schema) Exposure {
	operations := 0
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			operations++
			if callers := ir.EffectiveServiceCallers(set, op); callers == nil || callers.Mode != ir.ServiceCallersRequire {
				return Exposure{Exposed: true, Reason: fmt.Sprintf("%s.%s is not @requireService", set.Name, op.Name)}
			}
		}
	}
	if operations == 0 {
		return Exposure{Reason: "it has no operations"}
	}
	return Exposure{Reason: "every operation is @requireService"}
}

// ExposeFor applies Expose to every API, and exposes each API a site
// calls, which D55 requires. apis maps each API's name to its schema, and
// siteCalls each site's name to the APIs it calls. It returns the APIs to
// expose, by name, and why each API is exposed or internal.
func ExposeFor(apis map[string]*ir.Schema, siteCalls map[string][]string) ([]string, map[string]Exposure) {
	reasons := make(map[string]Exposure, len(apis))
	for name, schema := range apis {
		reasons[name] = Expose(schema)
	}
	for _, site := range sortedKeys(siteCalls) {
		for _, api := range siteCalls[site] {
			if r, ok := reasons[api]; ok && !r.Exposed {
				reasons[api] = Exposure{Exposed: true, Reason: "site " + site + " calls it"}
			}
		}
	}
	var exposed []string
	for _, name := range sortedKeys(reasons) {
		if reasons[name].Exposed {
			exposed = append(exposed, name)
		}
	}
	return exposed, reasons
}

// CheckExpose refuses an expose list that names a service the stack does
// not deploy, or leaves out an API a site calls (D55).
func CheckExpose(expose []string, deploy []ir.ServiceRef, siteCalls map[string][]string) error {
	kinds := make(map[string]ir.SchemaKind, len(deploy))
	for _, ref := range deploy {
		kinds[ref.Name] = ref.Kind
	}
	for _, name := range expose {
		if _, ok := kinds[name]; !ok {
			return fmt.Errorf("expose names %s, which is no API or Site service of the tree", name)
		}
	}
	for _, site := range sortedKeys(siteCalls) {
		for _, api := range siteCalls[site] {
			if kinds[api] == ir.SchemaKindAPI && !slices.Contains(expose, api) {
				return fmt.Errorf("site %s calls %s, so %s must be exposed (D55)", site, api, api)
			}
		}
	}
	return nil
}

// Refs returns the handles of the named services, in the order given.
func Refs(names []string, kinds map[string]ir.SchemaKind) []ir.ServiceRef {
	refs := make([]ir.ServiceRef, 0, len(names))
	for _, name := range names {
		refs = append(refs, ir.ServiceRef{Name: name, Kind: kinds[name]})
	}
	return refs
}

// AppRef names an app: its @app class, of the API service that declares
// it.
type AppRef struct {
	API, App string
}

// AppsOf lists every @app of the APIs deploy names, in deploy's order and
// each API's declaration order, from apis, each API's schema by name. Init
// exposes each one (D78): an app is a program people use, and one the
// stack does not expose is reachable from inside its environment alone,
// which on gcp is no one.
func AppsOf(deploy []ir.ServiceRef, apis map[string]*ir.Schema) []AppRef {
	var out []AppRef
	for _, ref := range deploy {
		schema := apis[ref.Name]
		if ref.Kind != ir.SchemaKindAPI || schema == nil {
			continue
		}
		for _, app := range schema.Apps {
			out = append(out, AppRef{API: ref.Name, App: app.Name})
		}
	}
	return out
}

// Handles returns the class names of the handles the stack's schema
// imports.
func (p *Plan) Handles() []string {
	var out []string
	for _, ref := range append(slices.Clone(p.Deploy), p.Expose...) {
		if class := tsutil.ToClassName(ref.Name); !slices.Contains(out, class) {
			out = append(out, class)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
