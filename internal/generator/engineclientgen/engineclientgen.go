// Package engineclientgen writes a TypeScript module of typed wrappers over
// the engine's client (@superschematic/engine/client, D16) for one or more
// engine schemas (D49 in docs/DECISIONS.md).
//
// The engine's client is generic over schema names: a create takes
// unknown data, an operation unknown parameters. A schema's shape is known
// before it runs, from its document and the behavior declarations the
// binary registers, and this package writes it down per schema: the
// instance type's own fields, its behaviors' fields under each behavior's
// name, the create input with each behavior's create parameters, the
// update patch, each operation's parameters and result, the veto codes,
// and a wrapper object whose methods call the client with them.
//
// A behavior's config narrows what its declaration says: Workflow's states,
// Links' names, Dependencies' blocker schemas, Variants' types. The engine
// narrows the create parameters in TypeScript (createParamsSchema(config));
// internal/generator/behaviortypes narrows them again in Go for the core's
// behaviors, for this package and every other generator that renders
// behaviors, and the engine's test suite holds the two to each other
// through runtime/engine/testdata/client_codegen_parity.json, which this
// package writes. A behavior without a narrowing there, an extension's, is
// typed by its declaration.
//
// It is a command of its own (`superschematic engine-client`), not a step
// of a kind's pipeline. Of the pipeline's generators, only types renders
// behaviors, their fields as <T>Behaviors in each language (D58), with the
// same narrowings; the pipeline refuses a type that composes a behavior
// (D16) for every other.
package engineclientgen

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Options configures one module.
type Options struct {
	// Registry holds the behavior declarations: the binary's, so an
	// extension's behaviors are typed by their declarations.
	Registry *registry.Registry
	// Naming names the engine's npm package the module imports the client
	// from (Naming.EngineNpmPackage).
	Naming naming.Naming
}

// Generate writes the module for the schemas: one section per schema, in
// the order of their names.
func Generate(schemas []*ir.Schema, opts Options) ([]byte, error) {
	models, err := buildModels(schemas, opts.Registry)
	if err != nil {
		return nil, err
	}
	return render(models, opts.Naming.OrDefault())
}

// schemaModel is one engine schema as the module types it.
type schemaModel struct {
	schema *ir.Schema
	// name is the schema's name, which the engine's routes carry.
	name string
	// instance is the type that holds instances: the type named like the
	// schema, or its only type.
	instance *ir.TypeDef
	// base names the instance type's exports: Note, NoteFields, NotePatch.
	base string
	// client names the wrapper: NotesClient, and notesClient its factory.
	client string
	// behaviors are the type's behaviors in list order.
	behaviors []behaviortypes.Behavior
	// variants is the instance type's Variants config, when it composes it.
	variants *behaviortypes.Variants
}

// buildModels checks each schema as the engine would take it and builds
// its model.
func buildModels(schemas []*ir.Schema, reg *registry.Registry) ([]*schemaModel, error) {
	if len(schemas) == 0 {
		return nil, errors.New("engine-client: no schema to generate")
	}
	if reg == nil {
		return nil, errors.New("engine-client: no registry: the behavior declarations come from the binary's registry")
	}
	models := make([]*schemaModel, 0, len(schemas))
	seen := map[string]bool{}
	for _, schema := range schemas {
		model, err := buildModel(schema, reg)
		if err != nil {
			return nil, err
		}
		if seen[model.name] {
			return nil, fmt.Errorf("engine-client: schema %s is given twice", model.name)
		}
		seen[model.name] = true
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].name < models[j].name })
	return models, nil
}

