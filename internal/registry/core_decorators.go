package registry

import (
	"fmt"
	"regexp"

	ir "github.com/parable-work/superschematic/ir"
)

// Core authoring packages the decorators below are declared in. These are
// the packages whose package.json owns the declarations (utils/psgen/packages);
// the Parable @psgen/* packages re-export them, and the frontend resolves an
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
// IR changes.
func coreDecorators() []DecoratorSpec {
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
	flag(TargetType, "jsonField", []string{pkgDB, pkgSchema}, func(n Node) { n.Type.JsonField = true })
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
	return specs
}

// temporalFormat reads @temporalFormat('unix_millis'). The unit is a fact
// about the source API (PARABLE-2886): only the epoch members of
// IncrementalTimeFormatEnum are valid, and only on a plain Temporal.DateTime
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
// MiddlewareConfig, allocating it on first use.
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
	readInt := func(key string) (int, bool) {
		f, ok := cfg[key].(float64)
		return int(f), ok
	}
	switch name {
	case "rateLimit":
		n, ok := readInt("requestsPerMinute")
		if !ok {
			return fmt.Errorf("@rateLimit requires a literal requestsPerMinute")
		}
		(*target).RateLimit = &n
	case "bodyLimit":
		n, ok := readInt("megabytes")
		if !ok {
			return fmt.Errorf("@bodyLimit requires a literal megabytes")
		}
		(*target).BodyLimit = &n
	case "timeout":
		n, ok := readInt("seconds")
		if !ok {
			return fmt.Errorf("@timeout requires a literal seconds")
		}
		(*target).Timeout = &n
	}
	return nil
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
