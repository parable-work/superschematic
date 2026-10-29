package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	validator "github.com/santhosh-tekuri/jsonschema/v6"

	ir "github.com/parable-work/superschematic/ir"
)

// BehaviorSpec registers one behavior: code that adds fields, operations,
// checks and storage to a type when an engine runs the schema (D16 in
// docs/DECISIONS.md). The declaration is one JSON document that sits beside
// the Go package registering it, which embeds it; its shape is
// BehaviorDeclaration. See docs/extension-model.md section 3.16.
type BehaviorSpec struct {
	// Extension is the registering extension's Name(); "" for core. A core
	// behavior has a bare name ("Workflow"), an extension's is
	// "<extension>.<Name>".
	Extension string
	// Package is the npm package whose implementation runs the behavior in
	// an engine (the core's are "@superschematic/engine" and
	// "@superschematic/engine-workqueue"), so the behaviors command can
	// write each package's declarations into that package (--package).
	// Optional; when set it is an npm package name.
	Package string
	// Rust is the Rust crate whose implementation runs an extension's
	// behavior in the Rust engine (D58): a crate implementing the engine's
	// Behavior trait, which the project's engine crate, the one the
	// projectengine generator writes for a schema that composes the
	// behavior, depends on and registers. Optional; the core's behaviors
	// are the engine's own crates and name none.
	Rust BehaviorRust
	// Declaration is the behavior's JSON declaration.
	Declaration json.RawMessage
}

// BehaviorRust names the Rust crate that implements an extension's
// behavior (D58) and the value the project's engine registers.
type BehaviorRust struct {
	// Crate is the crate's Cargo package name, which the project's engine
	// crate depends on: a letter, then letters, digits, '-' and '_', at
	// most 64 characters.
	Crate string
	// Path is the crate's directory relative to the project, the parent of
	// the schemas root, which the [paths] keys of superschematic.toml are
	// relative to as well: slash-separated and clean, and not absolute.
	// The project's engine crate depends on the crate by this path, as acme
	// points at crates/behaviors.
	Path string
	// Register is the Rust expression of the behavior the project's engine
	// registers (BehaviorSet::with): a path into the crate, as
	// acme_behaviors::Rating names acme's unit struct, or a call with no
	// arguments, as acme_behaviors::Rating::new() would be. It starts with
	// the crate's name as Rust spells it (Ident).
	Register string
}

// Declared reports whether the spec names a crate.
func (r BehaviorRust) Declared() bool {
	return r != BehaviorRust{}
}

// Ident is the crate's name as Rust source spells it: the Cargo name with
// '-' folded to '_'.
func (r BehaviorRust) Ident() string {
	return strings.ReplaceAll(r.Crate, "-", "_")
}

// BehaviorDeclaration is the shape of a behavior's declaration document.
type BehaviorDeclaration struct {
	// Name is the behavior's registered name, the one a type lists.
	Name string `json:"name"`
	// Description says what the behavior adds to a type.
	Description string `json:"description,omitempty"`
	// ConfigSchema is the JSON Schema of the config a type gives the
	// behavior. Absent means the behavior takes no config.
	ConfigSchema json.RawMessage `json:"configSchema,omitempty"`
	// CreateParamsSchema is the JSON Schema of the parameters a create
	// gives the behavior for the new instance, which an engine passes to
	// its initialize: an object schema that sets "additionalProperties",
	// false or a schema, so no parameter goes unchecked. Absent means a
	// create gives it none.
	CreateParamsSchema json.RawMessage `json:"createParamsSchema,omitempty"`
	// Requires names behaviors a type must also list to list this one.
	Requires []string `json:"requires,omitempty"`
	// Conflicts names behaviors a type that lists this one may not list.
	Conflicts []string `json:"conflicts,omitempty"`
	// Fields are the fields the behavior adds to a type.
	Fields []BehaviorField `json:"fields,omitempty"`
	// Operations are the operations the behavior adds to a type, beside
	// the create, get, list, update and delete every schema has.
	Operations []BehaviorOperation `json:"operations,omitempty"`
	// Signals are the messages about an instance an engine delivers to
	// the event streams open on it and keeps nowhere (D77): a client's
	// typing indicator, a draft being written.
	Signals []BehaviorSignal `json:"signals,omitempty"`
	// PreconditionSchema is the JSON Schema of the entry a caller sends
	// for the behavior in the preconditions of an update, a delete or an
	// operation: an object schema that sets "additionalProperties": false,
	// as a paramsSchema does. An engine checks a caller's entry against it
	// and hands it to the behavior's guard. Absent, the behavior takes
	// none.
	PreconditionSchema json.RawMessage `json:"preconditionSchema,omitempty"`
	// Vetoes are the codes the behavior's refusals carry. A client
	// branches on a veto's code, read beside the behavior's name; an
	// engine refuses a veto whose code its behavior does not list.
	Vetoes []BehaviorVeto `json:"vetoes,omitempty"`
}

