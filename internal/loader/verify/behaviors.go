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
// behavior's fields sit under its name). A table's uses, the behaviors it
// implements and those anchored on its fields (D103), are held to
// checkTableUses as well, and a behavior it implements is one the
// behaviors' core runs and that names no value field. A declaration's own
// field and operation names are checked when it registers.
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
	if onType := td.TypeBehaviors(); len(onType) > 0 {
		if implemented := td.ImplementsBehaviors(); len(implemented) > 0 {
			r.errorf(td.Owner, "type %s composes behavior %s on the type (@behavior) and behavior %s by implements: a type composes its behaviors with @behavior (an engine type, D58) or on a table, by implements and field types (D103), never both", td.Name, onType[0].Name, implemented[0].Name)
			return
		}
		if onFields := td.FieldBehaviors(); len(onFields) > 0 {
			r.errorf(td.Owner, "type %s composes behavior %s on the type (@behavior) and behavior %s on its field %s: a type composes its behaviors on the type or on fields, never both (D103)", td.Name, onType[0].Name, onFields[0].Name, onFields[0].Field)
			return
		}
	}
	for _, ref := range td.Behaviors {
		if ref.Implements && ref.Field != "" {
			r.errorf(td.Owner, "type %s: behavior %s is anchored on field %s and implemented by the type: a use is one or the other (D103)", td.Name, ref.Name, ref.Field)
			return
		}
	}
	ids := make([]string, 0, len(td.Behaviors))
	var listed []string
	var declared []registry.Behavior
	uses := map[string]registry.Behavior{}
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
		if ref.Implements && !implementable(behavior, reg, td, r) {
			continue
		}
		if err := behavior.ValidateConfig(ref.Config); err != nil {
			if ref.FieldAnchored() {
				r.errorf(td.Owner, "type %s: field %s: %s", td.Name, ref.Field, err)
			} else {
				r.errorf(td.Owner, "type %s: %s", td.Name, err)
			}
		} else if ref.Name == "Derived" && !ref.FieldAnchored() && !ref.Implements {
			checkDerivedInputs(td, ref.Config, r)
		} else if ref.Name == "Timers" && !ref.FieldAnchored() && !ref.Implements && configNames(ref.Config, "status") {
			// Timers' declaration takes status for a table, whose core
			// reads the Workflow it names; the engine's Timers reads its
			// one Workflow and would ignore it (D103).
			r.errorf(td.Owner, "type %s: @behavior Timers names status, the Workflow field a table's Timers reads (implements Timers<%s, C>, D103); an engine type's Timers reads its one Workflow, so leave status out", td.Name, td.Name)
		}
		if ref.FieldAnchored() || ref.Implements {
			uses[id] = behavior
		}
		if !slices.Contains(listed, ref.Name) {
			listed = append(listed, ref.Name)
			declared = append(declared, behavior)
		}
	}
	if len(uses) > 0 {
		checkTableUses(schema, td, bases, uses, r)
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
		// A table's use's operations sit under its member, the field's
		// name or the behavior's (orm.order.status.transition,
		// orm.ticket.timers.fireTimer), so they share no name with another
		// use's (D103).
		if len(uses) > 0 {
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

// configNames reports whether config, a JSON object, has the member key.
func configNames(config json.RawMessage, key string) bool {
	var members map[string]json.RawMessage
	if json.Unmarshal(config, &members) != nil {
		return false
	}
	_, ok := members[key]
	return ok
}

// implementable reports whether a table may implement behavior (D103): one
// the behaviors' core runs (registry.RulesBehaviors), naming no value
// field, which a field anchors instead. It reports why not on td.
func implementable(behavior registry.Behavior, reg *registry.Registry, td *ir.TypeDef, r *Result) bool {
	if !registry.RunsOnTheCore(behavior.Name) {
		var names []string
		for _, name := range registry.RulesBehaviors {
			if b, ok := reg.Behavior(name); ok && b.Value == "" {
				names = append(names, name)
			}
		}
		r.errorf(td.Owner, "type %s implements %s, which the behaviors' core does not run: a table implements one of %s (D103)", td.Name, behavior.Name, strings.Join(names, ", "))
		return false
	}
	if behavior.Value != "" {
		r.errorf(td.Owner, "type %s implements %s, whose declaration names its value field %s, so it is anchored on a field: write %s: %s<{ ... }> (D103)", td.Name, behavior.Name, behavior.Value, behavior.Value, behavior.Name)
		return false
	}
	return true
}

// fieldName is what a field anchor's field may be named: an identifier, as
// a field of a schema is.
var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkTableUses holds td's uses as a table (D103), the behaviors it
// implements and those anchored on its fields, each by its id, to what the
// loader's pass that adds their fields, sub-tables and deadline queue needs
// (internal/loader/behaviorfields): td is a DB table, so each declared
// field becomes a column of it; each field behavior names its value field
// and types each field a column holds; no two uses sit under one member;
// no field or column the pass adds, by the layout rule
// (behaviorlayout.OwnedFieldName and OwnedColumn), is one of td's own or
// another use's; a table whose uses keep sub-tables or arm deadlines has
// one key field, which their rows and messages name; and no type the pass
// adds is one the schema declares. The anchor field is the value field
// itself, so td declares no field of its name, and a table that composes
// behaviors keeps its version in _version (@optimistic), which no own
// column may take. A Timers use's config is held to the table
// (checkTimersConfig).
func checkTableUses(schema *ir.Schema, td *ir.TypeDef, bases map[string]bool, uses map[string]registry.Behavior, r *Result) {
	if schema.Kind != ir.SchemaKindDB || !hasTable(td, bases) {
		if implemented := td.ImplementsBehaviors(); len(implemented) > 0 {
			r.errorf(td.Owner, "type %s implements %s, and a type implements a behavior on a DB table, which %s is not (D103)", td.Name, implemented[0].Name, td.Name)
			return
		}
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
	// The member each use's operations sit under: an implements use's is
	// the behavior's storage key, which a field use's field may also be.
	members := map[string]ir.BehaviorRef{}
	for _, ref := range td.TableBehaviors() {
		if _, ok := uses[ref.UseID()]; !ok {
			continue
		}
		member := behaviorlayout.UseMember(ref)
		other, taken := members[member]
		members[member] = ref
		if !taken {
			continue
		}
		switch {
		case !other.FieldAnchored() && ref.FieldAnchored():
			r.errorf(td.Owner, "type %s: implements %s, whose operations sit under %s, and its field %s is a %s: rename the field (D103)", td.Name, other.Name, member, ref.Field, ref.Name)
		case !other.FieldAnchored():
			r.errorf(td.Owner, "type %s: implements %s and %s, whose operations both sit under %s (D103)", td.Name, other.Name, ref.Name, member)
		}
	}
	// added holds each field and column a use adds, by what adds it.
	addedFields := map[string]string{}
	addedColumns := map[string]string{}
	for _, ref := range td.TableBehaviors() {
		behavior, ok := uses[ref.UseID()]
		if !ok {
			continue
		}
		where := fmt.Sprintf("type %s: field %s: behavior %s", td.Name, ref.Field, ref.Name)
		member := behaviorlayout.UseMember(ref)
		adder := "field " + ref.Field + "'s " + ref.Name
		if ref.FieldAnchored() {
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
		} else {
			where = fmt.Sprintf("type %s: implements %s", td.Name, ref.Name)
			adder = "implements " + ref.Name + "'s"
			if ref.Name == timersBehavior {
				checkTimersConfig(schema, td, ref, r)
			}
		}
		for _, field := range behavior.Fields {
			if field.Volatile {
				continue
			}
			if field.Type == "" {
				// An implements use's untyped field is a read the
				// behavior computes (Timers' deadlines), which takes no
				// column.
				if ref.FieldAnchored() {
					r.errorf(td.Owner, "%s declares field %s with no type, so it takes no column of a table; compose it on the type (D103)", where, field.Name)
				}
				continue
			}
			value := ref.FieldAnchored() && field.Name == behavior.Value
			name := behaviorlayout.OwnedFieldName(member, field.Name, value)
			column := behaviorlayout.OwnedColumn(ref, field.Name, value)
			adds := fmt.Sprintf("%s %s", adder, field.Name)
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
	checkTableStorage(schema, td, uses, r)
}

// checkTableStorage holds what td's uses keep beside the table (D103):
// the sub-tables their behaviors declare and the deadline queue of a
// table whose behaviors arm deadlines. Their rows and messages name the
// row by its key, so td has one key field, and the types the loader adds
// for them take no name of a type or a table the schema declares. A type
// the loader added, as an IR read back holds it, is the one it adds.
func checkTableStorage(schema *ir.Schema, td *ir.TypeDef, uses map[string]registry.Behavior, r *Result) {
	var keeping, arming []string
	for _, ref := range td.TableBehaviors() {
		behavior, ok := uses[ref.UseID()]
		if !ok {
			continue
		}
		if len(behavior.Tables) > 0 && !slices.Contains(keeping, ref.Name) {
			keeping = append(keeping, ref.Name)
		}
		if behavior.ArmsDeadlines && !slices.Contains(arming, ref.Name) {
			arming = append(arming, ref.Name)
		}
	}
	if len(keeping) == 0 && len(arming) == 0 {
		return
	}
	var keys []string
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			keys = append(keys, fd.Name)
		}
	}
	if len(keys) > 1 {
		names := keeping
		if len(names) == 0 {
			names = arming
		}
		r.errorf(td.Owner, "type %s: behavior %s keeps rows of its own per row, and %s's key is %d fields (%s): a table whose behaviors keep sub-tables or arm deadlines has one @key field (D103)", td.Name, names[0], td.Name, len(keys), strings.Join(keys, ", "))
		return
	}
	tables := map[string]string{}
	for _, name := range sortedTypeNames(schema.Types) {
		other := schema.Types[name]
		switch {
		case other == nil, other.BehaviorTable != nil && other.BehaviorTable.Row == td.Name, other.BehaviorQueue != nil && other.BehaviorQueue.Row == td.Name:
		case other.Queue != nil:
			tables[sqlutil.QueueTableName(other)] = other.Name
		case other.Role == ir.RoleDBTable && !other.JsonField:
			tables[sqlutil.TableName(other)] = other.Name
		}
	}
	for _, ref := range td.TableBehaviors() {
		behavior, ok := uses[ref.UseID()]
		if !ok {
			continue
		}
		for _, table := range behavior.Tables {
			name := behaviorlayout.SubTableTypeName(td.Name, ref, table.Name)
			sql := codegen.ToSnakeCase(name)
			if added := schema.Types[name]; added != nil && added.BehaviorTable != nil && added.BehaviorTable.Row == td.Name {
				continue
			}
			if schema.Types[name] != nil || schema.Inputs[name] != nil || tables[sql] != "" {
				r.errorf(td.Owner, "type %s: behavior %s adds the table %s (%s) for its %s, which the schema also declares; rename that type (D103)", td.Name, ref.Name, name, sql, table.Name)
			}
		}
	}
	if len(arming) == 0 {
		return
	}
	name := behaviorlayout.DeadlineQueueTypeName(td.Name)
	sql := codegen.ToSnakeCase(name) + ir.QueueTableSuffix
	if added := schema.Types[name]; added != nil && added.BehaviorQueue != nil && added.BehaviorQueue.Row == td.Name {
		return
	}
	if schema.Types[name] != nil || schema.Inputs[name] != nil || tables[sql] != "" {
		r.errorf(td.Owner, "type %s: its behaviors arm deadlines (%s), whose queue the build adds as %s (%s), which the schema also declares; rename that type (D103)", td.Name, strings.Join(arming, ", "), name, sql)
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
