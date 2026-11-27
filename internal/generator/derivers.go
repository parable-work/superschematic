package generator

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
)

// The derivers of an API (D74, D146): each `@deriver` fills the Derived
// fields of types of the API's database, engine types through the engine
// or tables through their repositories. The API's build reads that
// database's IR to find each schema's Derived config and the types of its
// fields, which the generated Derivers interface types its methods with,
// and refuses what no deriver can run: a schema that composes no Derived,
// a deriver with no schema, one whose schemas are engine types and tables
// both, a Go API's deriver of engine types, a Rust API whose derivers fill
// both kinds, and a rules policy that admits no engine deriver's subject.

// deriversOf reads the derivers of the API api over the types of db, its
// database: engineTypes, its engine types, and tableTypes, its tables that
// implement Derived. It returns those that fill engine types and those
// that fill tables (whose Tables is set), each sorted by name. policy is
// the engine's access policy, which each deriver of engine types must pass
// (checkDeriverPolicy); a deriver of tables acts through the repositories
// as a behavior caller, which the core admits by the Derived config's
// permission alone.
func deriversOf(api *ir.Schema, db string, engineTypes, tableTypes []*ir.TypeDef, policy json.RawMessage) (engine, tables []apigen.DeriverInfo, err error) {
	byName := map[string]*ir.TypeDef{}
	isTable := map[string]bool{}
	var derived []string
	for _, td := range engineTypes {
		byName[td.Name] = td
		if td.ComposesDerived() {
			derived = append(derived, td.Name)
		}
	}
	for _, td := range tableTypes {
		if td.ComposesDerived() {
			byName[td.Name] = td
			isTable[td.Name] = true
			derived = append(derived, td.Name)
		}
	}
	slices.Sort(derived)
	for _, d := range api.Derivers {
		if d == nil {
			continue
		}
		if err := registry.CheckDeriver(d); err != nil {
			return nil, nil, fmt.Errorf("generator: deriver %s of %s: %w", d.Name, api.Name, err)
		}
		names := d.Schemas
		if len(names) == 0 {
			names = derived
		}
		if len(names) == 0 {
			return nil, nil, fmt.Errorf("generator: deriver %s of %s fills the derived fields of %s, and no type of %s composes Derived; compose Derived on a type, or remove the deriver (D74)", d.Name, api.Name, db, db)
		}
		info := apigen.DeriverInfo{
			Name:        d.Name,
			Subject:     ir.DeriverDeployableName(api.Name, d.Name),
			Concurrency: registry.DeriverConcurrency(d),
			BatchSize:   registry.DeriverBatchSize(d),
		}
		permissions := map[string]bool{}
		var ofEngine, ofTables []string
		for _, name := range names {
			td := byName[name]
			if td == nil || !td.ComposesDerived() {
				declared := "none"
				if len(derived) > 0 {
					declared = strings.Join(derived, ", ")
				}
				return nil, nil, fmt.Errorf("generator: deriver %s of %s fills the derived fields of %s, which is no engine type or table of %s that composes Derived (its types that do: %s) (D74, D146)", d.Name, api.Name, name, db, declared)
			}
			if isTable[name] {
				ofTables = append(ofTables, name)
			} else {
				ofEngine = append(ofEngine, name)
			}
			schema, err := derivedSchema(td)
			if err != nil {
				return nil, nil, fmt.Errorf("generator: deriver %s of %s: %w", d.Name, api.Name, err)
			}
			info.Schemas = append(info.Schemas, schema)
			permissions[td.Name] = true
			if schema.Permission != "" {
				permissions[schema.Permission] = true
			}
		}
		if len(ofEngine) > 0 && len(ofTables) > 0 {
			return nil, nil, fmt.Errorf("generator: deriver %s of %s fills the derived fields of %s, engine types of %s, and of %s, tables of it; a deriver fills engine types through the engine or tables through their repositories, so declare a deriver for each and name its schemas (D146)", d.Name, api.Name, strings.Join(ofEngine, ", "), db, strings.Join(ofTables, ", "))
		}
		for permission := range permissions {
			info.Permissions = append(info.Permissions, permission)
		}
		slices.Sort(info.Permissions)
		if len(ofTables) > 0 {
			info.Tables = true
			tables = append(tables, info)
			continue
		}
		if err := checkDeriverPolicy(info, db, policy); err != nil {
			return nil, nil, err
		}
		engine = append(engine, info)
	}
	byDeriver := func(a, b apigen.DeriverInfo) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(engine, byDeriver)
	slices.SortFunc(tables, byDeriver)
	return engine, tables, nil
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

// checkDeriverPolicy refuses a deriver the engine's access policy would
// refuse everything (D74). The permission policy admits it, since it holds
// each schema's name, and allowAll admits everyone. A rules policy decides
// by its first rule that matches, and the build cannot answer every
// question a rule asks, so it asks one thing of it: a rule that names the
// deriver's subject in subject and allows, before any rule that names it
// and refuses. The project writes that rule; the build does not.
func checkDeriverPolicy(d apigen.DeriverInfo, db string, policy json.RawMessage) error {
	var rules struct {
		Rules []map[string]json.RawMessage `json:"rules"`
	}
	if len(policy) == 0 || json.Unmarshal(policy, &rules) != nil || rules.Rules == nil {
		return nil // a shorthand: "permission" or "allowAll"
	}
	for _, rule := range rules.Rules {
		if !namesSubject(rule["subject"], d.Subject) {
			continue
		}
		var allow bool
		if raw, ok := rule["allow"]; ok && json.Unmarshal(raw, &allow) == nil && allow {
			return nil
		}
		if _, ok := rule["require"]; ok {
			return nil // the principal holds its schemas' names and the configs' permissions
		}
		break
	}
	return fmt.Errorf("generator: deriver %s runs as %s, and the policy of %s's engine (outputs.engine.policy) has no rule that names it and allows; add {\"subject\": %q, \"allow\": true} to its rules, before a rule that refuses it (D74)", d.Name, d.Subject, db, d.Subject)
}

// namesSubject reports whether a rule's subject, a name or a list of them,
// names subject.
func namesSubject(raw json.RawMessage, subject string) bool {
	if len(raw) == 0 {
		return false
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == subject
	}
	var list []string
	return json.Unmarshal(raw, &list) == nil && slices.Contains(list, subject)
}

// refuseGoEngineDerivers refuses a Go API one of whose derivers fills
// engine types: a Go API's deriver fills tables that implement Derived
// (D146), and one of engine types runs on a TypeScript or Rust API (D74).
func refuseGoEngineDerivers(api *ir.Schema, engine *apigen.EngineInfo) error {
	if engine == nil || len(engine.Derivers) == 0 {
		return nil
	}
	d := engine.Derivers[0]
	var names []string
	for _, schema := range d.Schemas {
		names = append(names, schema.Name)
	}
	return fmt.Errorf("generator: %s declares deriver %s, which fills the derived fields of %s, engine types of its database, and it is a Go API; %s", api.Name, d.Name, strings.Join(names, ", "), stack.DeriverLanguages)
}

// refuseGoHeldDerivers refuses a Go API's deriver of a table whose rows
// keep to an audience (@audience, D156). The deriver's context reaches
// every row, as a job's does (D180), but the Go behaviors' core reads the
// table's rows with its access columns and decodes effectiveAudience, a
// list, as one UUID, so its pull of the stale fields fails on every pass.
func refuseGoHeldDerivers(api *ir.Schema, derivers []apigen.DeriverInfo) error {
	for _, d := range derivers {
		for _, s := range d.Schemas {
			if s.Type != nil && s.Type.Audience != nil {
				return fmt.Errorf("generator: %s declares deriver %s, which fills the derived fields of %s, and %s keeps its rows to an audience (@audience); a Go API's deriver does not fill such a table yet, since the Go behaviors' core reads its effective audience as one UUID and fails every pull (D146, D180)", api.Name, d.Name, s.Name, s.Name)
			}
		}
	}
	return nil
}

// refuseRustMixedDerivers refuses a Rust API whose derivers fill engine
// types and tables both: deps.rs types the items of one kind of deriver,
// the engine's or the HTTP runtime's (D146).
func refuseRustMixedDerivers(api *ir.Schema, engine *apigen.EngineInfo, tables []apigen.DeriverInfo) error {
	if engine == nil || len(engine.Derivers) == 0 || len(tables) == 0 {
		return nil
	}
	return fmt.Errorf("generator: %s declares deriver %s, which fills engine types, and deriver %s, which fills tables, and it is a Rust API, whose derivers fill one kind or the other; declare them on two APIs (D146)", api.Name, engine.Derivers[0].Name, tables[0].Name)
}
