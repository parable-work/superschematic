package registry

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"

	ir "github.com/parable-work/superschematic/ir"
)

// Core authoring packages the decorators below are declared in. These are
// the packages whose package.json owns the declarations (packages/); a
// distribution's own packages may re-export them, and the frontend resolves an
// identity through re-exports to the declaring package (tsreader/identity.go).
const (
	pkgAPI          = "@superschematic/api"
	pkgDB           = "@superschematic/db"
	pkgSchema       = "@superschematic/schema"
	pkgSchemaConfig = "@superschematic/schema-config"
)

var indexPurposeName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// coreDecorators returns one DecoratorSpec per (name, target) the tsreader
// walker used to switch on. Each Apply sets the typed IR field the walker's
// case arm set and returns the same error text for the same bad input. The
// walker still evaluates arguments and formats diagnostics; nothing about the
// IR changes. r supplies what an Apply reads from the assembled registry:
// @mcp reads its invocation policy and @behavior the behaviors when they
// run, after every extension has registered.
func coreDecorators(r *Registry) []DecoratorSpec {
	var specs []DecoratorSpec

	flag := func(target DecoratorTarget, name string, packages []string, set func(Node)) {
		specs = append(specs, DecoratorSpec{
			Name: name, Packages: packages, Target: target,
			Apply: func(n Node, _ []any, _ Site) error {
				set(n)
				return nil
			},
		})
	}
	marker := func(target DecoratorTarget, name string, packages []string) {
		specs = append(specs, DecoratorSpec{Name: name, Packages: packages, Target: target})
	}

	// Type declarations.
	marker(TargetType, "trait", []string{pkgSchema})
	// The argument evaluator reads a class named as a value, local or
	// imported, as a class reference ({"class": name}, ClassRef) for every
	// decorator with an Apply, and records an import for a class from another
	// service's package. @source, @versionGraph and @graphMember stay markers
	// the walker reads, because each would lose a diagnostic or change the IR
	// as an Apply; the reasons follow each registration.
	//
	// @source(Product) is projected from the class's declaration, whose
	// flattened fields Apply cannot reach, and a cross-service target is
	// compile-time lineage that must not be recorded as an import.
	specs = append(specs, DecoratorSpec{
		Name: "source", Packages: []string{pkgAPI, pkgSchema}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindAPI), string(ir.SchemaKindGeneral)},
	})
	// @envVars and @versioned are read by the walker before it resolves the
	// type's heritage and fields, so a @versioned config error is reported even
	// when a field later fails to resolve. Registering them with Apply would
	// move them after field resolution and lose that diagnostic.
	marker(TargetType, "envVars", []string{pkgSchemaConfig})
	marker(TargetType, "versioned", []string{pkgDB})
	// @versionGraph names no class; it is read beside @versioned, for the same
	// reason. @graphMember is too, and also reports each bad property at its
	// value (an ArgError points only at a whole argument), counts its repeats
	// on one type, and resolves graph and parent.of without recording an
	// import, since both must name classes of this schema.
	marker(TargetType, "versionGraph", []string{pkgDB})
	marker(TargetType, "graphMember", []string{pkgDB})
	flag(TargetType, "optimistic", []string{pkgDB}, func(n Node) { n.Type.Optimistic = true })
	flag(TargetType, "jsonField", []string{pkgDB, pkgSchema}, func(n Node) { n.Type.JsonField = true })
	flag(TargetType, "denyUnknownFields", []string{pkgSchema}, func(n Node) { n.Type.DenyUnknownFields = true })
	flag(TargetType, "strictJSON", []string{pkgSchema}, func(n Node) { n.Type.StrictJSON = true })
	specs = append(specs, DecoratorSpec{
		Name: "index", Packages: []string{pkgDB}, Target: TargetType,
		Apply: func(n Node, args []any, _ Site) error {
			idx, err := indexDef(args)
			if err != nil {
				return err
			}
			n.Type.Indexes = append(n.Type.Indexes, idx)
			return nil
		},
	})

	// SQL projection views: @projection and @join on the class, @column on
	// its fields (core_projection.go).
	specs = append(specs, projectionDecorators()...)

	// The Stack kind's declarations: @stack, @server, @database and
	// @environment (core_stack.go).
	specs = append(specs, stackDecorators()...)

	// An API service's jobs, a class each (core_jobs.go, D52).
	specs = append(specs, jobDecorator())

	// Fields.
	fieldFlag := func(name string, packages []string, set func(*ir.FieldDef)) {
		flag(TargetField, name, packages, func(n Node) { set(n.Field) })
	}
	fieldFlag("key", []string{pkgDB}, func(f *ir.FieldDef) { f.Key = true })
	fieldFlag("unique", []string{pkgDB}, func(f *ir.FieldDef) { f.Unique = true })
	fieldFlag("searchField", []string{pkgDB}, func(f *ir.FieldDef) { f.SearchField = true })
	fieldFlag("jsonField", []string{pkgDB, pkgSchema}, func(f *ir.FieldDef) { f.JsonField = true })
	fieldFlag("uiHidden", []string{pkgAPI, pkgSchema}, func(f *ir.FieldDef) { f.UIHidden = true })
	fieldFlag("internalMetadata", []string{pkgSchema}, func(f *ir.FieldDef) { f.InternalMetadata = true })
	fieldFlag("virtual", []string{pkgAPI, pkgSchema}, func(f *ir.FieldDef) { f.Virtual = true })
	fieldFlag("sourceMustProject", []string{pkgDB}, func(f *ir.FieldDef) { f.SourceMustProject = true })
	specs = append(specs, DecoratorSpec{
		Name: "conflictUnit", Packages: []string{pkgDB}, Target: TargetField,
		Apply: func(n Node, args []any, _ Site) error {
			unit, err := conflictUnit(args)
			if err != nil {
				return err
			}
			n.Field.ConflictUnit = unit
			return nil
		},
	})
	specs = append(specs, DecoratorSpec{
		Name: "temporalFormat", Packages: []string{pkgSchema}, Target: TargetField,
		Apply: func(n Node, args []any, _ Site) error {
			format, err := temporalFormat(args, n.Field)
			if err != nil {
				return err
			}
			n.Field.TemporalFormat = format
			return nil
		},
	})
	// Operation sets and operations share the middleware trio.
	for _, name := range []string{"rateLimit", "bodyLimit", "timeout"} {
		specs = append(specs, DecoratorSpec{
			Name: name, Packages: []string{pkgAPI}, Target: TargetOperationSet,
			Apply: func(n Node, args []any, _ Site) error {
				return applyMiddleware(name, args, &n.OperationSet.Middleware)
			},
		})
		specs = append(specs, DecoratorSpec{
			Name: name, Packages: []string{pkgAPI}, Target: TargetOperation,
			Apply: func(n Node, args []any, _ Site) error {
				return applyMiddleware(name, args, &n.Field.Middleware)
			},
			recordsErrors: true,
		})
	}

	// Operation sets and operations share the service clause pair (D37).
	// `from` only names who may call, so its handles are identities, not
	// references (D41).
	for _, clause := range []struct {
		name string
		mode ir.ServiceCallersMode
	}{
		{"requireService", ir.ServiceCallersRequire},
		{"allowService", ir.ServiceCallersAllow},
	} {
		specs = append(specs, DecoratorSpec{
			Name: clause.name, Packages: []string{pkgAPI}, Target: TargetOperationSet,
			Identities: []string{"from"},
			Apply: func(n Node, args []any, _ Site) error {
				return applyServiceCallers(clause.name, clause.mode, args, &n.OperationSet.ServiceCallers, "operation set")
			},
		})
		specs = append(specs, DecoratorSpec{
			Name: clause.name, Packages: []string{pkgAPI}, Target: TargetOperation,
			Identities: []string{"from"},
			Apply: func(n Node, args []any, _ Site) error {
				return applyServiceCallers(clause.name, clause.mode, args, &n.Field.ServiceCallers, "operation")
			},
		})
	}

	// Operations.
	opFlag := func(name string, set func(*ir.FieldDef)) {
		flag(TargetOperation, name, []string{pkgAPI}, func(n Node) { set(n.Field) })
	}
	opFlag("requireOwnership", func(f *ir.FieldDef) { f.RequireOwnership = true })
	opFlag("auth", func(f *ir.FieldDef) { f.Auth = true })
	opFlag("encrypted", func(f *ir.FieldDef) { f.Encrypted = true })
	opFlag("publicRoute", func(f *ir.FieldDef) { f.Public = true })
	opFlag("webhook", func(f *ir.FieldDef) { f.Webhook = true })
	opFlag("manualRouteRegistration", func(f *ir.FieldDef) { f.ManualRouteRegistration = true })
	specs = append(specs, DecoratorSpec{
		Name: "rest", Packages: []string{pkgAPI}, Target: TargetOperation,
		Apply: func(n Node, args []any, _ Site) error { return applyRest(args, n.Field) },
	})
	specs = append(specs, DecoratorSpec{
		Name: "requirePermission", Packages: []string{pkgAPI}, Target: TargetOperation,
		Apply: func(n Node, args []any, _ Site) error {
			if len(args) != 1 {
				return fmt.Errorf("@requirePermission takes exactly one array argument")
			}
			list, ok := args[0].([]any)
			if !ok || len(list) == 0 {
				return fmt.Errorf("@requirePermission requires a non-empty array of permission strings")
			}
			for _, p := range list {
				s, ok := p.(string)
				if !ok {
					return fmt.Errorf("@requirePermission entries must be string literals")
				}
				n.Field.Permissions = append(n.Field.Permissions, s)
			}
			return nil
		},
	})
	specs = append(specs, DecoratorSpec{
		Name: "hmacVerified", Packages: []string{pkgAPI}, Target: TargetOperation,
		Apply: func(n Node, args []any, _ Site) error {
			if len(args) != 1 {
				return fmt.Errorf("@hmacVerified takes exactly one config object")
			}
			cfg, ok := args[0].(map[string]any)
			if !ok {
				return fmt.Errorf("@hmacVerified config must be an object literal")
			}
			provider, ok := cfg["provider"].(string)
			if !ok || provider == "" {
				return fmt.Errorf("@hmacVerified requires a literal provider")
			}
			n.Field.HMACVerifiedProvider = provider
			return nil
		},
	})
	specs = append(specs, docsDecorators(r.ToolInvocationPolicy)...)
	specs = append(specs, behaviorDecorator(r.Behavior, r.BehaviorNames))
	specs = append(specs, displayDecorator())
	return specs
}

