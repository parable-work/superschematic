package enginedoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/writer"
	ir "github.com/parable-work/superschematic/ir"
)

// Version is the document format this package writes.
const Version = 1

// Dir is the directory of the Go types module the document is written
// in, as identity/ holds the identity descriptor.
const Dir = "engine"

// namespaceName is the engine's rule for a namespace name: lowercase
// letters, digits and hyphens, starting with a letter, at most 63
// characters (runtime/engine/typescript/src/namespaces.ts).
var namespaceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Document is a DB schema's engine document.
type Document struct {
	// Version is Version.
	Version int `json:"version"`
	// Namespace is the engine namespace the schema's documents are defined
	// and published in: outputs.engine.namespace, else the DB service's
	// name.
	Namespace string `json:"namespace"`
	// Tools are the D11 options the engine writes its tool documents and
	// MCP tools with, as this binary registers them: the shape of the
	// EngineOptions.tools of @superschematic/engine.
	Tools Tools `json:"tools"`
	// Behaviors are the behaviors the engine types compose, by name, each
	// with what implements it.
	Behaviors []Behavior `json:"behaviors"`
	// Schemas holds one schema-file document per engine type, sorted by
	// the type's name: the JSON data form format --to=json writes.
	Schemas []json.RawMessage `json:"schemas"`
	// MetaSchema is the JSON Schema the documents load against: what
	// superschematic json-schema prints for the registry the build runs
	// with, so a document that carries an extension's decorators loads.
	MetaSchema json.RawMessage `json:"metaSchema"`
}

// Tools are an engine's tool options (D11): the invocation policy and the
// vendor keys of the tool documents, as the binary registers them, and the
// policy of each tool or behavior operation the deployment marks
// (outputs.engine.tools.invocation, D70).
type Tools struct {
	InvocationPolicy InvocationPolicy `json:"invocationPolicy"`
	// Invocation is the policy of an engine tool, by name, or of a
	// behavior operation, by <Behavior>.<operation>: the engine's
	// tools.invocation. Absent when the deployment marks none.
	Invocation map[string]string `json:"invocation,omitempty"`
	Keys       ToolKeys          `json:"keys"`
}

// BuiltinTools are the engine's own tools and the operations every schema
// has, which tools.invocation names bare: the Rust engine's BUILTIN_TOOLS.
var BuiltinTools = []string{
	"create", "get", "list", "update", "delete", "lookup",
	"listSchemas", "describeSchema", "defineSchema", "listBehaviors", "describeBehavior",
	"search", "getValue",
	"listNamespaces", "createNamespace", "archiveNamespace", "unarchiveNamespace",
}

// InvocationPolicy is the registry's ToolInvocationPolicy without its
// extension.
type InvocationPolicy struct {
	Key     string   `json:"key"`
	Values  []string `json:"values"`
	Default string   `json:"default"`
}

// ToolKeys are apigen.ToolKeys as the engine reads them.
type ToolKeys struct {
	Scalar     string          `json:"scalar"`
	Guidance   string          `json:"guidance"`
	Parameters []ToolParameter `json:"parameters"`
}

// ToolParameter is one key written at the root of every argument schema.
type ToolParameter struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

// Behavior is a behavior the engine types compose.
type Behavior struct {
	// Name is the registered name: bare for a core behavior,
	// "<extension>.<Name>" for an extension's.
	Name string `json:"name"`
	// Extension is the extension that declares it; absent for the core's.
	Extension string `json:"extension,omitempty"`
	// Package is the npm package whose implementation runs it:
	// @superschematic/engine or @superschematic/engine-workqueue for the
	// core's, the one the extension's registration names, else absent.
	Package string `json:"package,omitempty"`
	// Crate is the Rust crate that implements an extension's behavior, the
	// one its registration's Rust names (D58), which the project's engine
	// crate compiles in (internal/generator/projectengine); absent for the
	// core's, which the engine's own crates implement, and when the
	// registration names none.
	Crate string `json:"crate,omitempty"`
}