func buildModel(schema *ir.Schema, reg *registry.Registry) (*schemaModel, error) {
	if schema.Name == "" {
		return nil, errors.New("engine-client: a schema needs a name, which the engine keys its versions by")
	}
	if schema.Kind != ir.SchemaKindGeneral {
		return nil, fmt.Errorf("engine-client: schema %s is of kind %s; the engine runs General schemas", schema.Name, schema.Kind)
	}
	if len(schema.Imports) > 0 {
		return nil, fmt.Errorf("engine-client: schema %s has imports; an engine schema reaches another schema's type through a link behavior", schema.Name)
	}
	if len(schema.OperationSets) > 0 {
		return nil, fmt.Errorf("engine-client: schema %s declares operations; the engine serves create, get, list, update and delete, and behaviors add their own", schema.Name)
	}
	instance := instanceTypeOf(schema)
	if instance == nil {
		return nil, fmt.Errorf("engine-client: schema %s has no instance type: declare a type named %s or exactly one type (found: %s)",
			schema.Name, schema.Name, strings.Join(behaviortypes.SortedKeys(schema.Types), ", "))
	}
	if err := checkFields(schema, instance); err != nil {
		return nil, err
	}
	model := &schemaModel{
		schema:   schema,
		name:     schema.Name,
		instance: instance,
		base:     instance.Name,
		client:   codegen.ToPascalCase(schema.Name) + "Client",
	}
	target := behaviortypes.Target{Schema: schema.Name, Base: model.base, Document: schema, Instance: instance}
	for _, ref := range instance.Behaviors {
		declaration, ok := reg.Behavior(ref.Name)
		if !ok {
			return nil, fmt.Errorf("engine-client: type %s of schema %s composes behavior %s, which this binary does not register", instance.Name, schema.Name, ref.Name)
		}
		if err := declaration.ValidateConfig(ref.Config); err != nil {
			return nil, fmt.Errorf("engine-client: type %s of schema %s: %w", instance.Name, schema.Name, err)
		}
		narrowed, err := behaviortypes.Narrow(declaration, ref.Config, target)
		if err != nil {
			return nil, fmt.Errorf("engine-client: type %s of schema %s: behavior %s: %w", instance.Name, schema.Name, ref.Name, err)
		}
		if narrowed.Variants != nil {
			model.variants = narrowed.Variants
		}
		model.behaviors = append(model.behaviors, behaviortypes.Behavior{Declaration: declaration, Narrowed: narrowed})
	}
	return model, nil
}

// instanceTypeOf is the engine's rule: the type named like the schema, or
// its only type.
func instanceTypeOf(schema *ir.Schema) *ir.TypeDef {
	if td, ok := schema.Types[schema.Name]; ok {
		return td
	}
	if len(schema.Types) == 1 {
		for _, td := range schema.Types {
			return td
		}
	}
	return nil
}

// checkFields refuses what the engine refuses in the types an instance can
// hold: a map field and a union-typed field, which the schema runtime does
// not validate.
func checkFields(schema *ir.Schema, instance *ir.TypeDef) error {
	var problems []error
	for _, name := range reachableTypes(schema, instance.Name) {
		for _, field := range schema.Types[name].Fields {
			if field == nil {
				continue
			}
			label := fmt.Sprintf("field %s.%s", name, field.Name)
			switch {
			case field.TypeRef.IsMap:
				problems = append(problems, fmt.Errorf("engine-client: schema %s: %s is a map, which the engine refuses", schema.Name, label))
			case schema.Unions[field.TypeRef.Name] != nil:
				problems = append(problems, fmt.Errorf("engine-client: schema %s: %s has the union type %s, which the engine refuses", schema.Name, label, field.TypeRef.Name))
			case !knownType(schema, field.TypeRef.Name):
				problems = append(problems, fmt.Errorf("engine-client: schema %s: %s has type %s, which is not a primitive, a scalar, or an enum or type of the schema", schema.Name, label, field.TypeRef.Name))
			}
		}
	}
	return errors.Join(problems...)
}

func knownType(schema *ir.Schema, name string) bool {
	switch name {
	case codegen.PrimitiveString, codegen.PrimitiveNumber, codegen.PrimitiveBoolean:
		return true
	}
	return schema.Types[name] != nil || schema.Enums[name] != nil || schema.Scalars[name] != nil
}

// reachableTypes lists the types an instance can hold, from the instance
// type through its fields, in the order they are reached.
func reachableTypes(schema *ir.Schema, root string) []string {
	var seen []string
	queue := []string{root}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		td := schema.Types[name]
		if td == nil || slices.Contains(seen, name) {
			continue
		}
		seen = append(seen, name)
		for _, field := range td.Fields {
			if field != nil && schema.Types[field.TypeRef.Name] != nil {
				queue = append(queue, field.TypeRef.Name)
			}
		}
	}
	return seen
}
