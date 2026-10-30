package verify

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkBehaviors holds every type's behaviors to their declarations in the
// registry (D16): each behavior is registered, each use's id is listed
// once, its config passes its config schema, what it requires is on the
// type and what it conflicts with is not, and no two type-anchored
// behaviors add an operation of the same name. The fields a type-anchored
// behavior adds cannot collide: an instance keeps them under the
// behavior's name, apart from the type's own fields (D16, amended: a
// behavior's fields sit under its name). A field-anchored use (D103) is
// held to checkFieldAnchors as well. A declaration's own field and
// operation names are checked when it registers.
func checkBehaviors(schema *ir.Schema, reg *registry.Registry, r *Result) {
	bases := codegen.BaseTypeNames(schema)
	for _, types := range []map[string]*ir.TypeDef{schema.Types, schema.Inputs} {
		for _, name := range sortedTypeNames(types) {
			if td := types[name]; td != nil && len(td.Behaviors) > 0 {
				checkTypeBehaviors(schema, td, bases, reg, r)
			}
		}
	}
}

func checkTypeBehaviors(schema *ir.Schema, td *ir.TypeDef, bases map[string]bool, reg *registry.Registry, r *Result) {
	if onType, onFields := td.TypeBehaviors(), td.FieldBehaviors(); len(onType) > 0 && len(onFields) > 0 {
		r.errorf(td.Owner, "type %s composes behavior %s on the type (@behavior) and behavior %s on its field %s: a type composes its behaviors on the type or on fields, never both (D103)", td.Name, onType[0].Name, onFields[0].Name, onFields[0].Field)
		return
	}
	ids := make([]string, 0, len(td.Behaviors))
	var listed []string
	var declared []registry.Behavior
	fields := map[string]registry.Behavior{}
	for _, ref := range td.Behaviors {
		id := ref.UseID()
		if slices.Contains(ids, id) {
			if ref.FieldAnchored() {
				r.errorf(td.Owner, "type %s anchors two behaviors on its field %s", td.Name, ref.Field)
			} else {
				r.errorf(td.Owner, "type %s lists behavior %s twice", td.Name, ref.Name)
			}
			continue
		}
		ids = append(ids, id)
		behavior, ok := reg.Behavior(ref.Name)
		if !ok {
			known := "none are registered"
			if names := reg.BehaviorNames(); len(names) > 0 {
				known = "registered: " + strings.Join(names, ", ")
			}
			r.errorf(td.Owner, "type %s: behavior %q is not a registered behavior (%s)", td.Name, ref.Name, known)
			continue
		}
		if err := behavior.ValidateConfig(ref.Config); err != nil {
			if ref.FieldAnchored() {
				r.errorf(td.Owner, "type %s: field %s: %s", td.Name, ref.Field, err)
			} else {
				r.errorf(td.Owner, "type %s: %s", td.Name, err)
			}
		} else if ref.Name == "Derived" && !ref.FieldAnchored() {
			checkDerivedInputs(td, ref.Config, r)
		}
		if ref.FieldAnchored() {
			fields[ref.Field] = behavior
		}
		if !slices.Contains(listed, ref.Name) {
			listed = append(listed, ref.Name)
			declared = append(declared, behavior)
		}
	}
	if len(fields) > 0 {
		checkFieldAnchors(schema, td, bases, fields, r)
	}

	opOwner := map[string]string{}
	for _, behavior := range declared {
		for _, required := range behavior.Requires {
			if !slices.Contains(listed, required) {
				r.errorf(td.Owner, "type %s: behavior %s requires behavior %s, which the type does not list", td.Name, behavior.Name, required)
			}
		}
		for _, conflict := range behavior.Conflicts {
			if slices.Contains(listed, conflict) {
				r.errorf(td.Owner, "type %s: behavior %s conflicts with behavior %s, which the type also lists", td.Name, behavior.Name, conflict)
			}
		}
		// A field-anchored use's operations sit under its id, the field's
		// name (orm.order.status.transition), so they share no name with
		// another use's (D103).
		if len(fields) > 0 {
			continue
		}
		for _, op := range behavior.Operations {
			// A namespace operation (D77) is called by its behavior's
			// name, not on the type, so it shares no name with the type's
			// operations, as the engine composes them.
			if op.Scope == registry.OperationScopeNamespace {
				continue
			}
			if prev, taken := opOwner[op.Name]; taken {
				r.errorf(td.Owner, "type %s: behaviors %s and %s both add operation %s", td.Name, prev, behavior.Name, op.Name)
				continue
			}
			opOwner[op.Name] = behavior.Name
		}
	}
}

