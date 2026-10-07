package tswriter

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The Stack kind's declarations (docs/stack-model.md, section 4.1): each
// @stack, @server, @database and @environment class is written with its
// decorator, the argument the reader evaluates back to the declaration. A
// service handle is the service's sentinel, imported from its package, and
// a declared deployable is its class. An environment's order has no
// property: the reader numbers the classes by their place, which
// sortedTypes gives them.

// emitStackDeclarations renders the declarations def carries. A class
// takes one; the reader refuses a second, so the writer does not.
func (e *emitter) emitStackDeclarations(def *ir.TypeDef) {
	if s := def.Stack; s != nil {
		var parts []string
		if len(s.Deploy) > 0 {
			parts = append(parts, "deploy: "+e.handles(s.Deploy))
		}
		if len(s.Expose) > 0 {
			refs := make([]string, len(s.Expose))
			for i, ref := range s.Expose {
				refs[i] = e.deployableRef(fmt.Sprintf("type %s: @stack expose[%d]", def.Name, i), ref)
			}
			parts = append(parts, "expose: ["+strings.Join(refs, ", ")+"]")
		}
		fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("stack"), objectLiteral(parts))
	}
	if s := def.Server; s != nil {
		fmt.Fprintf(&e.body, "@%s({ serves: %s })\n", e.use("server"), e.handles(s.Serves))
	}
	if d := def.Database; d != nil {
		fmt.Fprintf(&e.body, "@%s({ hosts: %s })\n", e.use("database"), e.handles(d.Hosts))
	}
	if env := def.Environment; env != nil {
		fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("environment"), e.environmentLiteral(def.Name, env))
	}
}

// environmentLiteral renders @environment's argument: the target's values
// sit under the target's name.
func (e *emitter) environmentLiteral(name string, env *ir.EnvironmentDecl) string {
	var parts []string
	if env.Target != "" {
		parts = append(parts, "target: "+quote(env.Target))
	}
	if len(env.Values) > 0 {
		if env.Target == "" {
			e.failf("type %s: an environment's values with no target have no TypeScript form; @environment holds them under its target's name", name)
		} else {
			parts = append(parts, propertyName(env.Target)+": "+valueLiteral(env.Values))
		}
	}
	if env.Domain != "" {
		parts = append(parts, "domain: "+quote(env.Domain))
	}
	if dns := env.DNS; dns != nil {
		parts = append(parts, "dns: "+valueLiteral(map[string]any{dns.Platform: dns.Values}))
	}
	if len(env.Settings) > 0 {
		elements := make([]string, 0, len(env.Settings))
		for i, settings := range env.Settings {
			if settings != nil {
				elements = append(elements, e.settingsLiteral(fmt.Sprintf("type %s: @environment settings[%d]", name, i), settings))
			}
		}
		parts = append(parts, "settings: ["+strings.Join(elements, ", ")+"]")
	}
	if len(env.Parameters) > 0 {
		parts = append(parts, "parameters: "+stringListLiteral(env.Parameters))
	}
	return objectLiteral(parts)
}

// settingsLiteral renders one settings element: of, platform, env, and the
// platform settings in every other key.
func (e *emitter) settingsLiteral(owner string, settings *ir.DeployableSettings) string {
	parts := []string{"of: " + e.deployableRef(owner+" of", settings.Of)}
	if settings.Platform != "" {
		parts = append(parts, "platform: "+quote(settings.Platform))
	}
	for _, key := range sortedKeys(settings.Values) {
		switch key {
		case "of", "platform", "env":
			e.failf("%s: a platform setting named %q has no TypeScript form; the element's own key takes it", owner, key)
			continue
		}
		parts = append(parts, propertyName(key)+": "+valueLiteral(settings.Values[key]))
	}
	if len(settings.Env) > 0 {
		env := make([]string, 0, len(settings.Env))
		for _, field := range sortedKeys(settings.Env) {
			value := settings.Env[field]
			if value.Parameter != "" {
				env = append(env, propertyName(field)+": { parameter: "+quote(value.Parameter)+" }")
				continue
			}
			env = append(env, propertyName(field)+": "+valueLiteral(value.Value))
		}
		parts = append(parts, "env: "+objectLiteral(env))
	}
	return objectLiteral(parts)
}

// handles renders a list of service handles.
func (e *emitter) handles(refs []ir.ServiceRef) string {
	out := make([]string, len(refs))
	for i, ref := range refs {
		out[i] = e.handle(ref)
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// handle renders a service handle as the service's sentinel, which carries
// its kind, imported from its package as the reader resolves it.
func (e *emitter) handle(ref ir.ServiceRef) string {
	symbol := tsutil.ToClassName(ref.Name)
	e.importSymbol(naming.Active().NpmServicePackage(ref.Name), symbol)
	return symbol
}

// deployableRef renders a handle, or a declared deployable's class.
func (e *emitter) deployableRef(owner string, ref ir.DeployableRef) string {
	switch {
	case ref.Service != nil && ref.Deployable != "":
		e.failf("%s names both service %s and deployable %s", owner, ref.Service.Name, ref.Deployable)
	case ref.Service != nil:
		return e.handle(*ref.Service)
	case ref.Deployable != "":
		return e.ident(ref.Deployable, "deployable")
	default:
		e.failf("%s names no service and no deployable", owner)
	}
	return ""
}

// objectLiteral renders an object literal's properties on one line.
func objectLiteral(parts []string) string {
	if len(parts) == 0 {
		return "{}"
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// propertyName renders an object literal's key, quoted when it is not an
// identifier.
func propertyName(key string) string {
	if identifierPattern.MatchString(key) {
		return key
	}
	return quote(key)
}