// BehaviorVeto is one code a behavior's vetoes carry.
type BehaviorVeto struct {
	// Code is lowercase snake case, at most 64 characters, unique within
	// the behavior.
	Code        string `json:"code"`
	Description string `json:"description,omitempty"`
}

// BehaviorField is a field a behavior adds to a type. It carries a name, a
// description and whether it is volatile and per reader, no type: a
// field's type can depend on the behavior's config. An instance keeps a
// behavior's fields under the behavior's name, apart from the type's own
// and every other behavior's, so a field collides with nothing (D16,
// amended: a behavior's fields sit under its name); a string names one by
// BehaviorFieldPath.
type BehaviorField struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Volatile is true for a field that can move without an event of its
	// own instance: computed when the instance is read, from other
	// instances, the clock or the config. No event records such a move, so
	// a rule that hears a field's changes refuses one (D56).
	Volatile bool `json:"volatile,omitempty"`
	// PerReader is true for a volatile field whose value is the caller's,
	// not the instance's, as Comments.unread is (D77): a read computes it
	// for its caller, and no event patch, create event or reference move
	// records it. Only a volatile field is per reader.
	PerReader bool `json:"perReader,omitempty"`
}

// BehaviorFieldPath is a behavior field's qualified name: the behavior's
// name, a dot, the field's, as Workflow.status and acme.Rating.count name
// theirs. A display's summaryFields, a list's filter and a rollup's field
// name a behavior's field by it; an own field's name holds no dot.
func BehaviorFieldPath(behavior, field string) string {
	return behavior + "." + field
}

// BehaviorOperation is an operation a behavior adds to a type.
type BehaviorOperation struct {
	// Name is the operation name, camelCase.
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// ParamsSchema is the JSON Schema of the operation's parameters: an
	// object schema, as an MCP tool's parameters are, that sets
	// "additionalProperties": false, so the parameters are exactly the ones
	// it declares and none reaches an engine's handler without its guards
	// seeing it.
	ParamsSchema json.RawMessage `json:"paramsSchema"`
	// ResultSchema is the JSON Schema of the operation's result.
	ResultSchema json.RawMessage `json:"resultSchema"`
	// Writes is true for an operation that changes stored state.
	Writes bool `json:"writes,omitempty"`
	// Scope is what the operation runs on: "instance" (or empty, the
	// default), one instance, which an engine's call names by id;
	// "schema", the schema as a whole, with no instance; or "namespace"
	// (D77), the behavior's own tables across every schema of a namespace
	// that composes it, with no instance and no schema, which an engine
	// calls by the behavior's name. A namespace operation is never beside
	// an instance and appends no event, so it sets neither Beside nor Log.
	Scope string `json:"scope,omitempty"`
	// Beside is true for an instance operation that writes beside the
	// instance (D77): only its behavior's own tables and columns, so an
	// engine moves no sequence of the instance, runs no other behavior's
	// guard, and appends an operation event of no sequence. A behavior
	// that declares one declares every field volatile.
	Beside bool `json:"beside,omitempty"`
	// Log, false, says a beside operation appends no event at all, for a
	// write only its writer reads back. Absent, it appends one.
	Log *bool `json:"log,omitempty"`
	// Asks, "read", says the access policy is asked read for the operation
	// though it writes (D77): a beside operation with "log": false that
	// writes only its caller's own state, a read marker or a follow, which a
	// caller who may only read the instance may call. Empty, a writing
	// operation asks write.
	Asks string `json:"asks,omitempty"`
	// InvocationPolicy is the MCP invocation policy of the operation's
	// tool: one of the values of the registry's ToolInvocationPolicy.
	// Empty means the policy's default.
	InvocationPolicy string `json:"invocationPolicy,omitempty"`
}

// The accesses a signal declares: who may send it.
const (
	// SignalAccessRead lets a caller who may read the schema send it.
	SignalAccessRead = "read"
	// SignalAccessWrite lets a caller who may write the schema send it.
	SignalAccessWrite = "write"
	// SignalAccessOperation lets only the behavior's own operations send
	// it, through their context.
	SignalAccessOperation = "operation"
)

