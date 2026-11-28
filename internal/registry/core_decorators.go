package registry

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
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

// sqlIdentifier is a table or column name @table and @column take (D85):
// letters, digits and underscores, not starting with a digit, which every
// statement quotes where Postgres and SQLite need it (QuoteIdentifier).
var sqlIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// sqlIdentifierArg reads the one SQL identifier decorator takes.
func sqlIdentifierArg(decorator string, args []any) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("%s takes exactly one name", decorator)
	}
	name, ok := args[0].(string)
	if !ok {
		return "", ArgErrorf(0, "%s takes a string literal", decorator)
	}
	if !sqlIdentifier.MatchString(name) {
		return "", ArgErrorf(0, "%s(%q): a name is letters, digits and underscores, not starting with a digit", decorator, name)
	}
	if len(name) > sqlutil.PostgresIdentifierLimit {
		return "", ArgErrorf(0, "%s(%q) is %d bytes, over PostgreSQL's %d-byte limit", decorator, name, len(name), sqlutil.PostgresIdentifierLimit)
	}
	return name, nil
}

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
	flag(TargetType, "allowUnknownFields", []string{pkgSchema}, func(n Node) { n.Type.AllowUnknownFields = true })
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
	// The table a DB type is stored in, in place of its name in snake case
	// (D85).
	specs = append(specs, DecoratorSpec{
		Name: "table", Packages: []string{pkgDB}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindDB)},
		Apply: func(n Node, args []any, _ Site) error {
			name, err := sqlIdentifierArg("@table", args)
			if err != nil {
				return err
			}
			if n.Type.SQLName != "" {
				return fmt.Errorf("a class takes one @table")
			}
			n.Type.SQLName = name
			return nil
		},
	})
	// Raw SQL the DB service runs after its own objects (D83).
	specs = append(specs, DecoratorSpec{
		Name: "rawSql", Packages: []string{pkgDB}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindDB)},
		Apply: func(n Node, args []any, _ Site) error {
			raw, err := rawSQLDef(args)
			if err != nil {
				return err
			}
			n.Type.RawSQL = append(n.Type.RawSQL, raw)
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

	// A DB service's queues, a message class each, and an API service's
	// workers, which handle them (core_queues.go, D53); and the triggers
	// that enqueue a queue's message on a write to a table
	// (core_enqueue.go, D80).
	specs = append(specs, queueDecorator(), workerDecorator(), enqueueOnChangeDecorator())

	// The moves an enum field of a class may make, in a schema of any kind
	// (core_state_machine.go, D118).
	specs = append(specs, stateMachineDecorator())

	// The counters of a DB table every covered write advances by one
	// (core_fenced.go, D119).
	specs = append(specs, fencedDecorator())

	// Per-record access: a table's audience, the table of grants and the
	// access log (core_access.go, D156).
	specs = append(specs, accessDecorators()...)

	// An API's provider slots, each a config field that names a runtime
	// provider per environment (core_providers.go, D159).
	specs = append(specs, providersDecorator())

	// A table's references to rows of any table, and the files it holds in
	// a bucket (core_rowrefs.go, D154).
	specs = append(specs, rowFeatureDecorators()...)

	// The dates of a table's rows, or its Recurrence's occurrences, that
	// the calendar merges (core_calendar.go, D152).
	specs = append(specs, calendarDecorators()...)

	// The fields of a table the database's search index holds, and the queue
	// a write that changes them enqueues on (core_search.go, D155).
	specs = append(specs, searchDecorator())

	// The events a table's rows raise, the notifier's tables and the table
	// of channel identities (core_notifications.go, D153).
	specs = append(specs, notificationDecorators()...)

	// The agent orchestration's tables and an operation's autonomy class
	// (core_agents.go, D158).
	specs = append(specs, agentDecorators()...)

	// The tables whose rows are parties, the conversations' tables and the
	// queue of run requests (core_parties.go, D157).
	specs = append(specs, partyDecorators()...)

	// A DB service's declared queries, a class each (core_queries.go, D86).
	specs = append(specs, queryDecorator())

	// The roles a UserRole table holds from its first migration on
	// (core_seed_roles.go, D88).
	specs = append(specs, seedRolesDecorator())
	// The rows any DB table holds from its first migration on
	// (core_seed_rows.go, #625).
	specs = append(specs, seedRowsDecorator())

	// An API service's derivers, which fill the Derived fields of its
	// engine database's types (core_derivers.go, D74).
	specs = append(specs, deriverDecorator())
	// A MemoryStore service's channels and keys, a class each
	// (core_memorystore.go, D75).
	specs = append(specs, memoryStoreDecorators()...)

	// A Tasks service's declarations: @tasks and @task (core_tasks.go,
	// D203).
	specs = append(specs, tasksDecorators()...)

	// An API service's apps, the project's own HTTP programs over its Deps
	// (core_apps.go, D78).
	specs = append(specs, appDecorator())

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
	// @decrypted, or @decrypted({ permission }): reveal is its own
	// permission (D156).
	specs = append(specs, DecoratorSpec{
		Name: "decrypted", Packages: []string{pkgAPI, pkgSchema}, Target: TargetField,
		Apply: func(n Node, args []any, _ Site) error {
			n.Field.Decrypted = true
			if len(args) == 0 {
				return nil
			}
			obj, ok := args[0].(map[string]any)
			if len(args) != 1 || !ok {
				return ArgErrorf(0, "@decrypted takes no argument, or { permission }: the permission a caller holds to see the value")
			}
			for _, key := range slices.Sorted(maps.Keys(obj)) {
				if key != "permission" {
					return ArgErrorf(0, "@decrypted has no option %q; it takes permission", key)
				}
			}
			permission, ok := obj["permission"].(string)
			if !ok || permission == "" {
				return ArgErrorf(0, "@decrypted's permission is the permission a caller holds to see the value, as \"contracts.reveal\"")
			}
			n.Field.RevealPermission = permission
			return nil
		},
	})
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
	specs = append(specs, DecoratorSpec{
		Name: "idempotent", Packages: []string{pkgAPI}, Target: TargetOperation,
		Apply: func(n Node, args []any, _ Site) error {
			config, err := idempotentConfig(args)
			if err != nil {
				return err
			}
			n.Field.Idempotent = config
			return nil
		},
		recordsErrors: true,
	})
	// An operation whose success body is a stream, not the envelope (D167).
	specs = append(specs, DecoratorSpec{
		Name: "httpBinaryStream", Packages: []string{pkgAPI}, Target: TargetOperation,
		Apply: func(n Node, args []any, _ Site) error {
			if n.Field.HTTPBinaryStream != nil {
				return fmt.Errorf("operation %s has more than one @httpBinaryStream decorator", n.Field.Name)
			}
			stream, err := httpBinaryStreamDef(args)
			if err != nil {
				return err
			}
			n.Field.HTTPBinaryStream = stream
			return nil
		},
	})
	// Operation sets and operations share the middleware trio.
	for _, name := range []string{"rateLimit", "bodyLimit", "timeout"} {
		specs = append(specs, DecoratorSpec{
			Name: name, Packages: []string{pkgAPI}, Target: TargetOperationSet,
			Apply: func(n Node, args []any, _ Site) error {
				return applyMiddleware(name, args, &n.OperationSet.Middleware, r.isRateLimitKey)
			},
		})
		specs = append(specs, DecoratorSpec{
			Name: name, Packages: []string{pkgAPI}, Target: TargetOperation,
			Apply: func(n Node, args []any, _ Site) error {
				return applyMiddleware(name, args, &n.Field.Middleware, r.isRateLimitKey)
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

	// The user model's route sets (D50), on a class with no methods
	// (core_identity_routes.go).
	specs = append(specs, identityRouteDecorators()...)

	// A table's behavior operations (D103), on a class with no members
	// (core_behavior_operations.go), and the worker that runs its
	// behaviors' queues (core_behavior_workers.go), and the set's user
	// clause, which
	// each operation the loader adds takes as its own. The verification
	// pass refuses the clause on any other set. @requireOwnership has no
	// class form: the routes run no implementation that could check what
	// the caller owns.
	specs = append(specs, behaviorOperationsDecorator(), behaviorWorkerDecorator())
	// A version graph's operations (D138), on a class with no members
	// (core_graph_operations.go), which takes the same clause.
	specs = append(specs, graphOperationsDecorator())
	// A table's references and files (#723, D154), on a class with no
	// members (core_rowfeature_operations.go), which takes the same clause.
	specs = append(specs, rowFeatureOperationsDecorator())
	// The inbound path's triage (#746, D157), on a class with no members
	// (core_triage_operations.go), which takes the same clause.
	specs = append(specs, triageOperationsDecorator())
	// The active grants (#735), on a class with no members
	// (core_access_grants_operations.go), which takes the same clause.
	specs = append(specs, accessGrantsOperationsDecorator())
	flag(TargetOperationSet, "auth", []string{pkgAPI}, func(n Node) { n.OperationSet.Auth = true })
	flag(TargetOperationSet, "publicRoute", []string{pkgAPI}, func(n Node) { n.OperationSet.Public = true })
	specs = append(specs, DecoratorSpec{
		Name: "requirePermission", Packages: []string{pkgAPI}, Target: TargetOperationSet,
		Apply: func(n Node, args []any, _ Site) error { return applySetPermissions(args, n.OperationSet) },
	})

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
			perms, err := requirePermissionList(args)
			if err != nil {
				return err
			}
			n.Field.Permissions = append(n.Field.Permissions, perms...)
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
	// A list operation that pages by a cursor (core_page.go, D60).
	specs = append(specs, paginatedDecorator())
	// An operation the build serves from a table (core_table_operation.go,
	// D106).
	specs = append(specs, tableOperationDecorator())
	specs = append(specs, docsDecorators(r.ToolInvocationPolicy)...)
	specs = append(specs, behaviorDecorator(r.Behavior, r.BehaviorNames))
	specs = append(specs, displayDecorator(), fieldDisplayDecorator())
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
				case "nullsNotDistinct":
					b, ok := value.(bool)
					if !ok {
						return ir.IndexDef{}, ArgErrorf(1, "@index nullsNotDistinct must be a boolean literal")
					}
					idx.NullsNotDistinct = b
				case "name":
					s, ok := value.(string)
					if !ok {
						return ir.IndexDef{}, ArgErrorf(1, "@index name must be a string literal")
					}
					if !indexPurposeName.MatchString(s) {
						return ir.IndexDef{}, ArgErrorf(1, "@index name %q must be a lowercase identifier (e.g. digest)", s)
					}
					idx.Name = s
				case "method":
					s, ok := value.(string)
					if !ok || !slices.Contains(ir.IndexMethods, s) {
						return ir.IndexDef{}, ArgErrorf(1, "@index method must be one of %s", strings.Join(ir.IndexMethods, ", "))
					}
					idx.Method = s
				case "ops":
					s, ok := value.(string)
					if !ok || !sqlutil.ValidOpclass(s) {
						return ir.IndexDef{}, ArgErrorf(1, "@index ops must name an operator class, a lowercase identifier such as gin_trgm_ops, optionally schema-qualified")
					}
					idx.Ops = s
				case "where", "expression":
					s, ok := value.(string)
					if !ok || strings.TrimSpace(s) == "" {
						return ir.IndexDef{}, ArgErrorf(1, "@index %s must be a non-empty string of SQL", key)
					}
					if key == "where" {
						idx.Where = strings.TrimSpace(s)
					} else {
						idx.Expression = strings.TrimSpace(s)
					}
				case "concurrently":
					b, ok := value.(bool)
					if !ok {
						return ir.IndexDef{}, ArgErrorf(1, "@index concurrently must be a boolean literal")
					}
					idx.Concurrently = &b
				case "m", "efConstruction", "lists":
					n, ok := wholeNumber(value)
					if !ok {
						return ir.IndexDef{}, ArgErrorf(1, "@index %s must be a whole number literal", key)
					}
					switch key {
					case "m":
						idx.M = &n
					case "efConstruction":
						idx.EFConstruction = &n
					default:
						idx.Lists = &n
					}
				default:
					return ir.IndexDef{}, ArgErrorf(1, "@index options has unknown key %q", key)
				}
			}
		default:
			return ir.IndexDef{}, ArgErrorf(1, "@index second argument must be a boolean unique flag or an options object")
		}
	}
	if idx.NullsNotDistinct && !idx.Unique {
		return ir.IndexDef{}, ArgErrorf(1, "@index nullsNotDistinct requires unique: true")
	}
	return idx, nil
}