// conflictUnit reads @conflictUnit('keyed'). Which fields may carry which
// strategy is a verification rule (a member field; keyed and jsonSchema on a
// JSON object), since it reads the type the field sits on.
func conflictUnit(args []any) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("@conflictUnit takes exactly one strategy string")
	}
	unit, ok := args[0].(string)
	if !ok {
		return "", fmt.Errorf("@conflictUnit strategy must be a string literal")
	}
	switch unit {
	case ir.ConflictUnitAtomic, ir.ConflictUnitKeyed, ir.ConflictUnitJSONSchema, ir.ConflictUnitExcluded:
	default:
		return "", fmt.Errorf("@conflictUnit %q is not a strategy (atomic, keyed, jsonSchema, excluded)", unit)
	}
	return unit, nil
}

// temporalFormat reads @temporalFormat('unix_millis'). The unit is a fact
// about the source API: only the epoch units (unix, unix_millis,
// unix_micros, unix_nanos) are valid, and only on a plain Temporal.DateTime
// field -- an ISO field needs no declaration, and any other type would give
// the annotation nothing to decode into.
func temporalFormat(args []any, fd *ir.FieldDef) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("@temporalFormat takes exactly one format string")
	}
	format, ok := args[0].(string)
	if !ok {
		return "", fmt.Errorf("@temporalFormat format must be a string literal")
	}
	switch format {
	case "unix", "unix_millis", "unix_micros", "unix_nanos":
	default:
		return "", fmt.Errorf("@temporalFormat %q is not a declared epoch unit (unix, unix_millis, unix_micros, unix_nanos)", format)
	}
	if fd.TypeRef.Name != "Temporal.DateTime" || fd.TypeRef.IsArray || fd.TypeRef.IsMap {
		return "", fmt.Errorf("@temporalFormat is only valid on a Temporal.DateTime field, not %s", fd.TypeRef.Name)
	}
	return format, nil
}