// BehaviorSignal is a signal a behavior declares (D77). An engine checks a
// sender's data against ParamsSchema, at most 4096 bytes of JSON, asks a
// caller's of the access policy with the operation "signal:<name>", and
// delivers it to the event streams opened with signals, stored nowhere.
type BehaviorSignal struct {
	// Name is camelCase, unique among the behavior's signals.
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// ParamsSchema is the JSON Schema of the signal's data.
	ParamsSchema json.RawMessage `json:"paramsSchema"`
	// Access is who may send it: SignalAccessRead, SignalAccessWrite or
	// SignalAccessOperation.
	Access string `json:"access"`
}

// Behavior is a registered behavior: its declaration, the extension that
// registered it and its compiled config schema.
type Behavior struct {
	BehaviorDeclaration
	// Extension is the registering extension's Name(); "" for core.
	Extension string
	// Package is the npm package that implements it; "" when its spec
	// names none.
	Package string
	// Rust is the Rust crate that implements it; the zero value when its
	// spec names none.
	Rust BehaviorRust

	config         *validator.Schema
	configRequired bool
}

// builtinOperations are the operations an engine serves on a schema's
// instance type itself (D16): every type has create, get, list, update and
// delete, and one with a unique field lookup. A behavior operation cannot
// take one of these names.
var builtinOperations = []string{"create", "get", "list", "update", "delete", "lookup"}

// The scopes an operation declares: an instance, the default, its schema
// as a whole, or a namespace (D77).
const (
	OperationScopeInstance  = "instance"
	OperationScopeSchema    = "schema"
	OperationScopeNamespace = "namespace"
)

// OperationAsksRead is the one value of an operation's Asks.
const OperationAsksRead = "read"

