package tsgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The behaviors of a table whose fields are behaviors, in orm.ts (D103): a
// DB type that writes `status: Workflow<{ ... }>` stays a table, and the
// fields the behavior owns are columns of it that its row holds and the
// inputs of its create, update and upsert leave out. orm.ts embeds the database's layout
// document (behaviorlayout.Of), which the HTTP runtime's adapter
// (runtime/http/typescript/src/rules.ts) reads to run the behaviors' core
// inside the ORM's transaction, imports the core from the rules package
// (naming.RulesNpmPackage) and passes it to ormOf (withRulesCore), and
// types each use's operations as members of the table's repository under
// the use's id, orm.order.status.transition(key, { to: 'paid' }). Nothing
// here names a behavior: the operations, their parameters and results, and
// the owned fields' types come from each behavior's declaration, narrowed
// by its config where behaviortypes narrows one.

// repositoryMembers are the members of a table's repository in the HTTP
// runtime (runtime/http/typescript/src/orm.ts's TableRepository), which a
// use's id, the member its operations sit under, may not take: its methods
// (Repository, SoftDeleteRepository, EncryptedRepository and
// VectorRepository), then the members it keeps for itself, which the
// runtime refuses to shadow as well (bindUses) and the build refuses
// first. TestRepositoryMembersAreTheRuntimes holds the list to orm.ts.
var repositoryMembers = []string{
	"getOne", "getManyByIds", "findOne", "findMany", "findPage",
	"createOne", "createMany", "updateOne", "updateMany", "upsertOne", "upsertOneResult", "upsertMany",
	"deleteOne", "deleteMany", "hardDeleteOne", "nearest", "reencrypt",

	"constructor", "encrypted", "behaviors", "peers", "orm", "def", "runner", "acting", "keys", "rules", "user",
	"peer", "selects", "selection", "selectText", "strip", "related", "hydrate", "clauses", "column",
	"keyColumn", "audit", "decode", "read", "atomically", "cell", "ring", "open", "seal", "sealed",
	"sealedUpdate", "setsEncrypted", "encode", "requireUser", "conditions", "group", "whereOf", "liveWhere",
	"orderBy", "loaded", "nearestSettings", "stamped", "insertValue", "insertable", "insertOne",
	"insertOrder", "insertTuples", "setClauses", "compareAs", "stamp", "uniqueKey", "expressionKey",
	"keyColumns", "upsertWhere", "upsertRow", "upsertBatches", "rowsOfKeys", "resealed", "rulesHost",
	"caller", "columnNamed", "ownedColumns", "coreOwn", "createColumns", "updateColumns",
	"operationColumns", "wireKey", "createThroughCore", "updateThroughCore", "deleteThroughCore",
	"notFound", "operate", "bindUses", "refuseOwnedUpdate", "upsertInputs", "actor", "clearActor",
}

// ORMUse is a field-anchored use of a behavior as orm.ts writes it: ID,
// the repository's member its operations sit under, the anchor field;
// Behavior, its registered name; and its operations on a row.
type ORMUse struct {
	ID         string
	Behavior   string
	Operations []ORMOperation
}

// ORMOperation is a use's operation as orm.ts types it: its name, its
// declaration's description, and the TypeScript types of its parameters
// and result, from its paramsSchema and resultSchema as the use's config
// narrows them, or unknown where a schema has no type orm.ts can write.
type ORMOperation struct {
	Name   string
	Doc    string
	Params string
	Result string
}

// ormRules is what orm.ts writes of schema's tables whose fields are
// behaviors: the layout document, each table's uses by its type, and the
// TypeScript type of each owned field whose declared type its use's
// config narrows (a state machine's states), by type and field. It is nil
// for a schema with no such table.
type ormRules struct {
	layout *behaviorlayout.Layout
	uses   map[string][]ORMUse
	types  map[string]map[string]string
}

