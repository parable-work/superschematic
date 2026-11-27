package apigen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/goutil"
)

// The derivers of a Go API (D146): deps.go declares Derivers, a field per
// @deriver class, each an interface of one method per derived field of its
// tables, typed by the field through the database's Go types, a struct of
// the inputs of each, and DeriversConstructor, the signature of the
// implementation's NewDerivers. A deriver's entrypoint hands each stale
// field to its method. A Go API's deriver fills tables that implement
// Derived alone: one of engine types is refused (stack.DeriverLanguages).

// DeriversFunc is the function an implementation of an API with derivers
// declares, of the generated DeriversConstructor's type, which a deriver's
// entrypoint calls with the API's Deps.
const DeriversFunc = "NewDerivers"

// GoDeriver is a deriver of a Go API: a field of the generated Derivers,
// Field, of the interface Interface, <Name>Deriver, with a method per
// derived field of its tables.
type GoDeriver struct {
	Name      string
	Field     string
	Interface string

	// Subject and Permissions are the caller it acts as, and Concurrency
	// and BatchSize how it runs unless an environment changes its
	// concurrency (DeriverInfo).
	Subject     string
	Permissions []string
	Concurrency int
	BatchSize   int

	// Schemas are the tables it fills, in order.
	Schemas []string

	// Methods are its methods, one per derived field of its tables, in the
	// tables' order and then by field.
	Methods []GoDeriverMethod
}

// GoDeriverMethod is a method of a Go deriver: the derived field Field of
// the table Schema, the method that answers it, the table and the field in
// Pascal case (TicketCategory), the struct of its inputs, Inputs, the Go
// type of its value, Value, through the database's Go types, the fields
// it is derived from, and the instruction and the model label of its
// config.
type GoDeriverMethod struct {
	Schema      string
	Field       string
	Method      string
	Inputs      string
	Value       string
	From        []GoDerivedInput
	Instruction string
	Model       string
}

// GoDerivedInput is a field a derived field is derived from: its name, the
// Inputs struct's field, and its Go type, nil when it holds no value.
type GoDerivedInput struct {
	Name   string
	GoName string
	Type   string
}

// Struct is the type the scaffold implements the deriver's interface with:
// its field in camel case, then Deriver (triageTicketsDeriver).
func (d GoDeriver) Struct() string {
	if d.Field == "" {
		return "deriver"
	}
	return strings.ToLower(d.Field[:1]) + d.Field[1:] + "Deriver"
}

// Key names the method's field: its table and its field, dotted.
func (m GoDeriverMethod) Key() string { return m.Schema + "." + m.Field }

// FromList is the fields the method's field is derived from, as its
// comment lists them.
func (m GoDeriverMethod) FromList() string {
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

// GoFieldTypes gives the Go type of each field of a table, as the
// database's Go types declare it, qualified with the package deps.go
// imports them as, by field: typegen's FieldTypes.
type GoFieldTypes func(schema string) (map[string]string, error)

// SetDerivers sets the API's derivers, its derivers of tables, their
// fields typed by types, refusing two methods of one deriver that one name
// would take.
func (o *APIOutput) SetDerivers(tables []DeriverInfo, types GoFieldTypes) error {
	o.GoDerivers = nil
	for _, d := range tables {
		info := GoDeriver{
			Name:        d.Name,
			Field:       goutil.GoPublicIdentifier(d.Name),
			Interface:   goutil.GoPublicIdentifier(d.Name) + "Deriver",
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
				return fmt.Errorf("apigen: deriver %s of %s: %w", d.Name, o.SchemaName, err)
			}
			for _, f := range schema.Fields {
				key := schema.Name + "." + f.Name
				m := GoDeriverMethod{
					Schema:      schema.Name,
					Field:       f.Name,
					Method:      goutil.GoPublicIdentifier(schema.Name) + goutil.GoPublicIdentifier(f.Name),
					Inputs:      goutil.GoPublicIdentifier(schema.Name) + goutil.GoPublicIdentifier(f.Name) + "Inputs",
					Value:       strings.TrimPrefix(fieldTypes[f.Name], "*"),
					Instruction: f.Instruction,
					Model:       f.Model,
				}
				if m.Value == "" {
					return fmt.Errorf("apigen: deriver %s of %s fills %s, whose Go type the database's types do not declare", d.Name, o.SchemaName, key)
				}
				for _, from := range f.From {
					t := fieldTypes[from.Name]
					if t == "" {
						return fmt.Errorf("apigen: deriver %s of %s fills %s from %s, whose Go type the database's types do not declare", d.Name, o.SchemaName, key, from.Name)
					}
					// An input that holds no value is left out: a pointer,
					// unless its type is nil already.
					if !strings.HasPrefix(t, "*") && !strings.HasPrefix(t, "[]") && !strings.HasPrefix(t, "map[") {
						t = "*" + t
					}
					m.From = append(m.From, GoDerivedInput{Name: from.Name, GoName: goutil.GoPublicIdentifier(from.Name), Type: t})
				}
				if other, clash := methods[m.Method]; clash {
					return fmt.Errorf("apigen: deriver %s of %s fills %s and %s, which would both be its method %s", d.Name, o.SchemaName, other, key, m.Method)
				}
				methods[m.Method] = key
				info.Methods = append(info.Methods, m)
			}
		}
		o.GoDerivers = append(o.GoDerivers, info)
	}
	return nil
}

// HasDerivers reports whether the API declares a deriver, for which
// deps.go declares Derivers and DeriversConstructor.
func (o *APIOutput) HasDerivers() bool { return len(o.GoDerivers) > 0 }

// GoDeriver returns the deriver named name, or nil.
func (o *APIOutput) GoDeriver(name string) *GoDeriver {
	for i := range o.GoDerivers {
		if o.GoDerivers[i].Name == name {
			return &o.GoDerivers[i]
		}
	}
	return nil
}

// DeriversNameDatabaseTypes reports whether a deriver's method names a
// type of the database's Go types, which deps.go then imports.
func (o *APIOutput) DeriversNameDatabaseTypes() bool {
	alias := o.Deps.DatabaseTypesAlias + "."
	if alias == "." {
		return false
	}
	for _, d := range o.GoDerivers {
		for _, m := range d.Methods {
			if strings.Contains(m.Value, alias) {
				return true
			}
			for _, from := range m.From {
				if strings.Contains(from.Type, alias) {
					return true
				}
			}
		}
	}
	return false
}

// DerivedInputs are the methods whose inputs' structs deps.go declares:
// one per derived field of the derivers' tables, the first method of each,
// since two derivers of one table share its structs.
func (o *APIOutput) DerivedInputs() []GoDeriverMethod {
	var out []GoDeriverMethod
	seen := map[string]bool{}
	for _, d := range o.GoDerivers {
		for _, m := range d.Methods {
			if !seen[m.Inputs] {
				seen[m.Inputs] = true
				out = append(out, m)
			}
		}
	}
	return out
}
