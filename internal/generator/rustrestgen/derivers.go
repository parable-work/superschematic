package rustrestgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
)

// The derivers of a Rust API (D74): src/deps.rs declares a trait per
// @deriver class, with a method per derived field of its schemas, typed by
// the field through the database's Rust types, a struct of the inputs of
// each, and Derivers, which holds one value of each trait and which the
// implementation's create_derivers builds. A deriver's entrypoint hands
// each stale field to its method.

// DeriversFunc is the function an implementation of an API with derivers
// declares in its src/lib.rs, which a deriver's entrypoint calls with the
// API's Deps.
const DeriversFunc = "create_derivers"

// DeriverInfo is a deriver of the API: a field of the generated Derivers,
// of the trait Trait, with a method per derived field of its schemas.
type DeriverInfo struct {
	// Name is the @deriver class, Field its field of Derivers, the class
	// in snake case, and Trait its trait, <Name>Deriver.
	Name  string
	Field string
	Trait string

	// Subject and Permissions are the principal it acts as, and
	// Concurrency and BatchSize how it runs unless an environment changes
	// its concurrency (apigen.DeriverInfo).
	Subject     string
	Permissions []string
	Concurrency int
	BatchSize   int

	// Schemas are the engine types it fills, in order.
	Schemas []string

	// Methods are its methods, one per derived field of its schemas, in
	// the schemas' order and then by field.
	Methods []DeriverMethod
}

// DeriverMethod is a method of a deriver's trait: the derived field Field
// of the engine type Schema, the method that answers it, the schema and
// the field in snake case (ticket_category), the struct of its inputs,
// Inputs, the Rust type of its value, Value, through the API crate's
// re-export of its database's types, and the instruction and the model
// label of its config.
type DeriverMethod struct {
	Schema      string
	Field       string
	Method      string
	Inputs      string
	Value       string
	From        []DerivedInput
	Instruction string
	Model       string
}

// DerivedInput is a field a derived field is derived from: its name, the
// Inputs struct's field, and its Rust type, without Option.
type DerivedInput struct {
	Name     string
	RustName string
	Type     string
}

// Rename is the serde rename of an input whose Rust name is not its own.
func (i DerivedInput) Rename() string {
	if strings.TrimPrefix(i.RustName, "r#") == i.Name {
		return ""
	}
	return i.Name
}