// indexDef reads @index<T>(["fieldA", "fieldB"], true?) or
// @index<T>(["fieldA"], { unique?: boolean, name?: string }). Errors in the
// options object are ArgErrors so the frontend points at that argument.
func indexDef(args []any) (ir.IndexDef, error) {
	if len(args) < 1 || len(args) > 2 {
		return ir.IndexDef{}, fmt.Errorf("@index takes one array argument and an optional unique flag or options object")
	}
	list, ok := args[0].([]any)
	if !ok || len(list) == 0 {
		return ir.IndexDef{}, fmt.Errorf("@index requires a non-empty array of field names")
	}
	idx := ir.IndexDef{}
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return ir.IndexDef{}, fmt.Errorf("@index keys must be string literals")
		}
		idx.Keys = append(idx.Keys, s)
	}
	if len(args) == 2 {
		switch v := args[1].(type) {
		case bool:
			idx.Unique = v
		case map[string]any:
			for key, value := range v {
				switch key {
				case "unique":
					b, ok := value.(bool)
					if !ok {
						return ir.IndexDef{}, ArgErrorf(1, "@index unique must be a boolean literal")
					}
					idx.Unique = b
				case "name":
					s, ok := value.(string)
					if !ok {
						return ir.IndexDef{}, ArgErrorf(1, "@index name must be a string literal")
					}
					if !indexPurposeName.MatchString(s) {
						return ir.IndexDef{}, ArgErrorf(1, "@index name %q must be a lowercase identifier (e.g. digest)", s)
					}
					idx.Name = s
				default:
					return ir.IndexDef{}, ArgErrorf(1, "@index options has unknown key %q", key)
				}
			}
		default:
			return ir.IndexDef{}, ArgErrorf(1, "@index second argument must be a boolean unique flag or an options object")
		}
	}
	return idx, nil
}