// Options configures Build.
type Options struct {
	// SchemaName is the DB service's name: the default namespace.
	SchemaName string
	// Namespace is outputs.engine.namespace; empty means SchemaName.
	Namespace string
	// Registry declares the behaviors, the invocation policy and the tool
	// hooks: the registry the build runs with. Required.
	Registry *registry.Registry
	// MetaSchema is the JSON Schema the documents load against. Nil is
	// schemafile.DefinitionFor(Registry).
	MetaSchema json.RawMessage
	// Dependencies are the loaded schemas of the service's dependencies,
	// by name, which a type the schema imports is read from.
	Dependencies map[string]*ir.Schema
	// Invocation is outputs.engine.tools.invocation, which Build checks
	// (CheckInvocation) and the document's tools carry.
	Invocation map[string]string
}

// Build returns the engine document of a DB schema, or nil when no type of
// the schema composes a behavior. It fails with every problem Problems
// finds, and when the namespace is not a namespace name or a type the
// engine types use cannot be found.
func Build(schema *ir.Schema, opts Options) (*Document, error) {
	engineTypes := codegen.EngineTypes(schema)
	if len(engineTypes) == 0 {
		return nil, nil
	}
	if problems := Problems(schema); len(problems) > 0 {
		return nil, fmt.Errorf("enginedoc: %s", strings.Join(problems, "; "))
	}
	reg := opts.Registry
	if reg == nil {
		return nil, fmt.Errorf("enginedoc: no registry")
	}

	namespace, err := Namespace(opts.SchemaName, opts.Namespace)
	if err != nil {
		return nil, fmt.Errorf("enginedoc: %w", err)
	}
	doc := &Document{Version: Version, Namespace: namespace}

	if doc.Tools, err = ToolsFor(schema, reg); err != nil {
		return nil, err
	}
	if err := CheckInvocation(opts.Invocation, engineTypes, reg); err != nil {
		return nil, err
	}
	if len(opts.Invocation) > 0 {
		doc.Tools.Invocation = maps.Clone(opts.Invocation)
	}

	doc.Behaviors = Behaviors(engineTypes, reg)

	r := resolver{schema: schema, deps: opts.Dependencies}
	for _, td := range engineTypes {
		file, err := r.document(td)
		if err != nil {
			return nil, fmt.Errorf("enginedoc: engine type %s: %w", td.Name, err)
		}
		data, err := writer.WriteWith(file, writer.FormatJSON, reg.Scalars())
		if err != nil {
			return nil, fmt.Errorf("enginedoc: engine type %s: %w", td.Name, err)
		}
		doc.Schemas = append(doc.Schemas, json.RawMessage(data))
	}

	doc.MetaSchema = opts.MetaSchema
	if doc.MetaSchema == nil {
		definition, err := schemafile.DefinitionFor(reg)
		if err != nil {
			return nil, fmt.Errorf("enginedoc: the meta-schema: %w", err)
		}
		doc.MetaSchema = definition
	}
	return doc, nil
}

// ToolsFor returns the D11 options the engine writes a schema's tools
// with, as the registry the build runs with registers them: its tool
// invocation policy, and the vendor keys its tool hooks leave, run over a
// tool set with no tools (apigen.ResolveToolKeys). The engine document
// holds them, and so does the project's engine crate
// (internal/generator/projectengine).
func ToolsFor(schema *ir.Schema, reg *registry.Registry) (Tools, error) {
	policy := reg.ToolInvocationPolicy()
	tools := Tools{InvocationPolicy: InvocationPolicy{Key: policy.Key, Values: policy.Values, Default: policy.Default}}
	keys, err := apigen.ResolveToolKeys(schema, reg.ToolHooks())
	if err != nil {
		return Tools{}, fmt.Errorf("enginedoc: %w", err)
	}
	tools.Keys = ToolKeys{Scalar: keys.Scalar, Guidance: keys.Guidance, Parameters: []ToolParameter{}}
	for _, kv := range keys.Parameters {
		tools.Keys.Parameters = append(tools.Keys.Parameters, ToolParameter{Key: kv.Key, Value: kv.Value})
	}
	return tools, nil
}

