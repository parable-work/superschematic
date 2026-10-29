package tsrestgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
)

// The derivers of a TypeScript API (D74): deps.ts declares Derivers, a
// member per @deriver class, each an object of one method per derived
// field of its schemas, typed by the field through the database's
// TypeScript types, and the type of the implementation's createDerivers.
// A deriver's entrypoint hands each stale field to its method.

// DeriversFunc is the function an implementation of an API with derivers
// exports, of the generated DeriversConstructor's type, which a deriver's
// entrypoint calls with the API's Deps.
const DeriversFunc = "createDerivers"

// DeriverInfo is a deriver of the API: a member of the generated
// Derivers, of type Interface, with a method per derived field of its
// schemas.
type DeriverInfo struct {
	// Name is the @deriver class, Member its member of Derivers, the class
	// in camel case, and Interface the type of the member,
	// <Name>Deriver.
	Name      string
	Member    string
	Interface string

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

// DeriverMethod is a method of a deriver: the derived field Field of the
// engine type Schema, the method that answers it, the schema in camel
// case and the field in Pascal case (ticketCategory), and the fields it is
// derived from, the instruction and the model label of its config.
type DeriverMethod struct {
	Schema      string
	Field       string
	Method      string
	From        []string
	Instruction string
	Model       string
}

// Key is how the entrypoint finds the method of a stale field: its schema
// and its field, dotted.
func (m DeriverMethod) Key() string { return m.Schema + "." + m.Field }

// InputsType is the type of the method's item's inputs: each field it is
// derived from, with its own type, optional, since an input that holds no
// value is left out.
func (m DeriverMethod) InputsType() string {
	quoted := make([]string, len(m.From))
	for i, from := range m.From {
		quoted[i] = "'" + from + "'"
	}
	return "Readonly<Partial<Pick<" + m.Schema + ", " + strings.Join(quoted, " | ") + ">>>"
}

// ValueType is the type of the method's answer's value: the derived
// field's own type, without null.
func (m DeriverMethod) ValueType() string {
	return "NonNullable<" + m.Schema + "['" + m.Field + "']>"
}

// FromList is the fields the method's field is derived from, as its
// comment lists them.
func (m DeriverMethod) FromList() string {
	switch len(m.From) {
	case 0:
		return "nothing"
	case 1:
		return m.From[0]
	}
	return strings.Join(m.From[:len(m.From)-1], ", ") + " and " + m.From[len(m.From)-1]
}

// SetDerivers sets the API's derivers from its engine database, refusing
// two methods of one deriver that one name would take.
func (o *APIOutput) SetDerivers(engine *apigen.EngineInfo) error {
	o.Derivers = nil
	if engine == nil {
		return nil
	}
	for _, d := range engine.Derivers {
		info := DeriverInfo{
			Name:        d.Name,
			Member:      tsutil.ToCamelCase(d.Name),
			Interface:   tsutil.ToClassName(d.Name) + "Deriver",
			Subject:     d.Subject,
			Permissions: d.Permissions,
			Concurrency: d.Concurrency,
			BatchSize:   d.BatchSize,
		}
		methods := map[string]string{}
		for _, schema := range d.Schemas {
			info.Schemas = append(info.Schemas, schema.Name)
			for _, f := range schema.Fields {
				m := DeriverMethod{
					Schema:      schema.Name,
					Field:       f.Name,
					Method:      tsutil.ToCamelCase(schema.Name) + tsutil.ToClassName(f.Name),
					Instruction: f.Instruction,
					Model:       f.Model,
				}
				for _, from := range f.From {
					m.From = append(m.From, from.Name)
				}
				if other, clash := methods[m.Method]; clash {
					return fmt.Errorf("tsrestgen: deriver %s of %s fills %s and %s, which would both be its method %s", d.Name, o.SchemaName, other, m.Key(), m.Method)
				}
				methods[m.Method] = m.Key()
				info.Methods = append(info.Methods, m)
			}
		}
		o.Derivers = append(o.Derivers, info)
	}
	return nil
}

// HasDerivers reports whether the API declares a deriver, for which
// deps.ts declares Derivers and DeriversConstructor.
func (o *APIOutput) HasDerivers() bool { return len(o.Derivers) > 0 }

// Deriver returns the deriver named name, or nil.
func (o *APIOutput) Deriver(name string) *DeriverInfo {
	for i := range o.Derivers {
		if o.Derivers[i].Name == name {
			return &o.Derivers[i]
		}
	}
	return nil
}

// DatabaseTypeImports are the types deps.ts imports from the database's
// TypeScript types: the queues the API's workers handle and the engine
// types its derivers fill, sorted and each once.
func (o *APIOutput) DatabaseTypeImports() []string {
	out := o.WorkerQueues()
	for _, d := range o.Derivers {
		for _, schema := range d.Schemas {
			if !slices.Contains(out, schema) {
				out = append(out, schema)
			}
		}
	}
	slices.Sort(out)
	return out
}