var (
	behaviorBareName      = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	behaviorOperationName = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
	behaviorFieldName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	behaviorVetoCode      = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	// npmPackageName is a lowercase npm package name, scoped or not.
	npmPackageName = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)
	// cargoPackageName is a Cargo package name: a letter, then letters,
	// digits, '-' and '_', at most 64 characters.
	cargoPackageName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	// rustRegisterExpr is a Rust path of two segments or more, optionally
	// called with no arguments: a unit struct, a constant, or a function
	// or constructor that takes none.
	rustRegisterExpr = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(::[A-Za-z_][A-Za-z0-9_]*)+(\(\))?$`)
)

const behaviorNameDescriptor = "a letter A-Z followed by letters and digits"

// RegisterBehavior parses and checks one behavior declaration and adds it.
// It rejects a declaration that does not decode or has keys the shape does
// not have; a malformed name; a core name that is not bare or an extension
// name whose prefix is not the registering extension; a duplicate name; a
// config, params or result schema that does not compile; a params schema
// that is not an object schema or does not set "additionalProperties":
// false, the engine's rule, so no parameter reaches a handler without its
// guards seeing it; a create params schema that does not compile, is not
// an object schema or sets "additionalProperties" to neither false nor a
// schema; an operation name that is not camelCase, repeats, or is one an
// engine gives every schema (create, get, list, update, delete); a scope
// other than "instance" or "schema"; a signal whose name is not camelCase
// or repeats, whose paramsSchema is absent or does not compile, or whose
// access is not "read", "write" or "operation"; a field name that is not an
// identifier or repeats; a preconditionSchema that does not compile, is
// not an object schema or does not set "additionalProperties": false; a
// veto code that is not lowercase snake case of at most 64 characters, or
// repeats; a Package that is not an npm package name; and a Rust crate on a
// core behavior, or one whose crate is not a Cargo package name, whose
// path is empty, absolute or not clean, or whose register expression is
// not a path into the crate (checkBehaviorRust). Finalize checks what needs
// the whole registry: requires and conflicts name registered behaviors,
// each operation's invocation policy is a value of the registry's policy,
// and behaviors that name one Rust crate name it at one path.
func (r *Registry) RegisterBehavior(spec BehaviorSpec) error {
	var decl BehaviorDeclaration
	dec := json.NewDecoder(bytes.NewReader(spec.Declaration))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decl); err != nil {
		return fmt.Errorf("registry: behavior declaration of %s: %w", ownerName(spec.Extension), err)
	}
	if dec.More() {
		return fmt.Errorf("registry: behavior declaration of %s: trailing data after the JSON document", ownerName(spec.Extension))
	}
	if err := r.registrable("behavior " + decl.Name); err != nil {
		return err
	}
	if r.registering != "" && spec.Extension != r.registering {
		return fmt.Errorf("registry: behavior %q names extension %q, but extension %s is registering it", decl.Name, spec.Extension, r.registering)
	}
	if err := checkBehaviorName(decl.Name, spec.Extension); err != nil {
		return err
	}
	if spec.Package != "" && (len(spec.Package) > 214 || !npmPackageName.MatchString(spec.Package)) {
		return fmt.Errorf("registry: behavior %s package %q is not an npm package name", decl.Name, spec.Package)
	}
	if err := checkBehaviorRust(decl.Name, spec.Extension, spec.Rust); err != nil {
		return err
	}
	if _, dup := r.behaviors[decl.Name]; dup {
		return fmt.Errorf("registry: behavior %q is already registered", decl.Name)
	}
	b := Behavior{BehaviorDeclaration: decl, Extension: spec.Extension, Package: spec.Package, Rust: spec.Rust}
	if len(decl.ConfigSchema) > 0 {
		compiled, err := compileSchema(decl.ConfigSchema, "superschematic://behaviors/"+decl.Name+"/config.json")
		if err != nil {
			return fmt.Errorf("registry: behavior %s configSchema: %w", decl.Name, err)
		}
		b.config = compiled
		// A config the schema rejects when it is {} must be written out;
		// the data-form JSON Schema then requires the key.
		b.configRequired = compiled.Validate(map[string]any{}) != nil
	}
	if err := checkBehaviorCreateParams(decl); err != nil {
		return err
	}
	if err := checkBehaviorFields(decl); err != nil {
		return err
	}
	if err := checkBehaviorOperations(decl); err != nil {
		return err
	}
	if err := checkBehaviorSignals(decl); err != nil {
		return err
	}
	if err := checkBehaviorPrecondition(decl); err != nil {
		return err
	}
	if err := checkBehaviorVetoes(decl); err != nil {
		return err
	}
	r.behaviors[decl.Name] = b
	r.noteExtension(spec.Extension)
	return nil
}

func ownerName(extension string) string {
	if extension == "" {
		return "the core"
	}
	return "extension " + extension
}

// checkBehaviorName holds a core behavior to a bare name and an
// extension's to "<extension>.<Name>".
func checkBehaviorName(name, extension string) error {
	if name == "" {
		return fmt.Errorf("registry: behavior declaration of %s has no name", ownerName(extension))
	}
	if extension == "" {
		if !behaviorBareName.MatchString(name) {
			return fmt.Errorf("registry: core behavior name %q is malformed: a core behavior is named by %s", name, behaviorNameDescriptor)
		}
		return nil
	}
	prefix, bare, qualified := strings.Cut(name, ".")
	if !qualified || prefix != extension {
		return fmt.Errorf("registry: behavior name %q of extension %s must be %s.<Name>", name, extension, extension)
	}
	if !behaviorBareName.MatchString(bare) {
		return fmt.Errorf("registry: behavior name %q is malformed: after %s. comes %s", name, extension, behaviorNameDescriptor)
	}
	return nil
}

// checkBehaviorRust holds a behavior's Rust crate to what the project's
// engine crate can depend on and register (D58): only an extension's
// behavior names one, since the core's are the engine's own crates; the
// crate is a Cargo package name; its path is relative to the project,
// slash-separated and clean; and the register expression is a Rust path
// into the crate, called with no arguments or not.
func checkBehaviorRust(name, extension string, rust BehaviorRust) error {
	if !rust.Declared() {
		return nil
	}
	if extension == "" {
		return fmt.Errorf("registry: core behavior %s names Rust crate %q: the core's behaviors are the engine's own crates", name, rust.Crate)
	}
	if !cargoPackageName.MatchString(rust.Crate) {
		return fmt.Errorf("registry: behavior %s Rust crate %q is not a Cargo package name: a letter, then letters, digits, '-' and '_', at most 64 characters", name, rust.Crate)
	}
	switch {
	case rust.Path == "":
		return fmt.Errorf("registry: behavior %s Rust crate %s has no path: the crate's directory relative to the project, the parent of the schemas root", name, rust.Crate)
	case strings.HasPrefix(rust.Path, "/") || filepath.IsAbs(rust.Path):
		return fmt.Errorf("registry: behavior %s Rust crate %s path %q is an absolute path: it is relative to the project, the parent of the schemas root", name, rust.Crate, rust.Path)
	case strings.Contains(rust.Path, "\\") || path.Clean(rust.Path) != rust.Path:
		return fmt.Errorf("registry: behavior %s Rust crate %s path %q is not a clean slash-separated path (%s)", name, rust.Crate, rust.Path, path.Clean(strings.ReplaceAll(rust.Path, "\\", "/")))
	}
	head, _, _ := strings.Cut(rust.Register, "::")
	if !rustRegisterExpr.MatchString(rust.Register) || head != rust.Ident() {
		return fmt.Errorf("registry: behavior %s Rust register %q is not a path into crate %s: %s::<Name>, or a call of one with no arguments", name, rust.Register, rust.Crate, rust.Ident())
	}
	return nil
}

// checkBehaviorCreateParams holds a create params schema to an object
// schema whose "additionalProperties" is false or a schema. Unlike an
// operation's parameters, a create's may be keyed by names the config
// gives (a link's name), so the schema may admit further keys, but it
// checks each one's value.
func checkBehaviorCreateParams(decl BehaviorDeclaration) error {
	if len(decl.CreateParamsSchema) == 0 {
		return nil
	}
	if _, err := compileSchema(decl.CreateParamsSchema, "superschematic://behaviors/"+decl.Name+"/createParams.json"); err != nil {
		return fmt.Errorf("registry: behavior %s createParamsSchema: %w", decl.Name, err)
	}
	var params struct {
		Type                 any `json:"type"`
		AdditionalProperties any `json:"additionalProperties"`
	}
	if err := json.Unmarshal(decl.CreateParamsSchema, &params); err != nil || params.Type != "object" {
		return fmt.Errorf("registry: behavior %s createParamsSchema must be an object schema (\"type\": \"object\")", decl.Name)
	}
	if _, isSchema := params.AdditionalProperties.(map[string]any); params.AdditionalProperties != false && !isSchema {
		return fmt.Errorf("registry: behavior %s createParamsSchema must set \"additionalProperties\": false or a schema, so no create parameter goes unchecked", decl.Name)
	}
	return nil
}

func checkBehaviorFields(decl BehaviorDeclaration) error {
	seen := map[string]bool{}
	for _, field := range decl.Fields {
		if !behaviorFieldName.MatchString(field.Name) {
			return fmt.Errorf("registry: behavior %s field name %q is not an identifier", decl.Name, field.Name)
		}
		if seen[field.Name] {
			return fmt.Errorf("registry: behavior %s declares field %q twice", decl.Name, field.Name)
		}
		seen[field.Name] = true
		if field.PerReader && !field.Volatile {
			return fmt.Errorf("registry: behavior %s field %s is perReader but not volatile: a value each reader reads its own of is computed at a read", decl.Name, field.Name)
		}
	}
	return nil
}

func checkBehaviorOperations(decl BehaviorDeclaration) error {
	seen := map[string]bool{}
	for _, op := range decl.Operations {
		if !behaviorOperationName.MatchString(op.Name) {
			return fmt.Errorf("registry: behavior %s operation name %q is not camelCase", decl.Name, op.Name)
		}
		if slices.Contains(builtinOperations, op.Name) {
			return fmt.Errorf("registry: behavior %s operation %q has the name of an operation the engine serves on a schema (%s)", decl.Name, op.Name, strings.Join(builtinOperations, ", "))
		}
		if seen[op.Name] {
			return fmt.Errorf("registry: behavior %s declares operation %q twice", decl.Name, op.Name)
		}
		seen[op.Name] = true
		if op.Scope != "" && op.Scope != OperationScopeInstance && op.Scope != OperationScopeSchema && op.Scope != OperationScopeNamespace {
			return fmt.Errorf("registry: behavior %s operation %s scope %q is not %q, %q or %q", decl.Name, op.Name, op.Scope, OperationScopeInstance, OperationScopeSchema, OperationScopeNamespace)
		}
		if op.Scope == OperationScopeNamespace {
			if err := checkNamespaceOperation(decl, op); err != nil {
				return err
			}
		} else if err := checkBesideOperation(decl, op); err != nil {
			return err
		}
		if err := checkAsks(decl, op); err != nil {
			return err
		}
		for _, s := range []struct {
			key    string
			schema json.RawMessage
		}{{"paramsSchema", op.ParamsSchema}, {"resultSchema", op.ResultSchema}} {
			if len(s.schema) == 0 {
				return fmt.Errorf("registry: behavior %s operation %s has no %s", decl.Name, op.Name, s.key)
			}
			if _, err := compileSchema(s.schema, "superschematic://behaviors/"+decl.Name+"/operations/"+op.Name+"/"+s.key+".json"); err != nil {
				return fmt.Errorf("registry: behavior %s operation %s %s: %w", decl.Name, op.Name, s.key, err)
			}
		}
		object, closed := closedObjectSchema(op.ParamsSchema)
		if !object {
			return fmt.Errorf("registry: behavior %s operation %s paramsSchema must be an object schema (\"type\": \"object\")", decl.Name, op.Name)
		}
		if !closed {
			return fmt.Errorf("registry: behavior %s operation %s paramsSchema must set \"additionalProperties\": false, so its parameters are exactly the ones it declares", decl.Name, op.Name)
		}
	}
	return nil
}

// checkBehaviorSignals holds each signal to its rule (D77): a camelCase
// name declared once, a paramsSchema that compiles, and an access of read,
// write or operation.
func checkBehaviorSignals(decl BehaviorDeclaration) error {
	seen := map[string]bool{}
	for _, signal := range decl.Signals {
		if !behaviorOperationName.MatchString(signal.Name) {
			return fmt.Errorf("registry: behavior %s signal name %q is not camelCase", decl.Name, signal.Name)
		}
		if seen[signal.Name] {
			return fmt.Errorf("registry: behavior %s declares signal %q twice", decl.Name, signal.Name)
		}
		seen[signal.Name] = true
		if len(signal.ParamsSchema) == 0 {
			return fmt.Errorf("registry: behavior %s signal %s has no paramsSchema", decl.Name, signal.Name)
		}
		if _, err := compileSchema(signal.ParamsSchema, "superschematic://behaviors/"+decl.Name+"/signals/"+signal.Name+"/paramsSchema.json"); err != nil {
			return fmt.Errorf("registry: behavior %s signal %s paramsSchema: %w", decl.Name, signal.Name, err)
		}
		switch signal.Access {
		case SignalAccessRead, SignalAccessWrite, SignalAccessOperation:
		default:
			return fmt.Errorf("registry: behavior %s signal %s access %q is not %q, %q or %q", decl.Name, signal.Name, signal.Access, SignalAccessRead, SignalAccessWrite, SignalAccessOperation)
		}
	}
	return nil
}

// Logged reports whether the operation appends an event when it writes:
// every operation but a beside one that declares "log": false.
func (op BehaviorOperation) Logged() bool {
	return op.Log == nil || *op.Log
}

// checkNamespaceOperation holds a namespace operation to its rule (D77): it
// runs on no instance, so it is never beside one, and it appends no event,
// so it says nothing of one.
func checkNamespaceOperation(decl BehaviorDeclaration, op BehaviorOperation) error {
	if op.Beside {
		return fmt.Errorf("registry: behavior %s operation %s has scope %q and is beside the instance: a namespace operation runs on no instance", decl.Name, op.Name, OperationScopeNamespace)
	}
	if op.Log != nil {
		return fmt.Errorf("registry: behavior %s operation %s has scope %q and sets \"log\": a namespace operation appends no event", decl.Name, op.Name, OperationScopeNamespace)
	}
	return nil
}

// AsksRead reports whether the access policy is asked read for the
// operation though it writes (D77, "asks": "read").
func (op BehaviorOperation) AsksRead() bool {
	return op.Asks == OperationAsksRead
}

// checkAsks holds an operation's asks to its rule (D77): only "read", and
// only on a beside operation that appends no event, which writes nothing
// anyone but its caller reads.
func checkAsks(decl BehaviorDeclaration, op BehaviorOperation) error {
	if op.Asks == "" {
		return nil
	}
	if op.Asks != OperationAsksRead {
		return fmt.Errorf("registry: behavior %s operation %s asks %q: the only value is %q", decl.Name, op.Name, op.Asks, OperationAsksRead)
	}
	if !op.Beside || op.Logged() {
		return fmt.Errorf("registry: behavior %s operation %s asks %q and is not beside the instance with \"log\": false: only an operation that writes its caller's own state, and appends no event, asks read", decl.Name, op.Name, OperationAsksRead)
	}
	return nil
}

// checkBesideOperation holds a beside operation to its rule (D77): it
// writes, on an instance, and its behavior's fields are all volatile, since
// no event records their moves; only a beside operation appends no event.
func checkBesideOperation(decl BehaviorDeclaration, op BehaviorOperation) error {
	if !op.Beside {
		if !op.Logged() {
			return fmt.Errorf("registry: behavior %s operation %s sets \"log\": false and is not beside the instance: only a beside operation appends no event", decl.Name, op.Name)
		}
		return nil
	}
	if !op.Writes {
		return fmt.Errorf("registry: behavior %s operation %s is beside the instance and does not write: a beside operation declares \"writes\": true", decl.Name, op.Name)
	}
	if op.Scope == OperationScopeSchema {
		return fmt.Errorf("registry: behavior %s operation %s is beside the instance and has scope %q: a beside operation runs on an instance", decl.Name, op.Name, OperationScopeSchema)
	}
	for _, field := range decl.Fields {
		if !field.Volatile {
			return fmt.Errorf("registry: behavior %s field %s is not volatile, and operation %s is beside the instance: a behavior with a beside operation declares every field volatile, since no event records its moves", decl.Name, field.Name, op.Name)
		}
	}
	return nil
}

// closedObjectSchema reports whether a JSON Schema is an object schema
// ("type": "object") and whether it sets "additionalProperties": false.
func closedObjectSchema(raw json.RawMessage) (object, closed bool) {
	var schema struct {
		Type                 any `json:"type"`
		AdditionalProperties any `json:"additionalProperties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil || schema.Type != "object" {
		return false, false
	}
	return true, schema.AdditionalProperties == false
}