// typeScriptRules builds ormRules for schema, whose behaviors reg declares.
func typeScriptRules(schema *ir.Schema, reg *registry.Registry) (*ormRules, error) {
	layout, err := behaviorlayout.Of(schema, reg)
	if err != nil || layout == nil {
		return nil, err
	}
	out := &ormRules{layout: layout, uses: map[string][]ORMUse{}, types: map[string]map[string]string{}}
	for name, table := range layout.Types {
		td := schema.Types[name]
		out.types[name] = map[string]string{}
		for _, use := range table.Uses {
			if slices.Contains(repositoryMembers, use.ID) {
				return nil, fmt.Errorf("tsgen: table %s: the behavior %s on field %s would put its operations under the repository's member %s, which the repository holds itself; rename the field", name, use.Behavior, use.ID, use.ID)
			}
			ref, _ := td.FieldBehavior(use.ID)
			declaration, ok := reg.Behavior(use.Behavior)
			if !ok {
				return nil, fmt.Errorf("tsgen: table %s: field %s: behavior %s is not registered", name, use.ID, use.Behavior)
			}
			narrowed, err := behaviortypes.Narrow(declaration, ref.Config, behaviortypes.Target{
				Schema:   schema.Name,
				Base:     td.Name + codegen.ToPascalCase(use.ID),
				Document: schema,
				Instance: td,
			})
			if err != nil {
				return nil, fmt.Errorf("tsgen: table %s: field %s: behavior %s: %w", name, use.ID, use.Behavior, err)
			}
			aliases := map[string]any{}
			for _, alias := range narrowed.Aliases {
				aliases[alias.Name] = alias.Schema
			}
			for _, field := range use.Fields {
				if !slices.Contains(behaviorFieldBuiltins, field.Type) {
					continue
				}
				if shape, ok := narrowed.Fields[field.Name]; ok {
					if rendered := renderNarrowed(shape.Schema, aliases, "  "); rendered != "unknown" {
						out.types[name][field.Field] = rendered
					}
				}
			}
			ormUse := ORMUse{ID: use.ID, Behavior: use.Behavior}
			for _, op := range use.Operations {
				var doc string
				for _, declared := range declaration.Operations {
					if declared.Name == op.Name {
						doc = declared.Description
					}
				}
				ormUse.Operations = append(ormUse.Operations, ORMOperation{
					Name:   op.Name,
					Doc:    doc,
					Params: renderNarrowed(narrowed.Params[op.Name], aliases, "      "),
					Result: renderNarrowed(narrowed.Results[op.Name], aliases, "      "),
				})
			}
			out.uses[name] = append(out.uses[name], ormUse)
		}
	}
	return out, nil
}

// behaviorFieldBuiltins are the builtin types a declared field may take,
// whose owned field's TypeScript type a narrowing may make precise; a
// scalar's is its scalar's type.
var behaviorFieldBuiltins = []string{"string", "number", "boolean"}

// renderNarrowed writes a narrowed JSON Schema as a TypeScript type at an
// indent, each type the narrowing names written in place and each
// description left out: unknown for a schema orm.ts cannot write without a
// type it does not declare (a JSON object of no named members).
func renderNarrowed(schema any, aliases map[string]any, indent string) string {
	if schema == nil {
		return "unknown"
	}
	r := behaviortypes.TSRenderer{}
	rendered := r.Render(inlineSchema(schema, aliases), indent)
	if r.UsesJSONObject {
		return "unknown"
	}
	return rendered
}

// inlineSchema copies a narrowed schema with each node that names a type
// ({"$ts": name}) replaced by the schema the name stands for, and every
// description removed.
func inlineSchema(schema any, aliases map[string]any) any {
	switch s := schema.(type) {
	case map[string]any:
		if name, ok := s[behaviortypes.RefKey].(string); ok {
			alias, ok := aliases[name]
			if !ok {
				// A type of the narrowing's own (<Base>Fields) that orm.ts
				// does not declare.
				return true
			}
			inlined := inlineSchema(alias, aliases)
			if nullable, _ := s["nullable"].(bool); nullable {
				return map[string]any{"anyOf": []any{inlined, map[string]any{"type": "null"}}}
			}
			return inlined
		}
		out := make(map[string]any, len(s))
		for key, value := range s {
			if key == "description" {
				continue
			}
			if key == "properties" {
				if properties, ok := value.(map[string]any); ok {
					copied := make(map[string]any, len(properties))
					for name, property := range properties {
						copied[name] = inlineSchema(property, aliases)
					}
					out[key] = copied
					continue
				}
			}
			out[key] = inlineSchema(value, aliases)
		}
		return out
	case []any:
		out := make([]any, len(s))
		for i, each := range s {
			out[i] = inlineSchema(each, aliases)
		}
		return out
	}
	return schema
}

// RulesLayout is the layout document as orm.ts embeds it: JSON, which is a
// TypeScript object literal, indented under its declaration.
func (o *ORMInfo) RulesLayout() (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(o.Rules); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