// FromList is the fields the method's field is derived from, as its
// comment lists them.
func (m DeriverMethod) FromList() string {
	names := make([]string, len(m.From))
	for i, from := range m.From {
		names[i] = from.Name
	}
	switch len(names) {
	case 0:
		return "nothing"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// FieldTypes gives the Rust type of each field of an engine type, as the
// database's Rust types declare it holding a value, by field: rustgen's
// FieldTypes, qualified with DatabaseTypesModule.
type FieldTypes func(schema string) (map[string]string, error)

// SetDerivers sets the API's derivers from its engine database, their
// fields typed by types, refusing two methods of one deriver that one name
// would take.
func (o *APIOutput) SetDerivers(engine *apigen.EngineInfo, types FieldTypes) error {
	o.Derivers = nil
	if engine == nil {
		return nil
	}
	for _, d := range engine.Derivers {
		info := DeriverInfo{
			Name:        d.Name,
			Field:       rustutil.Identifier(rustutil.ToSnakeCase(d.Name), "deriver"),
			Trait:       rustutil.ToPascalCase(d.Name) + "Deriver",
			Subject:     d.Subject,
			Permissions: d.Permissions,
			Concurrency: d.Concurrency,
			BatchSize:   d.BatchSize,
		}
		methods := map[string]string{}
		for _, schema := range d.Schemas {
			info.Schemas = append(info.Schemas, schema.Name)
			fieldTypes, err := types(schema.Name)
			if err != nil {
				return fmt.Errorf("rustrestgen: deriver %s of %s: %w", d.Name, o.SchemaName, err)
			}
			for _, f := range schema.Fields {
				key := schema.Name + "." + f.Name
				m := DeriverMethod{
					Schema:      schema.Name,
					Field:       f.Name,
					Method:      rustutil.Identifier(rustutil.ToSnakeCase(schema.Name)+"_"+rustutil.ToSnakeCase(f.Name), "derive"),
					Inputs:      rustutil.ToPascalCase(schema.Name) + rustutil.ToPascalCase(f.Name) + "Inputs",
					Value:       fieldTypes[f.Name],
					Instruction: f.Instruction,
					Model:       f.Model,
				}
				if m.Value == "" {
					return fmt.Errorf("rustrestgen: deriver %s of %s fills %s, whose Rust type the database's types do not declare", d.Name, o.SchemaName, key)
				}
				for _, from := range f.From {
					m.From = append(m.From, DerivedInput{Name: from.Name, RustName: rustutil.Identifier(rustutil.ToSnakeCase(from.Name), "value"), Type: fieldTypes[from.Name]})
				}
				if other, clash := methods[m.Method]; clash {
					return fmt.Errorf("rustrestgen: deriver %s of %s fills %s and %s, which would both be its method %s", d.Name, o.SchemaName, other, key, m.Method)
				}
				methods[m.Method] = key
				info.Methods = append(info.Methods, m)
			}
		}
		o.Derivers = append(o.Derivers, info)
	}
	return nil
}

// HasDerivers reports whether the API declares a deriver, for which
// deps.rs declares Derivers and the scaffold create_derivers.
func (o *APIOutput) HasDerivers() bool { return o.Deps != nil && len(o.Derivers) > 0 }

// Deriver returns the deriver named name, or nil.
func (o *APIOutput) Deriver(name string) *DeriverInfo {
	for i := range o.Derivers {
		if o.Derivers[i].Name == name {
			return &o.Derivers[i]
		}
	}
	return nil
}

// DeriverSignature is the signature of method in its deriver's trait.
func (o *APIOutput) DeriverSignature(method DeriverMethod) string {
	return rustSignature("    ", "async fn "+method.Method, []string{"&self", "item: DerivedItem<" + method.Inputs + ">"}, "Result<DerivedAnswer<"+method.Value+">, BoxError>", ";")
}

// DeriverMethodSignature is the signature of method in the scaffold's impl
// of its deriver's trait.
func (s ImplementationScaffold) DeriverMethodSignature(method DeriverMethod) string {
	return rustFn("    ", "async fn "+method.Method, []string{"&self", "_item: DerivedItem<" + method.Inputs + ">"}, "Result<DerivedAnswer<"+method.Value+">, BoxError>")
}

// DeriverStruct is the struct the scaffold implements deriver's trait
// with, named after the API and the deriver: ShopCartTriageTickets.
func (s ImplementationScaffold) DeriverStruct(deriver DeriverInfo) string {
	return rustutil.ToPascalCase(s.SchemaName) + rustutil.ToPascalCase(deriver.Name)
}

// DerivedInputs are the methods whose inputs' structs deps.rs declares:
// one per derived field of the derivers' schemas, the first method of each,
// since two derivers of one schema share its structs.
func (o *APIOutput) DerivedInputs() []DeriverMethod {
	var out []DeriverMethod
	seen := map[string]bool{}
	for _, d := range o.Derivers {
		for _, m := range d.Methods {
			if !seen[m.Inputs] {
				seen[m.Inputs] = true
				out = append(out, m)
			}
		}
	}
	return out
}

// DeriverExports are the items of deps.rs a deriver's entrypoint and the
// implementation name: Derivers, DerivedItem, DerivedAnswer,
// derivation_answer, and each deriver's trait and each method's inputs.
func (o *APIOutput) DeriverExports() []string {
	if !o.HasDerivers() {
		return nil
	}
	names := []string{"derivation_answer", "DerivedAnswer", "DerivedItem", "Derivers"}
	for _, d := range o.Derivers {
		names = append(names, d.Trait)
	}
	for _, m := range o.DerivedInputs() {
		names = append(names, m.Inputs)
	}
	return names
}

// Key names the method's field: its schema and its field, dotted.
func (m DeriverMethod) Key() string { return m.Schema + "." + m.Field }