// wholeNumber is a decorator argument's number literal as an int, when it
// is a whole one.
func wholeNumber(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}

// rawSQLDef reads @rawSql's options object: id and up, and down, after and
// dialects when given. Verification checks the SQL and the order
// (sqlutil.CheckRawSQL).
func rawSQLDef(args []any) (ir.RawSQLDef, error) {
	if len(args) != 1 {
		return ir.RawSQLDef{}, fmt.Errorf("@rawSql takes one options object: { id, up, down?, after?, dialects? }")
	}
	opts, ok := args[0].(map[string]any)
	if !ok {
		return ir.RawSQLDef{}, ArgErrorf(0, "@rawSql takes one options object: { id, up, down?, after?, dialects? }")
	}
	var raw ir.RawSQLDef
	for key, value := range opts {
		switch key {
		case "id", "up", "down", "after":
			s, ok := value.(string)
			if !ok {
				return ir.RawSQLDef{}, ArgErrorf(0, "@rawSql %s must be a string literal", key)
			}
			switch key {
			case "id":
				raw.ID = s
			case "up":
				raw.Up = s
			case "down":
				raw.Down = s
			case "after":
				raw.After = s
			}
		case "dialects":
			list, ok := value.([]any)
			if !ok {
				return ir.RawSQLDef{}, ArgErrorf(0, "@rawSql dialects must be a list of dialect names")
			}
			for _, e := range list {
				d, ok := e.(string)
				if !ok {
					return ir.RawSQLDef{}, ArgErrorf(0, "@rawSql dialects must be a list of dialect names")
				}
				raw.Dialects = append(raw.Dialects, d)
			}
		default:
			return ir.RawSQLDef{}, ArgErrorf(0, "@rawSql options has unknown key %q", key)
		}
	}
	if raw.ID == "" || raw.Up == "" {
		return ir.RawSQLDef{}, ArgErrorf(0, "@rawSql needs an id and its up SQL")
	}
	return raw, nil
}

