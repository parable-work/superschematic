package verify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// blueprintBehavior is the name of the core's Blueprint (D149).
const blueprintBehavior = "Blueprint"

// blueprintStep is one step of a Blueprint template as the build reads it.
type blueprintStep struct {
	After []string                   `json:"after"`
	When  *blueprintWhen             `json:"when"`
	Data  map[string]json.RawMessage `json:"data"`
}

// blueprintWhen is a step's when as the build reads it.
type blueprintWhen struct {
	Field    string          `json:"field"`
	Includes json.RawMessage `json:"includes"`
}

// checkBlueprintConfig holds the config of a Blueprint use the table td
// implements to the tables of its database (D149), in the engine's words
// where it has them, each problem `type <T>: behavior Blueprint config:
// <reason>` and the first reported. These are the rules that read the
// child table, which a type's define check on the core cannot see: the
// child is a table of the database keyed by a UUID or a string, since the
// stamp draws each child's key; it composes no Blueprint; its relation
// parentLink is a to-one relation to td; keyField is a string field of
// it; its Constants keeps keyField and every copied field; it composes
// Dependencies, whose blockers are its own rows, when a step comes after
// another; and the fields copyFields and each step's data name, and the
// relations copyLinks names, are its own. A when's includes reads a list
// field of td, which the core's spec does not say. from is refused, as the
// core refuses it: a table's relation pins no revision to read its steps
// from. A config the configSchema refuses is reported by
// checkTypeBehaviors and left alone here.
func checkBlueprintConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		Schema     string          `json:"schema"`
		ParentLink string          `json:"parentLink"`
		KeyField   string          `json:"keyField"`
		Steps      json.RawMessage `json:"steps"`
		From       json.RawMessage `json:"from"`
		CopyFields []string        `json:"copyFields"`
		CopyLinks  []string        `json:"copyLinks"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, fmt.Sprintf(format, args...))
	}
	if len(config.From) > 0 {
		fail("from reads its steps from the revision a pinned link records, and a table's relation pins none: a table's Blueprint takes inline steps (D149)")
		return
	}
	keys, steps, err := orderedSteps(config.Steps)
	if err != nil {
		return
	}
	at := "schema " + config.Schema
	child := schema.Types[config.Schema]
	if child == nil || child.Role != ir.RoleDBTable || child.JsonField {
		fail("%s is not a table of %s", at, schema.Name)
		return
	}
	if why := blueprintKey(child); why != "" {
		fail("%s %s, and a stamp draws each child's key, a UUID: key the child table by an Identity.UUID or a string", at, why)
		return
	}
	for _, use := range child.TableBehaviors() {
		if use.Name == blueprintBehavior {
			fail("%s composes Blueprint: a child cannot stamp children of its own", at)
			return
		}
	}
	link := fieldNamedIn(child, config.ParentLink)
	if link == nil || link.Owned() || !sqlutil.IsTableRelation(schema, link) || link.TypeRef.IsArray || link.TypeRef.Name != td.Name {
		fail("%s has no to-one relation %s to %s, through which its children point at their parent", at, config.ParentLink, td.Name)
		return
	}
	childFields := map[string]string{}
	for _, column := range behaviorlayout.SpecFields(schema, child) {
		childFields[column.Field] = column.Type
	}
	if kind, ok := childFields[config.KeyField]; !ok || kind != "string" || blueprintList(child, config.KeyField) {
		fail("keyField %s is not a string field of %s", config.KeyField, config.Schema)
		return
	}
	constant := append([]string{config.KeyField}, config.CopyFields...)
	kept, composes := childConstants(child)
	if !composes {
		fail("%s does not compose Constants, so the fields each stamp sets in a child could change after it: compose Constants with fields %s", at, strings.Join(constant, ", "))
		return
	}
	var loose []string
	for _, field := range constant {
		if !slices.Contains(kept, field) {
			loose = append(loose, field)
		}
	}
	if len(loose) > 0 {
		fail("%s's Constants does not list %s, which each stamp sets and nothing may change after", at, strings.Join(loose, ", "))
		return
	}
	if slices.ContainsFunc(steps, func(step blueprintStep) bool { return len(step.After) > 0 }) {
		blockers, composes := dependenciesSchemas(child)
		if !composes {
			fail("%s does not compose Dependencies, so a step's after cannot block its child", at)
			return
		}
		if !slices.Contains(blockers, config.Schema) {
			fail("%s's Dependencies takes blockers of %s, not of %s, which its siblings are", at, strings.Join(blockers, ", "), config.Schema)
			return
		}
	}
	for _, field := range config.CopyFields {
		if _, ok := childFields[field]; !ok {
			fail("copyFields: %s is not a field of %s", field, config.Schema)
			return
		}
	}
	for i, key := range keys {
		step := steps[i]
		for _, field := range sortedTypeNames(step.Data) {
			if _, ok := childFields[field]; !ok {
				fail("step %s: data sets %s, which is not a field of %s", key, field, config.Schema)
				return
			}
		}
		if step.When != nil && len(step.When.Includes) > 0 && !blueprintList(td, step.When.Field) {
			fail("steps: step %s: when includes reads %s as a list, and %s's %s is not one", key, step.When.Field, td.Name, step.When.Field)
			return
		}
	}
	for _, name := range config.CopyLinks {
		own := fieldNamedIn(td, name)
		copied := fieldNamedIn(child, name)
		if copied == nil || copied.Owned() || !sqlutil.IsTableRelation(schema, copied) || copied.TypeRef.IsArray {
			fail("copyLinks: %s has no to-one relation %s", at, name)
			return
		}
		if own == nil || own.TypeRef.Name != copied.TypeRef.Name {
			target := "none"
			if own != nil {
				target = own.TypeRef.Name
			}
			fail("copyLinks: %s's relation %s points at %s, and this type's at %s", at, name, copied.TypeRef.Name, target)
			return
		}
	}
}

// orderedSteps reads a template's steps in the order it writes them.
func orderedSteps(raw json.RawMessage) ([]string, []blueprintStep, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, nil, err
	}
	var keys []string
	var steps []blueprintStep
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, _ := token.(string)
		var step blueprintStep
		if err := dec.Decode(&step); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
		steps = append(steps, step)
	}
	return keys, steps, nil
}

// blueprintKey is why the child table's key is not one a stamp draws, or
// "": none, which the generator adds as a UUID, or one field of
// Identity.UUID, Identity.UserID or string.
func blueprintKey(child *ir.TypeDef) string {
	var keys []*ir.FieldDef
	for _, fd := range child.Fields {
		if fd != nil && fd.Key {
			keys = append(keys, fd)
		}
	}
	switch {
	case len(keys) == 0:
		return ""
	case len(keys) > 1:
		return fmt.Sprintf("is keyed by %d fields", len(keys))
	}
	switch keys[0].TypeRef.Name {
	case "Identity.UUID", "Identity.UserID", "string":
		return ""
	}
	return fmt.Sprintf("is keyed by %s, a %s", keys[0].Name, keys[0].TypeRef.Name)
}

// blueprintList reports whether the own field name of td holds a list.
func blueprintList(td *ir.TypeDef, name string) bool {
	fd := fieldNamedIn(td, name)
	return fd != nil && fd.TypeRef.IsArray
}

// childConstants are the fields the Constants td implements keeps, and
// whether td implements Constants.
func childConstants(td *ir.TypeDef) ([]string, bool) {
	for _, use := range td.ImplementsBehaviors() {
		if use.Name != constantsBehavior {
			continue
		}
		var config struct {
			Fields []string `json:"fields"`
		}
		_ = json.Unmarshal(use.Config, &config)
		return config.Fields, true
	}
	return nil, false
}

// dependenciesSchemas are the tables whose rows may block td's through the
// Dependencies td implements, td itself when it names none, and whether td
// implements Dependencies.
func dependenciesSchemas(td *ir.TypeDef) ([]string, bool) {
	for _, use := range td.ImplementsBehaviors() {
		if use.Name != dependenciesBehavior {
			continue
		}
		var config struct {
			Schemas []string `json:"schemas"`
		}
		if len(use.Config) > 0 {
			_ = json.Unmarshal(use.Config, &config)
		}
		if len(config.Schemas) == 0 {
			return []string{td.Name}, true
		}
		return config.Schemas, true
	}
	return nil, false
}