// checkBehaviorPrecondition holds a preconditionSchema to the rule of a
// paramsSchema: it compiles, and it is a closed object schema, so a guard
// reads exactly the members it declares.
func checkBehaviorPrecondition(decl BehaviorDeclaration) error {
	if len(decl.PreconditionSchema) == 0 {
		return nil
	}
	if _, err := compileSchema(decl.PreconditionSchema, "superschematic://behaviors/"+decl.Name+"/precondition.json"); err != nil {
		return fmt.Errorf("registry: behavior %s preconditionSchema: %w", decl.Name, err)
	}
	object, closed := closedObjectSchema(decl.PreconditionSchema)
	if !object {
		return fmt.Errorf("registry: behavior %s preconditionSchema must be an object schema (\"type\": \"object\")", decl.Name)
	}
	if !closed {
		return fmt.Errorf("registry: behavior %s preconditionSchema must set \"additionalProperties\": false, so its members are exactly the ones it declares", decl.Name)
	}
	return nil
}

// checkBehaviorVetoes holds each veto code to lowercase snake case of at
// most 64 characters, listed once.
func checkBehaviorVetoes(decl BehaviorDeclaration) error {
	seen := map[string]bool{}
	for _, veto := range decl.Vetoes {
		if len(veto.Code) > 64 || !behaviorVetoCode.MatchString(veto.Code) {
			return fmt.Errorf("registry: behavior %s veto code %q is not lowercase snake case of at most 64 characters", decl.Name, veto.Code)
		}
		if seen[veto.Code] {
			return fmt.Errorf("registry: behavior %s declares veto code %q twice", decl.Name, veto.Code)
		}
		seen[veto.Code] = true
	}
	return nil
}

