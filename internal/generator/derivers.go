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

// The derivers of an API (D74): each `@deriver` fills the Derived fields
// of engine types of the API's engine database. The API's build reads
// that database's IR to find each schema's Derived config and the types of
// its fields, which the generated Derivers interface types its methods
// with, and refuses what no deriver can run: a schema that composes no
// Derived, a deriver with no schema, a Go API's deriver, and a rules
// policy that admits no deriver's subject.

// deriversOf reads the derivers of the API api over types, the engine
// types of db, its engine database, sorted by name. policy is the engine's
// access policy, which each deriver must pass (checkDeriverPolicy).
func deriversOf(api *ir.Schema, db string, types []*ir.TypeDef, policy json.RawMessage) ([]apigen.DeriverInfo, error) {
	byName := map[string]*ir.TypeDef{}
	var derived []string
	for _, td := range types {
		byName[td.Name] = td
		if td.ComposesDerived() {
			derived = append(derived, td.Name)
		}
	}
	var out []apigen.DeriverInfo
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
			return nil, fmt.Errorf("generator: deriver %s of %s fills the derived fields of %s, and no type of %s composes Derived; compose Derived on a type, or remove the deriver (D74)", d.Name, api.Name, db, db)
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
				return nil, fmt.Errorf("generator: deriver %s of %s fills the derived fields of %s, which is no engine type of %s that composes Derived (its types that do: %s) (D74)", d.Name, api.Name, name, db, declared)
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
		if err := checkDeriverPolicy(info, db, policy); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	slices.SortFunc(out, func(a, b apigen.DeriverInfo) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
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

// refuseGoDerivers refuses a Go API that declares derivers: a deriver runs
// on a TypeScript or Rust API (D74).
func refuseGoDerivers(api *ir.Schema) error {
	if len(api.Derivers) == 0 {
		return nil
	}
	return fmt.Errorf("generator: %s declares deriver %s, and it is a Go API; %s (D74)", api.Name, api.Derivers[0].Name, stack.DeriverLanguages)
}