// applyMiddleware folds one of @rateLimit/@bodyLimit/@timeout into a
// MiddlewareConfig, allocating it on first use. Its value is a whole number
// of at least 1: a server cannot apply a limit of 0 (the Go runtime would
// refuse every request) or a fraction of a request, megabyte or second.
// @rateLimit also takes a key (D131), one isRateLimitKey accepts; it is
// written down as given, and an absent one leaves the key empty, which
// counts per client IP.
func applyMiddleware(name string, args []any, target **ir.MiddlewareConfig, isRateLimitKey func(string) bool) error {
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
	if name == "rateLimit" {
		(*target).RateLimitKey = ""
		if raw, set := cfg["key"]; set {
			key, ok := raw.(string)
			if !ok || !isRateLimitKey(key) {
				return fmt.Errorf("@rateLimit key must be %q, %q or a key an extension registers, not %v", ir.RateLimitKeyIP, ir.RateLimitKeyPrincipal, raw)
			}
			(*target).RateLimitKey = key
		}
	}
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
		if err := ir.ValidateRestPath(path); err != nil {
			return ArgErrorf(1, "%s", err)
		}
		op.RestPath = path
	}
	return nil
}

// idempotentConfig reads @idempotent({ ttl?, required? }) (D140): no
// argument, or one config object. ttl is a duration of whole seconds as Go
// writes one, `24h`, `90m` or `30s`, at most ir.MaxIdempotencyTTLSeconds;
// absent leaves the default. required is a boolean.
func idempotentConfig(args []any) (*ir.IdempotentConfig, error) {
	config := &ir.IdempotentConfig{}
	if len(args) == 0 {
		return config, nil
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("@idempotent takes at most one config object")
	}
	cfg, ok := args[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("@idempotent config must be an object literal")
	}
	for key, value := range cfg {
		switch key {
		case "ttl":
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("@idempotent ttl must be a duration in a string, such as \"24h\"")
			}
			seconds, err := TimeoutSeconds(text)
			if err != nil {
				return nil, fmt.Errorf("@idempotent ttl %s", err)
			}
			if seconds > ir.MaxIdempotencyTTLSeconds {
				return nil, fmt.Errorf("@idempotent ttl %q is longer than %d days", text, ir.MaxIdempotencyTTLSeconds/86400)
			}
			config.TTLSeconds = seconds
		case "required":
			required, ok := value.(bool)
			if !ok {
				return nil, fmt.Errorf("@idempotent required must be true or false")
			}
			config.Required = required
		default:
			return nil, fmt.Errorf("@idempotent has no option %q; it takes ttl and required", key)
		}
	}
	return config, nil
}