// checkBehaviorReferences is Finalize's pass over the behaviors: requires
// and conflicts name registered behaviors other than the declaring one, no
// behavior both requires and conflicts with another, every operation's
// invocation policy is a value of the policy in force, and behaviors that
// name one Rust crate name it at one path, since the project's engine
// crate depends on each crate once.
func (r *Registry) checkBehaviorReferences() error {
	policy := r.ToolInvocationPolicy()
	crates := map[string]Behavior{}
	for _, b := range r.Behaviors() {
		if b.Rust.Declared() {
			if first, seen := crates[b.Rust.Crate]; seen && first.Rust.Path != b.Rust.Path {
				return fmt.Errorf("registry: behaviors %s and %s name Rust crate %s at two paths, %q and %q", first.Name, b.Name, b.Rust.Crate, first.Rust.Path, b.Rust.Path)
			} else if !seen {
				crates[b.Rust.Crate] = b
			}
		}
		for _, list := range []struct {
			key   string
			names []string
		}{{"requires", b.Requires}, {"conflicts", b.Conflicts}} {
			for _, name := range list.names {
				if name == b.Name {
					return fmt.Errorf("registry: behavior %s %s itself", b.Name, list.key)
				}
				if _, ok := r.behaviors[name]; !ok {
					return fmt.Errorf("registry: behavior %s %s %q, which is not a registered behavior (registered: %s)", b.Name, list.key, name, strings.Join(r.BehaviorNames(), ", "))
				}
			}
		}
		for _, name := range b.Requires {
			if slices.Contains(b.Conflicts, name) {
				return fmt.Errorf("registry: behavior %s both requires and conflicts with %s", b.Name, name)
			}
		}
		for _, op := range b.Operations {
			if op.InvocationPolicy != "" && !slices.Contains(policy.Values, op.InvocationPolicy) {
				return fmt.Errorf("registry: behavior %s operation %s invocationPolicy %q is not a value of the %s policy (%s)", b.Name, op.Name, op.InvocationPolicy, policy.Key, strings.Join(policy.Values, ", "))
			}
		}
	}
	return nil
}

