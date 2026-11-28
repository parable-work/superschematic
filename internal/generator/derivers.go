package generator

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The derivers of an API (D146): each `@deriver` fills the Derived fields
// of tables of the API's database that implement Derived, through their
// repositories. The API's build reads that database's IR to find each
// table's Derived config and the types of its fields, which the generated
// Derivers interface types its methods with, and refuses what no deriver
// can run: a table that composes no Derived, and a deriver with no table.

// deriversOf reads the derivers of the API api over tableTypes, the tables
// of db, its database, that implement Derived, sorted by name, each with
// Tables set. A deriver acts through the repositories as a behavior caller,
// which the core admits by the Derived config's permission alone.
func deriversOf(api *ir.Schema, db string, tableTypes []*ir.TypeDef) (tables []apigen.DeriverInfo, err error) {
	byName := map[string]*ir.TypeDef{}
	var derived []string
	for _, td := range tableTypes {
		if td.ComposesDerived() {
			byName[td.Name] = td
			derived = append(derived, td.Name)
		}
	}
	slices.Sort(derived)
	for _, d := range api.Derivers {
		if d == nil {
			continue
		}
		if err := registry.CheckDeriver(d); err != nil {
			return nil, fmt.Errorf("generator: deriver %s of %s: %w", d.Name, api.Name, err)
		}
		names := d.Schemas
		if len(names) == 0 {
			names = derived
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("generator: deriver %s of %s fills the derived fields of %s, and no table of %s implements Derived; implement Derived on a table, or remove the deriver (D146)", d.Name, api.Name, db, db)
		}
		info := apigen.DeriverInfo{
			Name:        d.Name,
			Subject:     ir.DeriverDeployableName(api.Name, d.Name),
			Concurrency: registry.DeriverConcurrency(d),
			BatchSize:   registry.DeriverBatchSize(d),
		}
		permissions := map[string]bool{}
		for _, name := range names {
			td := byName[name]
			if td == nil || !td.ComposesDerived() {
				declared := "none"
				if len(derived) > 0 {
					declared = strings.Join(derived, ", ")
				}
				return nil, fmt.Errorf("generator: deriver %s of %s fills the derived fields of %s, which is no table of %s that implements Derived (its tables that do: %s) (D146)", d.Name, api.Name, name, db, declared)
			}
			schema, err := derivedSchema(td)
			if err != nil {
				return nil, fmt.Errorf("generator: deriver %s of %s: %w", d.Name, api.Name, err)
			}
			info.Schemas = append(info.Schemas, schema)
			permissions[td.Name] = true
			if schema.Permission != "" {
				permissions[schema.Permission] = true
			}
		}
		for permission := range permissions {
			info.Permissions = append(info.Permissions, permission)
		}
		slices.Sort(info.Permissions)
		tables = append(tables, info)
	}
	slices.SortFunc(tables, func(a, b apigen.DeriverInfo) int { return strings.Compare(a.Name, b.Name) })
	return tables, nil
}

// derivedSchema reads td's Derived config with its fields' declarations.
func derivedSchema(td *ir.TypeDef) (apigen.DerivedSchema, error) {
	config, err := td.DerivedConfig()
	if err != nil {
		return apigen.DerivedSchema{}, err
	}
	fields := map[string]*ir.FieldDef{}
	for _, f := range td.Fields {
		if f != nil {
			fields[f.Name] = f
		}
	}
	schema := apigen.DerivedSchema{Name: td.Name, Type: td, Permission: config.Permission}
	for _, f := range config.Fields {
		field := fields[f.Name]
		if field == nil {
			return apigen.DerivedSchema{}, fmt.Errorf("behavior Derived of %s derives %s, which is no field of %s", td.Name, f.Name, td.Name)
		}
		out := apigen.DerivedField{Name: f.Name, Field: field, Instruction: f.Instruction, Model: f.Model}
		for _, from := range f.From {
			input := fields[from]
			if input == nil {
				return apigen.DerivedSchema{}, fmt.Errorf("behavior Derived of %s derives %s from %s, which is no field of %s", td.Name, f.Name, from, td.Name)
			}
			out.From = append(out.From, input)
		}
		schema.Fields = append(schema.Fields, out)
	}
	return schema, nil
}
