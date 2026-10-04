package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	// Declaration is the behavior's JSON declaration.
	Declaration json.RawMessage
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
	// Requires names behaviors a type must also list to list this one.
	Requires []string `json:"requires,omitempty"`
	// Conflicts names behaviors a type that lists this one may not list.
	Conflicts []string `json:"conflicts,omitempty"`
	// Fields are the fields the behavior adds to a type.
	Fields []BehaviorField `json:"fields,omitempty"`
	// Operations are the operations the behavior adds to a type, beside
	// the create, get, list, update and delete every schema has.
	Operations []BehaviorOperation `json:"operations,omitempty"`
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

// BehaviorField is a field a behavior adds to a type. It carries a name and
// a description only: a field's type can depend on the behavior's config,
// and the loader needs only the name to refuse a collision with the type's
// own fields or another behavior's.
type BehaviorField struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
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
	// default), one instance, which an engine's call names by id; or
	// "schema", the schema as a whole, with no instance.
	Scope string `json:"scope,omitempty"`
	// InvocationPolicy is the MCP invocation policy of the operation's
	// tool: one of the values of the registry's ToolInvocationPolicy.
	// Empty means the policy's default.
	InvocationPolicy string `json:"invocationPolicy,omitempty"`
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

	config         *validator.Schema
	configRequired bool
}

// builtinOperations are the operations an engine gives every schema's
// instance type (D16). A behavior operation cannot take one of these names.
var builtinOperations = []string{"create", "get", "list", "update", "delete"}

// The scopes an operation declares: an instance, the default, or its
// schema as a whole.
const (
	OperationScopeInstance = "instance"
	OperationScopeSchema   = "schema"
)

var (
	behaviorBareName      = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	behaviorOperationName = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
	behaviorFieldName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	behaviorVetoCode      = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	// npmPackageName is a lowercase npm package name, scoped or not.
	npmPackageName = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)
)

const behaviorNameDescriptor = "a letter A-Z followed by letters and digits"

// RegisterBehavior parses and checks one behavior declaration and adds it.
// It rejects a declaration that does not decode or has keys the shape does
// not have; a malformed name; a core name that is not bare or an extension
// name whose prefix is not the registering extension; a duplicate name; a
// config, params or result schema that does not compile; a params schema
// that is not an object schema or does not set "additionalProperties":
// false, the engine's rule, so no parameter reaches a handler without its
// guards seeing it; an operation name that is not camelCase, repeats, or
// is one an engine gives every schema (create, get, list, update, delete);
// a scope other than "instance" or "schema"; a field name that is not an
// identifier or repeats; a preconditionSchema that does not compile, is
// not an object schema or does not set "additionalProperties": false; a
// veto code that is not lowercase snake case of at most 64 characters, or
// repeats; and a Package that is not an npm package name.
// Finalize checks
// what needs the whole registry: requires and conflicts name registered
// behaviors, and each operation's invocation policy is a value of the
// registry's policy.
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
	if _, dup := r.behaviors[decl.Name]; dup {
		return fmt.Errorf("registry: behavior %q is already registered", decl.Name)
	}
	b := Behavior{BehaviorDeclaration: decl, Extension: spec.Extension, Package: spec.Package}
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
	if err := checkBehaviorFields(decl); err != nil {
		return err
	}
	if err := checkBehaviorOperations(decl); err != nil {
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
			return fmt.Errorf("registry: behavior %s operation %q has the name of an operation every schema has (%s)", decl.Name, op.Name, strings.Join(builtinOperations, ", "))
		}
		if seen[op.Name] {
			return fmt.Errorf("registry: behavior %s declares operation %q twice", decl.Name, op.Name)
		}
		seen[op.Name] = true
		if op.Scope != "" && op.Scope != OperationScopeInstance && op.Scope != OperationScopeSchema {
			return fmt.Errorf("registry: behavior %s operation %s scope %q is not %q or %q", decl.Name, op.Name, op.Scope, OperationScopeInstance, OperationScopeSchema)
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
// behavior both requires and conflicts with another, and every operation's
// invocation policy is a value of the policy in force.
func (r *Registry) checkBehaviorReferences() error {
	policy := r.ToolInvocationPolicy()
	for _, b := range r.Behaviors() {
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