// Behavior returns the behavior registered under name.
func (r *Registry) Behavior(name string) (Behavior, bool) {
	b, ok := r.behaviors[name]
	return b, ok
}

// Behaviors returns every registered behavior, sorted by name: the order
// the data-form JSON Schema lists them in.
func (r *Registry) Behaviors() []Behavior {
	out := make([]Behavior, 0, len(r.behaviors))
	for _, b := range r.behaviors {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// BehaviorNames returns the registered behavior names, sorted.
func (r *Registry) BehaviorNames() []string {
	names := make([]string, 0, len(r.behaviors))
	for name := range r.behaviors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ConfigRequired reports whether a type that lists the behavior must give
// a config: the behavior has a config schema that rejects {}.
func (b Behavior) ConfigRequired() bool {
	return b.configRequired
}

// ValidateConfig checks a type's config of the behavior against its
// config schema. An absent config is checked as {}, the value a config of
// {} is stored as. A behavior without a config schema takes no config.
func (b Behavior) ValidateConfig(config json.RawMessage) error {
	instance := any(map[string]any{})
	if len(config) > 0 {
		decoded, err := validator.UnmarshalJSON(bytes.NewReader(config))
		if err != nil {
			return fmt.Errorf("behavior %s config: %w", b.Name, err)
		}
		instance = decoded
	}
	if b.config == nil {
		if m, ok := instance.(map[string]any); ok && len(m) == 0 {
			return nil
		}
		return fmt.Errorf("behavior %s takes no config", b.Name)
	}
	if err := b.config.Validate(instance); err != nil {
		return fmt.Errorf("behavior %s config: %w", b.Name, err)
	}
	return nil
}

// behaviorDecorator is @behavior(name, config?) from @superschematic/schema,
// the TypeScript authoring form of a type's behaviors. Each use appends one
// ir.BehaviorRef to the type, so several on one class apply in source
// order. The name must be a registered behavior and the config, canonical
// as the data forms store it, must pass its config schema; verify checks
// the list as a whole. lookup and names read the registry when Apply runs,
// after every extension has registered.
func behaviorDecorator(lookup func(string) (Behavior, bool), names func() []string) DecoratorSpec {
	return DecoratorSpec{
		Name: "behavior", Packages: []string{pkgSchema}, Target: TargetType,
		Apply: func(n Node, args []any, _ Site) error {
			if len(args) == 0 || len(args) > 2 {
				return fmt.Errorf("@behavior takes a behavior name and an optional config")
			}
			name, ok := args[0].(string)
			if !ok {
				return ArgErrorf(0, "@behavior takes the behavior's name as a string literal")
			}
			behavior, ok := lookup(name)
			if !ok {
				known := "none are registered"
				if registered := names(); len(registered) > 0 {
					known = "registered: " + strings.Join(registered, ", ")
				}
				return ArgErrorf(0, "type %s: behavior %q is not a registered behavior (%s)", n.Type.Name, name, known)
			}
			for _, listed := range n.Type.Behaviors {
				if listed.Name == name {
					return fmt.Errorf("type %s lists behavior %s twice", n.Type.Name, name)
				}
			}
			refs := []ir.BehaviorRef{{Name: name}}
			if len(args) == 2 {
				raw, err := json.Marshal(args[1])
				if err != nil {
					return ArgErrorf(1, "behavior %s config: %v", name, err)
				}
				refs[0].Config = raw
			}
			if err := ir.CanonicalizeBehaviors(refs); err != nil {
				return err
			}
			if err := behavior.ValidateConfig(refs[0].Config); err != nil {
				if len(args) == 2 {
					return ArgErrorf(1, "type %s: %s", n.Type.Name, err)
				}
				return fmt.Errorf("type %s: %w", n.Type.Name, err)
			}
			n.Type.Behaviors = append(n.Type.Behaviors, refs[0])
			return nil
		},
	}
}
