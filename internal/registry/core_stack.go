package registry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// pkgStack is the authoring package of the Stack kind's decorators.
const pkgStack = "@superschematic/stack"

// stackGenerator is the Stack kind's one generator: it resolves every
// environment of the stack and writes its environment.json. It is
// registered by generator.RegisterCore, with the other core generators.
const stackGenerator = "stack"

// stackKind is the core Stack kind (docs/stack-model.md, section 4.1). A
// stack names other services through their sentinels and is named by none,
// so it gets no sentinel of its own and its schema files import its
// siblings', which declares membership rather than references to their
// types. Its classes declare; they hold no fields and are no language's
// types, so the kind's one generator writes the resolved environments.
func stackKind() KindSpec {
	return KindSpec{
		Name:       string(ir.SchemaKindStack),
		StructRole: ir.RoleEmbeddedStruct,
		ForbiddenPackages: map[string]bool{
			pkgAPI: true,
			pkgDB:  true,
		},
		Pipeline:                []string{stackGenerator},
		NoSentinel:              true,
		ImportsSiblingSentinels: true,
		Verify:                  verifyStack,
	}
}

// The JSON Schemas of a service handle, as the TypeScript frontend
// evaluates a sentinel and the data forms write it, and of the place that
// takes a handle or a declared deployable's class.
const (
	stackHandleSchema = `{"type":"object","required":["name","kind"],"additionalProperties":false,"properties":{"name":{"type":"string","minLength":1},"kind":{"type":"string","minLength":1}}}`
	stackRefSchema    = `{"oneOf":[` + stackHandleSchema + `,{"type":"object","required":["class"],"additionalProperties":false,"properties":{"class":{"type":"string","minLength":1}}}]}`
)