// applyMiddleware folds one of @rateLimit/@bodyLimit/@timeout into a
// MiddlewareConfig, allocating it on first use. Its value is a whole number
// of at least 1: a server cannot apply a limit of 0 (the Go runtime would
// refuse every request) or a fraction of a request, megabyte or second.
func applyMiddleware(name string, args []any, target **ir.MiddlewareConfig) error {
	if len(args) != 1 {
		return fmt.Errorf("@%s takes exactly one config object", name)
	}
	cfg, ok := args[0].(map[string]any)
	if !ok {
		return fmt.Errorf("@%s config must be an object literal", name)
	}
	if *target == nil {
		*target = &ir.MiddlewareConfig{}
	}
	var key string
	var slot **int
	switch name {
	case "rateLimit":
		key, slot = "requestsPerMinute", &(*target).RateLimit
	case "bodyLimit":
		key, slot = "megabytes", &(*target).BodyLimit
	case "timeout":
		key, slot = "seconds", &(*target).Timeout
	default:
		return nil
	}
	f, ok := cfg[key].(float64)
	if !ok {
		return fmt.Errorf("@%s requires a literal %s", name, key)
	}
	if f < 1 || f != math.Trunc(f) || f > math.MaxInt32 {
		return fmt.Errorf("@%s %s must be a whole number of at least 1, not %v", name, key, f)
	}
	n := int(f)
	*slot = &n
	return nil
}

// applyServiceCallers reads @requireService({ from? }) or
// @allowService({ from? }) into a ServiceCallers. Each from entry is a
// service handle, which reaches Apply as {name, kind}; only an API service's
// server calls an operation, so a handle of any other kind is refused. An
// operation or a set takes one of the pair, once. Errors in the config are
// ArgErrors so the frontend points at it.
func applyServiceCallers(name string, mode ir.ServiceCallersMode, args []any, target **ir.ServiceCallers, holder string) error {
	if prior := *target; prior != nil {
		if other := serviceCallersDecorator(prior.Mode); other != name {
			return fmt.Errorf("@%s contradicts @%s on the same %s: declare one of them", name, other, holder)
		}
		return fmt.Errorf("@%s is declared twice on the same %s", name, holder)
	}
	if len(args) > 1 {
		return fmt.Errorf("@%s takes at most one config object", name)
	}
	clause := &ir.ServiceCallers{Mode: mode}
	if len(args) == 1 && args[0] != nil {
		raw, err := json.Marshal(args[0])
		if err != nil {
			return ArgErrorf(0, "@%s config must be an object literal", name)
		}
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return ArgErrorf(0, "@%s config must be an object literal", name)
		}
		for key := range cfg {
			if key != "from" {
				return ArgErrorf(0, "@%s config has unknown key %q", name, key)
			}
		}
		var from []json.RawMessage
		if list, ok := cfg["from"]; ok {
			if err := json.Unmarshal(list, &from); err != nil {
				return ArgErrorf(0, "@%s from must be an array of service handles", name)
			}
		}
		for _, entry := range from {
			var handle struct {
				Name string `json:"name"`
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(entry, &handle); err != nil || handle.Name == "" || handle.Kind == "" {
				return ArgErrorf(0, "@%s from entries must be service handles: import the API service's sentinel from its package", name)
			}
			if handle.Kind != string(ir.SchemaKindAPI) {
				return ArgErrorf(0, "@%s from lists %q, a %s service: only an API service's server calls an operation", name, handle.Name, handle.Kind)
			}
			clause.From = append(clause.From, handle.Name)
		}
	}
	*target = clause
	return nil
}

// serviceCallersDecorator names the decorator that declares mode.
func serviceCallersDecorator(mode ir.ServiceCallersMode) string {
	if mode == ir.ServiceCallersAllow {
		return "allowService"
	}
	return "requireService"
}

// applyRest reads @rest(HttpMethod.X, "path"?).
func applyRest(args []any, op *ir.FieldDef) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("@rest takes a method and an optional path")
	}
	methodName, ok := args[0].(string)
	if !ok {
		return fmt.Errorf("@rest method must be an HttpMethod member")
	}
	switch methodName {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
		op.HTTPMethod = methodName
	default:
		return fmt.Errorf("unsupported HTTP method %q", methodName)
	}
	if len(args) == 2 {
		path, ok := args[1].(string)
		if !ok {
			return fmt.Errorf("@rest path must be a string literal")
		}
		op.RestPath = path
	}
	return nil
}