// httpBinaryStreamDef reads @httpBinaryStream({ contentType,
// responseHeaders? }) (D167): one config object whose contentType is the
// success body's media type and whose responseHeaders, when present, is a
// list of the header names the stream answers with.
// ir.ValidateHTTPBinaryStream holds both to what the SDKs read.
func httpBinaryStreamDef(args []any) (*ir.HTTPBinaryStreamDef, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("@httpBinaryStream takes exactly one config object")
	}
	cfg, ok := args[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("@httpBinaryStream config must be an object literal")
	}
	stream := &ir.HTTPBinaryStreamDef{}
	for _, key := range slices.Sorted(maps.Keys(cfg)) {
		switch value := cfg[key]; key {
		case "contentType":
			contentType, ok := value.(string)
			if !ok || contentType == "" {
				return nil, fmt.Errorf("@httpBinaryStream contentType must be a string literal, such as \"application/vnd.apache.arrow.stream\"")
			}
			stream.ContentType = contentType
		case "responseHeaders":
			list, ok := value.([]any)
			if !ok || len(list) == 0 {
				return nil, fmt.Errorf("@httpBinaryStream responseHeaders must be a non-empty list of header names")
			}
			for _, item := range list {
				header, ok := item.(string)
				if !ok || header == "" {
					return nil, fmt.Errorf("@httpBinaryStream responseHeaders must be a non-empty list of header names")
				}
				stream.ResponseHeaders = append(stream.ResponseHeaders, header)
			}
		default:
			return nil, fmt.Errorf("@httpBinaryStream has no option %q; it takes contentType and responseHeaders", key)
		}
	}
	if stream.ContentType == "" {
		return nil, fmt.Errorf("@httpBinaryStream requires a contentType, such as \"application/vnd.apache.arrow.stream\"")
	}
	if errs := ir.ValidateHTTPBinaryStream("@httpBinaryStream", stream); len(errs) > 0 {
		return nil, errs[0]
	}
	return stream, nil
}