// fieldName is what a field anchor's field may be named: an identifier, as
// a field of a schema is.
var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkFieldAnchors holds td's field-anchored uses (D103), each a behavior
// by its anchor field, to what the loader's pass that adds their fields
// needs (internal/loader/behaviorfields): td is a DB table, so each
// declared field becomes a column of it; each behavior names its value
// field and types each field a column holds; and no field or column the
// pass adds, by the layout rule (behaviorlayout.OwnedFieldName and
// OwnedColumn), is one of td's own or another use's. The anchor field is
// the value field itself, so td declares no field of its name, and a table
// that composes behaviors keeps its version in _version (@optimistic),
// which no own column may take.
func checkFieldAnchors(schema *ir.Schema, td *ir.TypeDef, bases map[string]bool, uses map[string]registry.Behavior, r *Result) {
	if schema.Kind != ir.SchemaKindDB || !hasTable(td, bases) {
		ref := td.FieldBehaviors()[0]
		r.errorf(td.Owner, "type %s: field %s is a %s, and a behavior is anchored on a field of a DB table, which %s is not (D103)", td.Name, ref.Field, ref.Name, td.Name)
		return
	}
	ownFields := map[string]bool{}
	ownColumns := map[string]string{}
	for _, fd := range td.Fields {
		// A field the loader's pass added, as an IR read back holds it, is
		// the behavior's, not the type's own.
		if fd == nil || fd.Owned() {
			continue
		}
		ownFields[fd.Name] = true
		if sqlutil.IsTableRelation(schema, fd) && fd.TypeRef.IsArray {
			continue
		}
		ownColumns[sqlutil.ColumnNameIn(schema, fd)] = fd.Name
	}
	if !td.Versioned && !td.Optimistic {
		if field, taken := ownColumns[sqlutil.VersionColumn]; taken {
			r.errorf(td.Owner, "%s.%s: its column is %s, the column a table that composes behaviors keeps its version in (D103); give it another name with @column", td.Name, field, sqlutil.VersionColumn)
		}
	}
	// added holds each field and column a use adds, by what adds it.
	addedFields := map[string]string{}
	addedColumns := map[string]string{}
	for _, ref := range td.FieldBehaviors() {
		behavior, ok := uses[ref.Field]
		if !ok {
			continue
		}
		where := fmt.Sprintf("type %s: field %s: behavior %s", td.Name, ref.Field, ref.Name)
		if !fieldName.MatchString(ref.Field) {
			r.errorf(td.Owner, "type %s: behavior %s is anchored on field %q, which is not a field name: a letter or _ and then letters, digits and _", td.Name, ref.Name, ref.Field)
			continue
		}
		if ownFields[ref.Field] {
			r.errorf(td.Owner, "%s is anchored on %s, which the type also declares as a field of its own: the anchor field is the behavior's value field, which the behavior adds (D103)", where, ref.Field)
			continue
		}
		if behavior.Value == "" {
			r.errorf(td.Owner, "%s names no value field in its declaration, so it is not anchored on a field; compose it on the type (D103)", where)
			continue
		}
		for _, field := range behavior.Fields {
			if field.Volatile {
				continue
			}
			if field.Type == "" {
				r.errorf(td.Owner, "%s declares field %s with no type, so it takes no column of a table; compose it on the type (D103)", where, field.Name)
				continue
			}
			value := field.Name == behavior.Value
			name := behaviorlayout.OwnedFieldName(ref.Field, field.Name, value)
			column := behaviorlayout.OwnedColumn(ref, field.Name, value)
			adds := fmt.Sprintf("field %s's %s %s", ref.Field, ref.Name, field.Name)
			switch {
			case ownFields[name] && !value:
				r.errorf(td.Owner, "%s adds field %s for its %s, which the type also declares; rename one of them (D103)", where, name, field.Name)
			case ownColumns[column] != "":
				r.errorf(td.Owner, "%s adds column %s for its %s, the column of %s.%s; rename one of them (D103)", where, column, field.Name, td.Name, ownColumns[column])
			case addedFields[name] != "":
				r.errorf(td.Owner, "type %s: %s and %s both add field %s; rename one of the fields (D103)", td.Name, addedFields[name], adds, name)
			case addedColumns[column] != "":
				r.errorf(td.Owner, "type %s: %s and %s both add column %s; rename one of the fields (D103)", td.Name, addedColumns[column], adds, column)
			}
			addedFields[name] = adds
			addedColumns[column] = adds
		}
	}
}

// checkDerivedInputs refuses a secret field as an input of a Derived field:
// a deriver sends its inputs to a model outside the engine, and a secret
// never leaves it (D74). The engine refuses the same at define; this
// refuses it at build, before anything deploys.
func checkDerivedInputs(td *ir.TypeDef, config json.RawMessage, r *Result) {
	var parsed struct {
		Fields map[string]struct {
			From []string `json:"from"`
		} `json:"fields"`
	}
	if json.Unmarshal(config, &parsed) != nil {
		return
	}
	secret := map[string]bool{}
	for _, field := range td.Fields {
		if field != nil && field.Secret {
			secret[field.Name] = true
			if field.JSONTag != "" {
				secret[field.JSONTag] = true
			}
		}
	}
	names := make([]string, 0, len(parsed.Fields))
	for name := range parsed.Fields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		for _, input := range parsed.Fields[name].From {
			if secret[input] {
				r.errorf(td.Owner, "type %s: Derived field %s is derived from %s, which is secret: a secret never goes to a model", td.Name, name, input)
			}
		}
	}
}