// CheckInvocation checks outputs.engine.tools.invocation as an engine
// that opens with it would, and at build time: each name is a built-in
// tool (BuiltinTools), or <Behavior>.<operation>, an operation the
// declaration of a behavior the engine types compose declares, and each
// value is one of the binary's invocation policy's. It names every
// problem.
func CheckInvocation(invocation map[string]string, engineTypes []*ir.TypeDef, reg *registry.Registry) error {
	if len(invocation) == 0 {
		return nil
	}
	composed := map[string]bool{}
	for _, b := range Behaviors(engineTypes, reg) {
		composed[b.Name] = true
	}
	policy := reg.ToolInvocationPolicy()
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(invocation)) {
		value := invocation[name]
		// A behavior's name may hold a dot (acme.Rating), an operation's
		// never does.
		if dot := strings.LastIndex(name, "."); dot >= 0 {
			behavior, operation := name[:dot], name[dot+1:]
			declared, found := reg.Behavior(behavior)
			switch {
			case !found || !composed[behavior]:
				problems = append(problems, fmt.Sprintf("%s names behavior %s, which no engine type composes", name, behavior))
				continue
			case !slices.ContainsFunc(declared.Operations, func(op registry.BehaviorOperation) bool { return op.Name == operation }):
				names := make([]string, len(declared.Operations))
				for i, op := range declared.Operations {
					names[i] = op.Name
				}
				problems = append(problems, fmt.Sprintf("%s names no operation of %s (%s)", name, behavior, strings.Join(names, ", ")))
				continue
			}
		} else if !slices.Contains(BuiltinTools, name) {
			problems = append(problems, fmt.Sprintf("%s is neither an engine tool (%s) nor a behavior's operation (<Behavior>.<operation>)", name, strings.Join(BuiltinTools, ", ")))
			continue
		}
		if !slices.Contains(policy.Values, value) {
			problems = append(problems, fmt.Sprintf("the policy of %s, %q, is not one of %s's values (%s)", name, value, policy.Key, strings.Join(policy.Values, ", ")))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("enginedoc: outputs.engine.tools.invocation: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Namespace returns the engine namespace of a DB service: configured, the
// value of outputs.engine.namespace, else the service's name. It fails
// when that is not a namespace name, naming the setting to change.
func Namespace(service, configured string) (string, error) {
	if configured != "" {
		if !namespaceName.MatchString(configured) {
			return "", fmt.Errorf("outputs.engine.namespace %q is not an engine namespace name: lowercase letters, digits and hyphens, starting with a letter, at most 63 characters", configured)
		}
		return configured, nil
	}
	if !namespaceName.MatchString(service) {
		return "", fmt.Errorf("the engine namespace is the service's name, %q, which is not an engine namespace name (lowercase letters, digits and hyphens, starting with a letter, at most 63 characters): set outputs.engine.namespace", service)
	}
	return service, nil
}

// JSON is the document as engine/<schema>.json and the types' constants
// hold it: two-space indented, with a trailing newline.
func (d *Document) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return nil, fmt.Errorf("enginedoc: encode the document: %w", err)
	}
	return buf.Bytes(), nil
}

// Behaviors lists the behaviors the engine types compose, sorted by name,
// each with what reg says implements it: the document's behaviors.
func Behaviors(engineTypes []*ir.TypeDef, reg *registry.Registry) []Behavior {
	seen := map[string]bool{}
	var out []Behavior
	for _, td := range engineTypes {
		for _, ref := range td.Behaviors {
			if seen[ref.Name] {
				continue
			}
			seen[ref.Name] = true
			b := Behavior{Name: ref.Name}
			if declared, ok := reg.Behavior(ref.Name); ok {
				b.Extension, b.Package, b.Crate = declared.Extension, declared.Package, declared.Rust.Crate
			}
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// resolver finds the definitions an engine type's closure names: in the
// schema, else in a dependency the schema imports them from.
type resolver struct {
	schema *ir.Schema
	deps   map[string]*ir.Schema
}

// document builds the schema-file document of root, an engine type: a
// General document named after it, holding it, the value types it
// reaches, and the enums and scalars their fields use, with no imports.
func (r resolver) document(root *ir.TypeDef) (*schemafile.Document, error) {
	file := &schemafile.Document{Name: root.Name, Kind: ir.SchemaKindGeneral, Types: map[string]*ir.TypeDef{}}
	type item struct {
		td    *ir.TypeDef
		owner *ir.Schema
	}
	queue := []item{{root, r.schema}}
	file.Types[root.Name] = engineType(root, true)
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		for _, fd := range it.td.Fields {
			name := fd.TypeRef.Name
			if codegen.IsLanguagePrimitive(name) || file.Types[name] != nil || file.Enums[name] != nil || file.Scalars[name] != nil {
				continue
			}
			owner, kind, def := r.lookup(it.owner, name)
			switch kind {
			case kindType:
				td := def.(*ir.TypeDef)
				if codegen.IsEngineType(td) {
					return nil, fmt.Errorf("field %s.%s holds type %s, which composes a behavior: link to its instances with the Links behavior (D16) instead of a field", it.td.Name, fd.Name, td.Name)
				}
				file.Types[name] = engineType(td, false)
				queue = append(queue, item{td, owner})
			case kindEnum:
				if file.Enums == nil {
					file.Enums = map[string]*ir.EnumDef{}
				}
				enum := *def.(*ir.EnumDef)
				enum.Owner, enum.Origin = "", ""
				file.Enums[name] = &enum
			case kindScalar:
				if file.Scalars == nil {
					file.Scalars = map[string]*ir.ScalarDef{}
				}
				file.Scalars[name] = engineScalar(def.(*ir.ScalarDef))
			case kindUnion:
				return nil, fmt.Errorf("field %s.%s has the union type %s, which the engine's schema runtime does not validate", it.td.Name, fd.Name, name)
			default:
				return nil, fmt.Errorf("field %s.%s has type %s, which is not a primitive, nor a type, an enum or a scalar of %s or of a dependency the build loaded", it.td.Name, fd.Name, name, r.schema.Name)
			}
		}
	}
	return file, nil
}

type defKind int

const (
	kindNone defKind = iota
	kindType
	kindEnum
	kindScalar
	kindUnion
)

// lookup finds name in owner, the schema whose type names it, else in the
// schema being built, else in a loaded dependency, in name order.
func (r resolver) lookup(owner *ir.Schema, name string) (*ir.Schema, defKind, any) {
	schemas := []*ir.Schema{owner}
	if owner != r.schema {
		schemas = append(schemas, r.schema)
	}
	for _, dep := range sortedNames(r.deps) {
		if r.deps[dep] != nil && r.deps[dep] != owner {
			schemas = append(schemas, r.deps[dep])
		}
	}
	for _, s := range schemas {
		if td := s.Types[name]; td != nil {
			return s, kindType, td
		}
		if enum := s.Enums[name]; enum != nil {
			return s, kindEnum, enum
		}
		if scalar := s.Scalars[name]; scalar != nil {
			return s, kindScalar, scalar
		}
		if union := s.Unions[name]; union != nil {
			return s, kindUnion, union
		}
	}
	return nil, kindNone, nil
}

// engineType is a copy of td as an engine document holds it: a General
// document's struct, its fields flattened and naming no base, trait or
// table, and its behaviors on the instance type alone.
func engineType(td *ir.TypeDef, instance bool) *ir.TypeDef {
	copied := *td
	copied.Owner = ""
	copied.Origin = ""
	copied.Role = ir.RoleEmbeddedStruct
	copied.JsonField = false
	// Fields arrive flattened: the base class and the traits the type
	// names are not in the document.
	copied.Extends = ""
	copied.RawHeritage = nil
	copied.Implements = nil
	if !instance {
		copied.Behaviors = nil
	}
	copied.Fields = make([]*ir.FieldDef, len(td.Fields))
	for i, fd := range td.Fields {
		field := *fd
		field.InheritedFrom = ""
		// How a table stores the field; an engine instance is JSON.
		field.JsonField = false
		copied.Fields[i] = &field
	}
	return &copied
}

// engineScalar is a scalar as an engine document declares it. A scalar the
// core's catalog holds is declared by its name and language primitive, as
// format --to=json writes it: the engine validates it by its own copy of
// the catalog, whatever the document says. Any other, a scalar of the
// schema or of an extension's catalog, which the engine does not know, is
// the whole definition the loader read.
func engineScalar(def *ir.ScalarDef) *ir.ScalarDef {
	if _, builtin := registry.CoreScalars().Scalar(def.Name); builtin {
		return &ir.ScalarDef{Name: def.Name, LanguagePrimitive: def.LanguagePrimitive}
	}
	copied := *def
	return &copied
}