var (
	stackArgs = json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"deploy": {"type": "array", "items": ` + stackHandleSchema + `},
			"expose": {"type": "array", "items": ` + stackRefSchema + `}
		}
	}`)
	serverArgs = json.RawMessage(`{
		"type": "object",
		"required": ["serves"],
		"additionalProperties": false,
		"properties": {"serves": {"type": "array", "minItems": 1, "items": ` + stackHandleSchema + `}}
	}`)
	databaseArgs = json.RawMessage(`{
		"type": "object",
		"required": ["hosts"],
		"additionalProperties": false,
		"properties": {"hosts": {"type": "array", "minItems": 1, "items": ` + stackHandleSchema + `}}
	}`)
	// Every key of @environment's argument other than those named here
	// holds the values of the target the environment names, under the
	// target's name (`gcp: { project, region }`). A settings element's keys
	// other than of, job, platform, env, schedule, timeZone and enabled are
	// the platform settings. job names a job of the API of; schedule,
	// timeZone and enabled change that job's schedule (D52).
	environmentArgs = json.RawMessage(`{
		"type": "object",
		"additionalProperties": {"type": "object"},
		"properties": {
			"target": {"type": "string", "minLength": 1},
			"domain": {"type": "string", "minLength": 1},
			"dns": {"type": "object", "minProperties": 1, "maxProperties": 1, "additionalProperties": {"type": "object"}},
			"parameters": {"type": "array", "items": {"type": "string", "minLength": 1}},
			"settings": {"type": "array", "items": {
				"type": "object",
				"required": ["of"],
				"properties": {
					"of": ` + stackRefSchema + `,
					"job": {"type": "string", "minLength": 1},
					"schedule": {"type": "string", "minLength": 1},
					"timeZone": {"type": "string", "minLength": 1},
					"enabled": {"type": "boolean"},
					"platform": {"type": "string", "minLength": 1},
					"env": {"type": "object", "additionalProperties": {"oneOf": [
						{"type": ["string", "number", "boolean"]},
						{"type": "object", "required": ["parameter"], "additionalProperties": false, "properties": {"parameter": {"type": "string", "minLength": 1}}}
					]}}
				}
			}}
		}
	}`)
)

// stackDecorators returns @stack, @server, @database and @environment, from
// @superschematic/stack and only in Stack schemas. Each writes its class's
// declaration (ir.TypeDef's Stack, Server, Database or Environment), and a
// class takes one. A handle evaluates to {name, kind} and a class to a class
// reference, so Apply reads the same value from every form. Whether a class
// a declaration names is a deployable of the schema is verifyStack's, so it
// holds for the data forms too.
func stackDecorators() []DecoratorSpec {
	stack := []string{string(ir.SchemaKindStack)}
	spec := func(name string, args json.RawMessage, apply func(*ir.TypeDef, any) error) DecoratorSpec {
		return DecoratorSpec{
			Name: name, Packages: []string{pkgStack}, Target: TargetType, Kinds: stack, Args: args,
			Apply: func(n Node, args []any, _ Site) error {
				if other := stackDeclaration(n.Type); other != "" {
					return fmt.Errorf("class %s is already an @%s class; a class takes one of @stack, @server, @database and @environment", n.Type.Name, other)
				}
				if len(args) != 1 {
					return fmt.Errorf("@%s takes exactly one argument", name)
				}
				return apply(n.Type, args[0])
			},
		}
	}
	return []DecoratorSpec{
		spec("stack", stackArgs, applyStack),
		spec("server", serverArgs, func(td *ir.TypeDef, arg any) error {
			var decl ir.ServerDecl
			if err := DecodeArgs([]any{arg}, &decl); err != nil {
				return err
			}
			td.Server = &decl
			return nil
		}),
		spec("database", databaseArgs, func(td *ir.TypeDef, arg any) error {
			var decl ir.DatabaseDecl
			if err := DecodeArgs([]any{arg}, &decl); err != nil {
				return err
			}
			td.Database = &decl
			return nil
		}),
		spec("environment", environmentArgs, applyEnvironment),
	}
}

// stackDeclaration names the stack declaration td already has, or "".
func stackDeclaration(td *ir.TypeDef) string {
	switch {
	case td.Stack != nil:
		return "stack"
	case td.Server != nil:
		return "server"
	case td.Database != nil:
		return "database"
	case td.Environment != nil:
		return "environment"
	}
	return ""
}

// applyStack reads @stack({ deploy, expose }).
func applyStack(td *ir.TypeDef, arg any) error {
	var args struct {
		Deploy []ir.ServiceRef `json:"deploy"`
		Expose []any           `json:"expose"`
	}
	if err := DecodeArgs([]any{arg}, &args); err != nil {
		return err
	}
	decl := &ir.StackDecl{Deploy: args.Deploy}
	for i, v := range args.Expose {
		ref, err := deployableRef(v)
		if err != nil {
			return ArgErrorf(0, "@stack expose[%d]: %v", i, err)
		}
		decl.Expose = append(decl.Expose, ref)
	}
	td.Stack = decl
	return nil
}

// applyEnvironment reads @environment({ target, <target>: values, domain,
// dns, settings, parameters }).
func applyEnvironment(td *ir.TypeDef, arg any) error {
	args, ok := arg.(map[string]any)
	if !ok {
		return ArgErrorf(0, "@environment takes an object")
	}
	decl := &ir.EnvironmentDecl{}
	decl.Target, _ = args["target"].(string)
	for _, key := range sortedKeys(args) {
		value := args[key]
		switch key {
		case "target":
		case "domain":
			decl.Domain, _ = value.(string)
		case "dns":
			for platform, values := range value.(map[string]any) {
				decl.DNS = &ir.DNSPlacement{Platform: platform, Values: nonEmpty(values.(map[string]any))}
			}
		case "parameters":
			for _, p := range value.([]any) {
				decl.Parameters = append(decl.Parameters, p.(string))
			}
		case "settings":
			for i, element := range value.([]any) {
				settings, err := deployableSettings(element.(map[string]any))
				if err != nil {
					return ArgErrorf(0, "@environment settings[%d]: %v", i, err)
				}
				decl.Settings = append(decl.Settings, settings)
			}
		default:
			switch {
			case decl.Target == "":
				return ArgErrorf(0, "@environment holds values under %q but names no target; the values of a target are keyed by its name, so name the target to set them", key)
			case key != decl.Target:
				return ArgErrorf(0, "@environment holds values under %q, but its target is %s; the values of a target are keyed by its name", key, decl.Target)
			}
			decl.Values = nonEmpty(value.(map[string]any))
		}
	}
	td.Environment = decl
	return nil
}

// deployableSettings reads one settings element: of, job, platform, env,
// and a job's schedule, timeZone and enabled, and the platform settings in
// every other key.
func deployableSettings(element map[string]any) (*ir.DeployableSettings, error) {
	settings := &ir.DeployableSettings{}
	var job string
	for _, key := range sortedKeys(element) {
		value := element[key]
		switch key {
		case "of":
			ref, err := deployableRef(value)
			if err != nil {
				return nil, fmt.Errorf("of: %w", err)
			}
			settings.Of = ref
		case "job":
			job, _ = value.(string)
		case "schedule":
			settings.Schedule, _ = value.(string)
		case "timeZone":
			settings.TimeZone, _ = value.(string)
		case "enabled":
			enabled, _ := value.(bool)
			settings.Enabled = &enabled
		case "platform":
			settings.Platform, _ = value.(string)
		case "env":
			for _, field := range sortedKeys(value.(map[string]any)) {
				if settings.Env == nil {
					settings.Env = map[string]ir.EnvValue{}
				}
				v := value.(map[string]any)[field]
				if param, ok := v.(map[string]any); ok {
					settings.Env[field] = ir.EnvValue{Parameter: param["parameter"].(string)}
					continue
				}
				settings.Env[field] = ir.EnvValue{Value: v}
			}
		default:
			if settings.Values == nil {
				settings.Values = map[string]any{}
			}
			settings.Values[key] = value
		}
	}
	if job != "" {
		if settings.Of.Service == nil {
			return nil, fmt.Errorf("job %s names a job of the API service of names, but of names no service; write `of: <API handle>, job: %q`", job, job)
		}
		settings.Of.Job = job
	}
	return settings, nil
}

// deployableRef reads a handle, {name, kind}, or a declared deployable's
// class, {"class": name}.
func deployableRef(v any) (ir.DeployableRef, error) {
	if ref, ok := ir.AsClassRef(v); ok {
		return ir.DeployableRef{Deployable: ref.Class}, nil
	}
	var handle ir.ServiceRef
	if err := DecodeArgs([]any{v}, &handle); err != nil || handle.Name == "" || handle.Kind == "" {
		return ir.DeployableRef{}, fmt.Errorf("want a service handle or a declared deployable's class")
	}
	return ir.DeployableRef{Service: &handle}, nil
}

// nonEmpty returns m, or nil when it is empty, as the data forms leave out
// an empty object.
func nonEmpty(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	return m
}

// verifyStack is the Stack kind's verification (docs/stack-model.md,
// section 4.1), in every form: the schema declares one @stack class, every
// class declares exactly one of @stack, @server, @database and
// @environment and holds no fields, only an @environment class extends
// another and then another @environment class, every class a declaration
// names is an @server or @database class of the schema, and no two
// environments share an order. Resolution checks the handles against the
// services they name.
func verifyStack(schema *ir.Schema, r VerifyReporter) {
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	var stacks []string
	// ordered holds the environment that took each order first, by name.
	// The TypeScript reader numbers the classes, so only a data form
	// writes two alike.
	ordered := map[int]string{}
	for _, name := range names {
		td := schema.Types[name]
		if td == nil {
			continue
		}
		declared := []string{}
		for _, d := range []struct {
			name string
			set  bool
		}{
			{"stack", td.Stack != nil}, {"server", td.Server != nil},
			{"database", td.Database != nil}, {"environment", td.Environment != nil},
		} {
			if d.set {
				declared = append(declared, "@"+d.name)
			}
		}
		switch len(declared) {
		case 0:
			r.Errorf(td.Owner, "class %s declares nothing; every class of a Stack schema is a @stack, @server, @database or @environment class", name)
			continue
		case 1:
		default:
			r.Errorf(td.Owner, "class %s is %s; a class takes one of them", name, strings.Join(declared, " and "))
			continue
		}
		if len(td.Fields) > 0 {
			r.Errorf(td.Owner, "class %s has fields; the classes of a Stack schema declare, and hold no fields", name)
		}
		if td.Stack != nil {
			stacks = append(stacks, name)
		}
		if td.Extends != "" {
			parent := schema.Types[td.Extends]
			switch {
			case td.Environment == nil:
				r.Errorf(td.Owner, "%s class %s extends %s; only an @environment class extends, another @environment class", declared[0], name, td.Extends)
			case parent == nil || parent.Environment == nil:
				r.Errorf(td.Owner, "@environment class %s extends %s, which is not an @environment class of this schema", name, td.Extends)
			}
		}
		checkRef := func(where string, ref ir.DeployableRef) {
			switch {
			case ref.Service != nil && ref.Deployable != "":
				r.Errorf(td.Owner, "%s names both service %s and deployable %s", where, ref.Service.Name, ref.Deployable)
			case ref.Service == nil && ref.Deployable == "":
				r.Errorf(td.Owner, "%s names no service and no deployable", where)
			case ref.Deployable != "":
				if d := schema.Types[ref.Deployable]; d == nil || (d.Server == nil && d.Database == nil) {
					r.Errorf(td.Owner, "%s names class %s, which is not an @server or @database class of this schema", where, ref.Deployable)
				}
				if ref.Job != "" {
					r.Errorf(td.Owner, "%s names job %s of deployable %s; a job belongs to an API service, so name the API's handle", where, ref.Job, ref.Deployable)
				}
			}
		}
		if td.Stack != nil {
			for i, ref := range td.Stack.Expose {
				where := fmt.Sprintf("@stack class %s expose[%d]", name, i)
				checkRef(where, ref)
				if ref.Job != "" {
					r.Errorf(td.Owner, "%s names job %s; only a server is exposed", where, ref.Job)
				}
			}
		}
		if td.Environment != nil {
			for i, settings := range td.Environment.Settings {
				if settings != nil {
					where := fmt.Sprintf("@environment class %s settings[%d]", name, i)
					checkRef(where+" of", settings.Of)
					checkJobSettings(where, td.Owner, settings, r)
				}
			}
			switch order := td.Environment.Order; {
			case order < 0:
				r.Errorf(td.Owner, "@environment class %s has order %d; an order counts from 1, and an environment without one leaves it out", name, order)
			case order == 0:
			case ordered[order] != "":
				r.Errorf(td.Owner, "@environment classes %s and %s both have order %d; each environment takes its own place in the order", ordered[order], name, order)
			default:
				ordered[order] = name
			}
		}
	}
	switch len(stacks) {
	case 0:
		r.Errorf("", "Stack schema %s declares no @stack class; one class names the stack", schema.Name)
	case 1:
	default:
		r.Errorf("", "Stack schema %s declares @stack on %s; a schema declares one stack", schema.Name, strings.Join(stacks, " and "))
	}
}

// checkJobSettings checks a settings element's job settings (D52): only an
// element whose of names a job sets a schedule, a time zone or enabled, a
// schedule is a five-field cron and a time zone an IANA one. Whether the
// API declares the job is resolution's to check.
func checkJobSettings(where, owner string, settings *ir.DeployableSettings, r VerifyReporter) {
	if settings.Of.Job == "" {
		var keys []string
		if settings.Schedule != "" {
			keys = append(keys, "schedule")
		}
		if settings.TimeZone != "" {
			keys = append(keys, "timeZone")
		}
		if settings.Enabled != nil {
			keys = append(keys, "enabled")
		}
		if len(keys) > 0 {
			r.Errorf(owner, "%s sets %s, which only a job takes; name the job beside its API's handle, `of: <API handle>, job: \"<job class>\"`", where, strings.Join(keys, " and "))
		}
		return
	}
	if settings.Schedule != "" {
		if err := CheckSchedule(settings.Schedule); err != nil {
			r.Errorf(owner, "%s schedule: %v", where, err)
		}
	}
	if settings.TimeZone != "" {
		if err := CheckTimeZone(settings.TimeZone); err != nil {
			r.Errorf(owner, "%s timeZone: %v", where, err)
		}
	}
}
