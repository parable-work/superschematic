// Package behaviorfields adds the fields a table's behaviors own to the
// tables that compose them, and the sub-tables and the deadline queue they
// keep (D103 in docs/DECISIONS.md). A DB type that
// writes `status: Workflow<{ ... }>` stays a table: each field the
// behavior declares becomes a column of it, which the readers do not write
// (they emit no field for the anchor) and the loader adds here, once the
// behaviors' own checks pass and before the rest of the verification
// pass, so every authoring form gets the same fields and every check that
// resolves a field by its name (an index, a projection's column,
// @enqueueOnChange) takes them as the table's own. The generators then
// meet them as ordinary fields of the table, each marked with the use that
// owns it (ir.FieldDef.Behavior), which the repositories read and leave
// out of their inputs.
//
// The names come from the layout rule, which lives in
// internal/generator/behaviorlayout alone: the value field takes the
// anchor's name and column (status), and every other field the anchor's
// name with its own after it (statusEnteredAt, status_entered_at). A use
// the table implements, `implements Timers<Ticket, { ... }>`, adds its
// typed fields after the behavior's storage key (timers_*); an untyped one
// is a read the behavior computes and takes no column.
//
// A use whose behavior declares sub-tables gets each as a table of its own
// (ir.TypeDef.BehaviorTable): a child of the row, named for the row, the
// use's member and the table (TicketTimersDeadlines,
// ticket_timers_deadlines), with a required relation to the row that
// deletes it with the row, the declared fields, and one row per key within
// its row. A table whose behaviors arm deadlines gets one plain queue
// (ir.TypeDef.BehaviorQueue, TicketDeadline, ticket_deadline_queue), each
// live message a deadline armed on a row, which a @behaviorWorker of an API
// over the database runs (D53). A table whose behaviors emit events of
// their own (Schedules' run, D104) gets its event queue (TicketEvent,
// ticket_event_queue): a D80 queue keyed by the row's key and serialized,
// one message per event, which the adapters write in the command's
// transaction and the same worker hands to the project's handler. A table
// whose writes are heard, by a
// behavior of its own that hears changes (Timers, Reactions) or by another
// table's through a relation of it (Reactions' allTerminal over
// task.project), gets its change queue (TicketChange,
// ticket_change_queue): a D80 queue keyed by the row's key and serialized,
// one message per write of a row, whoever makes it, which sqlgen's change
// trigger writes and the same worker runs. All are ordinary types from
// here on: sqlgen writes them, D27 plans them, and the generators that
// read a table's behaviors know them by their markers.
package behaviorfields

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// builtins are the types a declared field may take that are no scalar.
var builtins = []string{"string", "number", "boolean"}

// Expand adds, to each DB table of schema that composes behaviors as a
// table (ir.TypeDef.TableBehaviors), the fields those behaviors declare,
// after the table's own fields: for each use in list order, each declared
// field in declaration order, a volatile field excepted, since a value
// computed at a read takes no column, and an untyped field of a use the
// table implements likewise. A field takes its declaration's type and
// description; the value field is required, since the core sets it on
// every row it creates, and every other field is optional, absent until a
// behavior sets it. Each such table is given @optimistic's _version unless
// it is @versioned, the version the core's change sets hold each row to.
// Each use's declared sub-tables join the schema as tables, a table
// whose behaviors arm deadlines gets its deadline queue, a table whose
// behaviors emit events its event queue (D104), and a table whose writes
// are heard gets its change queue after them. Expand returns the
// scalars the added fields name, which the loader hydrates from the
// registry.
//
// The behaviors' checks have held what Expand relies on
// (verify.CheckBehaviors, checkTableUses among them): each behavior is
// registered, names its value field and types its fields, no added field
// or column is one the table already has, no added type's name is taken,
// a table that keeps sub-tables, arms deadlines, emits events or whose
// writes are heard has one key field, and a listen's relation is a to-one relation of the
// table heard to the listening one (checkReactionsConfig). A field or type
// already added, as an IR read back holds it, is kept, so Expand may run
// again on its own output.
func Expand(schema *ir.Schema, reg *registry.Registry) ([]string, error) {
	if schema == nil || schema.Kind != ir.SchemaKindDB {
		return nil, nil
	}
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	heardBy := HeardBy(schema)
	var scalars []string
	for _, name := range names {
		td := schema.Types[name]
		if td == nil || td.Role != ir.RoleDBTable || (len(td.TableBehaviors()) == 0 && len(heardBy[name]) == 0) {
			continue
		}
		var used, added []string
		if len(td.TableBehaviors()) > 0 {
			var err error
			if used, err = expandType(td, reg); err != nil {
				return nil, err
			}
			if added, err = expandStorage(schema, td, reg); err != nil {
				return nil, err
			}
			emitted, err := expandEvents(schema, td, reg)
			if err != nil {
				return nil, err
			}
			added = append(added, emitted...)
		}
		heard, err := expandChanges(schema, td, reg, heardBy[name])
		if err != nil {
			return nil, err
		}
		for _, scalar := range slices.Concat(used, added, heard) {
			if !slices.Contains(scalars, scalar) {
				scalars = append(scalars, scalar)
			}
		}
	}
	for _, scalar := range scalars {
		if schema.Scalars == nil {
			schema.Scalars = map[string]*ir.ScalarDef{}
		}
		if schema.Scalars[scalar] == nil {
			schema.Scalars[scalar] = &ir.ScalarDef{Name: scalar}
		}
	}
	return scalars, nil
}

// ExpandExternal adds, to each type of types that composes behaviors as a
// table, the fields those behaviors own, as Expand adds them to the
// database's tables. types are the flattened source types the TypeScript
// reader resolves for a @source view of another service's table
// (verify.Input.ExternalTypes), which hold the table's own fields and its
// uses but none of the fields a use owns, so the verification pass checks
// a view that projects one (status, statusEnteredAt) against the table as
// its database's load holds it (D103). A use the database's own load
// refuses (an unregistered behavior, a field the table already has) is
// left as it is.
func ExpandExternal(types map[string]*ir.TypeDef, reg *registry.Registry) {
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		td := types[name]
		if td == nil || len(td.TableBehaviors()) == 0 {
			continue
		}
		fields := td.Fields
		if _, err := expandType(td, reg); err != nil {
			td.Fields = fields
		}
	}
}

// ExpandFields adds the fields a table's behaviors own to each DB table of
// schema whose uses allow it, and nothing else of Expand's: no sub-table,
// no queue. It is for a schema whose behaviors' checks failed, so that the
// verification pass, which then runs to report them, does not also report
// each index, uniqueness, projection column or @enqueueOnChange that names
// a field a sound use owns (status, beside a Timers config the build
// refuses) as one that names no field. A table one of whose uses cannot be
// expanded (an unregistered behavior, a field the table already declares,
// a declared field with no type or with one the registry lacks, as a use
// anchored on a field takes of a behavior a table implements) is left as
// it was written. It returns the scalars the added fields name,
// which the loader hydrates from the registry, and adds each to the
// schema's scalars, as Expand does.
func ExpandFields(schema *ir.Schema, reg *registry.Registry) []string {
	if schema == nil || schema.Kind != ir.SchemaKindDB {
		return nil
	}
	var scalars []string
	for _, name := range sortedNames(schema.Types) {
		td := schema.Types[name]
		if td == nil || td.Role != ir.RoleDBTable || len(td.TableBehaviors()) == 0 {
			continue
		}
		fields, optimistic := td.Fields, td.Optimistic
		used, err := expandType(td, reg)
		typed := err == nil && !slices.ContainsFunc(td.Fields[len(fields):], func(added *ir.FieldDef) bool {
			return added.TypeRef.Name == ""
		})
		for _, scalar := range used {
			if _, known := reg.Scalars().Scalar(scalar); !known {
				typed = false
			}
		}
		if !typed {
			td.Fields, td.Optimistic = fields, optimistic
			continue
		}
		for _, scalar := range used {
			if !slices.Contains(scalars, scalar) {
				scalars = append(scalars, scalar)
			}
		}
	}
	for _, scalar := range scalars {
		if schema.Scalars == nil {
			schema.Scalars = map[string]*ir.ScalarDef{}
		}
		if schema.Scalars[scalar] == nil {
			schema.Scalars[scalar] = &ir.ScalarDef{Name: scalar}
		}
	}
	return scalars
}

// expandType adds td's owned fields and returns the scalars they name.
func expandType(td *ir.TypeDef, reg *registry.Registry) ([]string, error) {
	var scalars []string
	for _, use := range td.TableBehaviors() {
		where := fmt.Sprintf("type %s: field %s", td.Name, use.Field)
		if !use.FieldAnchored() {
			where = fmt.Sprintf("type %s: implements %s", td.Name, use.Name)
		}
		behavior, ok := reg.Behavior(use.Name)
		if !ok {
			return nil, fmt.Errorf("%s: behavior %s is not registered", where, use.Name)
		}
		for _, declared := range behavior.Fields {
			if declared.Volatile || (declared.Type == "" && !use.FieldAnchored()) {
				continue
			}
			value := use.FieldAnchored() && declared.Name == behavior.Value
			name := behaviorlayout.OwnedFieldName(behaviorlayout.UseMember(use), declared.Name, value)
			if existing := fieldNamed(td, name); existing != nil {
				if existing.Behavior != nil && existing.Behavior.Use == use.UseID() && existing.Behavior.Field == declared.Name {
					continue
				}
				return nil, fmt.Errorf("%s: behavior %s adds field %s, which the type already declares", where, use.Name, name)
			}
			field := &ir.FieldDef{
				Name:        name,
				Description: declared.Description,
				TypeRef:     ir.TypeRef{Name: declared.Type},
				// The value field is required, as the behavior sets it when
				// a row is created, unless its declaration says it is
				// optional (Reconcile's observed, absent before the first
				// observation, D104).
				Required: value && !declared.Optional,
				Behavior: &ir.FieldBehavior{
					Use:      use.UseID(),
					Behavior: use.Name,
					Field:    declared.Name,
					Value:    value,
				},
			}
			if column := behaviorlayout.OwnedColumn(use, declared.Name, value); column != codegen.ToSnakeCase(name) {
				field.SQLName = column
			}
			td.Fields = append(td.Fields, field)
			if !slices.Contains(builtins, declared.Type) && !slices.Contains(scalars, declared.Type) {
				scalars = append(scalars, declared.Type)
			}
		}
	}
	if !td.Versioned {
		td.Optimistic = true
	}
	return scalars, nil
}

// expandStorage adds the sub-tables td's uses keep and, when its
// behaviors arm deadlines, its deadline queue, each to schema's types, and
// returns the scalars their fields name. A type already added for the same
// marker is kept.
func expandStorage(schema *ir.Schema, td *ir.TypeDef, reg *registry.Registry) ([]string, error) {
	var scalars []string
	note := func(typeName string) {
		if !slices.Contains(builtins, typeName) && !slices.Contains(scalars, typeName) {
			scalars = append(scalars, typeName)
		}
	}
	var arming []string
	for _, use := range td.TableBehaviors() {
		behavior, ok := reg.Behavior(use.Name)
		if !ok {
			return nil, fmt.Errorf("type %s: behavior %s is not registered", td.Name, use.Name)
		}
		if behavior.ArmsDeadlines && !slices.Contains(arming, use.Name) {
			arming = append(arming, use.Name)
		}
		for _, table := range behavior.Tables {
			sub := subTable(td, use, table)
			if existing := schema.Types[sub.Name]; existing != nil {
				if existing.BehaviorTable != nil && *existing.BehaviorTable == *sub.BehaviorTable {
					continue
				}
				return nil, fmt.Errorf("type %s: behavior %s adds the table %s for its %s, which the schema also declares", td.Name, use.Name, sub.Name, table.Name)
			}
			schema.Types[sub.Name] = sub
			for _, fd := range sub.Fields {
				if fd.Relation == nil {
					note(fd.TypeRef.Name)
				}
			}
		}
	}
	if len(arming) == 0 {
		return scalars, nil
	}
	key := keyField(td)
	if key == nil {
		return nil, fmt.Errorf("type %s: its behaviors arm deadlines (%s), and it has no single key field", td.Name, strings.Join(arming, ", "))
	}
	queue := deadlineQueue(td, key, arming)
	if existing := schema.Types[queue.Name]; existing != nil {
		if !existing.BehaviorQueue.Same(queue.BehaviorQueue) {
			return nil, fmt.Errorf("type %s: its behaviors arm deadlines (%s), whose queue the build adds as %s, which the schema also declares", td.Name, strings.Join(arming, ", "), queue.Name)
		}
		return scalars, nil
	}
	schema.Types[queue.Name] = queue
	for _, fd := range queue.Fields {
		note(fd.TypeRef.Name)
	}
	return scalars, nil
}

// HeardBy are, for each DB table of schema whose writes another table's
// behaviors hear through a relation of it (registry.BehaviorListens: a
// Reactions use's allTerminal and anyTerminal), the tables that hear them,
// sorted by name, each once (D103): Task's are [Project] when Project's
// Reactions reads task.project. A use of a table that composes behaviors as
// a table alone listens; a listen that names no table of schema is left
// out, which the verification pass refuses.
func HeardBy(schema *ir.Schema) map[string][]string {
	if schema == nil {
		return nil
	}
	out := map[string][]string{}
	for _, name := range sortedNames(schema.Types) {
		td := schema.Types[name]
		if td == nil || td.Role != ir.RoleDBTable {
			continue
		}
		for _, use := range td.TableBehaviors() {
			for _, listen := range registry.BehaviorListens(use.Name, use.Config) {
				if heard := schema.Types[listen.Type]; heard == nil || heard.Role != ir.RoleDBTable {
					continue
				}
				if !slices.Contains(out[listen.Type], td.Name) {
					out[listen.Type] = append(out[listen.Type], td.Name)
				}
			}
		}
	}
	for name := range out {
		sort.Strings(out[name])
	}
	return out
}

// Hearing are the behaviors of td's table uses whose declarations hear the
// table's writes (registry.BehaviorDeclaration.HearsChanges), each once in
// list order (D103): what its change queue's marker names beside the
// tables that hear it through a relation.
func Hearing(td *ir.TypeDef, reg *registry.Registry) []string {
	var out []string
	for _, use := range td.TableBehaviors() {
		if behavior, ok := reg.Behavior(use.Name); ok && behavior.HearsChanges && !slices.Contains(out, use.Name) {
			out = append(out, use.Name)
		}
	}
	return out
}

// Emitting are the behaviors of td's table uses whose declarations list
// events (registry.BehaviorDeclaration.Events), each once in list order
// (D104): what its event queue's marker names.
func Emitting(td *ir.TypeDef, reg *registry.Registry) []string {
	var out []string
	for _, use := range td.TableBehaviors() {
		if behavior, ok := reg.Behavior(use.Name); ok && len(behavior.Events) > 0 && !slices.Contains(out, use.Name) {
			out = append(out, use.Name)
		}
	}
	return out
}

// expandEvents adds td's event queue to schema's types when its behaviors
// emit events (Emitting, D104), and returns the scalars the queue's fields
// name. A queue already added for the same marker is kept.
func expandEvents(schema *ir.Schema, td *ir.TypeDef, reg *registry.Registry) ([]string, error) {
	emitting := Emitting(td, reg)
	if len(emitting) == 0 {
		return nil, nil
	}
	key := keyField(td)
	if key == nil {
		return nil, fmt.Errorf("type %s: its behaviors emit events (%s), and it has no single key field", td.Name, strings.Join(emitting, ", "))
	}
	queue := eventQueue(td, key, emitting)
	if existing := schema.Types[queue.Name]; existing != nil {
		if !existing.BehaviorQueue.Same(queue.BehaviorQueue) {
			return nil, fmt.Errorf("type %s: its behaviors emit events (%s), whose queue the build adds as %s, which the schema also declares", td.Name, strings.Join(emitting, ", "), queue.Name)
		}
		return nil, nil
	}
	schema.Types[queue.Name] = queue
	var scalars []string
	for _, fd := range queue.Fields {
		if !slices.Contains(builtins, fd.TypeRef.Name) && !slices.Contains(scalars, fd.TypeRef.Name) {
			scalars = append(scalars, fd.TypeRef.Name)
		}
	}
	return scalars, nil
}

// eventQueue is the queue of the events td's behaviors emit about its rows
// (D104), emitting the behaviors that emit them: a D80 queue keyed by the
// row's key and serialized, its line kept in the order written
// (sqlutil.QueueSpec.InOrder, which an event queue's statements take from
// its BehaviorQueue's kind), so one row's events reach the app in the order
// the core wrote them and a failing one holds its row's later ones;
// appended, since an event is never folded into another; and pruned at
// once, its work done. An adapter writes one message per event of a change
// set, in the command's transaction, and leaves a row's messages when the
// row is deleted; a behavior worker hands each to the project's handler.
func eventQueue(td *ir.TypeDef, key *ir.FieldDef, emitting []string) *ir.TypeDef {
	return &ir.TypeDef{
		Name:          behaviorlayout.EventQueueTypeName(td.Name),
		Owner:         td.Owner,
		Comment:       "The events " + td.Name + "'s behaviors emit about its rows, one message per event, written in the command's transaction, which a behavior worker hands to the project's handler (D104).",
		Role:          ir.RoleEmbeddedStruct,
		Origin:        ir.OriginBehavior,
		BehaviorQueue: &ir.BehaviorQueue{Row: td.Name, Kind: ir.BehaviorQueueEvents, Behaviors: slices.Clone(emitting)},
		Queue:         &ir.QueueDef{Key: behaviorlayout.EventRowKeyField, Serialize: true, Retain: "0s"},
		Fields: []*ir.FieldDef{
			{Name: behaviorlayout.EventRowKeyField, Description: "The key of the " + td.Name + " row the event is about.", TypeRef: ir.TypeRef{Name: key.TypeRef.Name}, Required: true},
			{Name: behaviorlayout.EventUseField, Description: "The id of the use that emitted it: its anchor field, or the behavior's name for a use the table implements.", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: behaviorlayout.EventKindField, Description: "The event's kind, as its behavior declares it.", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: behaviorlayout.EventAtField, Description: "The time of the command that emitted it, in epoch milliseconds.", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Required: true},
			{Name: behaviorlayout.EventPayloadField, Description: "What the event says, as its kind's payloadSchema types it.", TypeRef: ir.TypeRef{Name: "Generic.JSON"}},
		},
	}
}

// expandChanges adds td's change queue to schema's types when its writes
// are heard: by its own behaviors (Hearing) or by the tables heardBy, which
// listen through a relation of it. It returns the scalars the queue's
// fields name. A queue already added for the same marker is kept.
func expandChanges(schema *ir.Schema, td *ir.TypeDef, reg *registry.Registry, heardBy []string) ([]string, error) {
	hearing := Hearing(td, reg)
	if len(hearing) == 0 && len(heardBy) == 0 {
		return nil, nil
	}
	who := slices.Clone(hearing)
	for _, table := range heardBy {
		who = append(who, table+"'s Reactions")
	}
	key := keyField(td)
	if key == nil {
		return nil, fmt.Errorf("type %s: its writes are heard (%s), and it has no single key field", td.Name, strings.Join(who, ", "))
	}
	queue := changeQueue(td, key, hearing, heardBy)
	if existing := schema.Types[queue.Name]; existing != nil {
		if !existing.BehaviorQueue.Same(queue.BehaviorQueue) {
			return nil, fmt.Errorf("type %s: its writes are heard (%s), whose queue the build adds as %s, which the schema also declares", td.Name, strings.Join(who, ", "), queue.Name)
		}
		return nil, nil
	}
	schema.Types[queue.Name] = queue
	var scalars []string
	for _, fd := range queue.Fields {
		if !slices.Contains(builtins, fd.TypeRef.Name) && !slices.Contains(scalars, fd.TypeRef.Name) {
			scalars = append(scalars, fd.TypeRef.Name)
		}
	}
	return scalars, nil
}

// changeQueue is the queue of the writes of td's rows its behaviors hear,
// or other tables' through a relation of it (D103): a D80 queue keyed by
// the row's key and serialized, so one row's messages are claimed one at a
// time, the head of its line first, its line kept in the order written
// (sqlutil.QueueSpec.InOrder, which a change queue's statements take from
// its BehaviorQueue's kind), appended (a write is never folded into
// another), and pruned at once, its work done. sqlgen's change trigger
// writes one message per insert, update and delete of a row, whoever makes
// it; a behavior worker runs it.
func changeQueue(td *ir.TypeDef, key *ir.FieldDef, hearing, heardBy []string) *ir.TypeDef {
	return &ir.TypeDef{
		Name:          behaviorlayout.ChangeQueueTypeName(td.Name),
		Owner:         td.Owner,
		Comment:       "The writes of " + td.Name + "'s rows its behaviors hear, one message per write, whoever makes it, which a trigger of the table writes and a behavior worker runs (D103).",
		Role:          ir.RoleEmbeddedStruct,
		Origin:        ir.OriginBehavior,
		BehaviorQueue: &ir.BehaviorQueue{Row: td.Name, Kind: ir.BehaviorQueueChanges, Behaviors: slices.Clone(hearing), HeardBy: slices.Clone(heardBy)},
		Queue:         &ir.QueueDef{Key: behaviorlayout.ChangeRowKeyField, Serialize: true, Retain: "0s"},
		Fields: []*ir.FieldDef{
			{Name: behaviorlayout.ChangeRowKeyField, Description: "The key of the " + td.Name + " row written.", TypeRef: ir.TypeRef{Name: key.TypeRef.Name}, Required: true},
			{Name: behaviorlayout.ChangeWriteField, Description: "What the write did: insert, update or delete.", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: behaviorlayout.ChangeAtField, Description: "When the write was made, in epoch milliseconds, by the database's clock.", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Required: true},
			{Name: behaviorlayout.ChangeBeforeField, Description: "The row's columns before the write, by column, as JSON; null for an insert.", TypeRef: ir.TypeRef{Name: "Generic.JSON"}},
			{Name: behaviorlayout.ChangeAfterField, Description: "The row's columns after the write, by column, as JSON; null for a delete.", TypeRef: ir.TypeRef{Name: "Generic.JSON"}},
		},
	}
}

// sortedNames are types' names, sorted.
func sortedNames(types map[string]*ir.TypeDef) []string {
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// subTable is the type of the sub-table table that the use keeps on the
// table td (D103): a table whose rows are a row's, deleted with it, keyed
// within it by the declared key. It holds no @key, so the build gives it
// the surrogate id every table without one takes, and one unique index
// of the row and the key. Its relation to the row is named for the row
// (rowFieldOf).
func subTable(td *ir.TypeDef, use ir.BehaviorRef, table registry.BehaviorTable) *ir.TypeDef {
	rowField := rowFieldOf(td, table)
	sub := &ir.TypeDef{
		Name:    behaviorlayout.SubTableTypeName(td.Name, use, table.Name),
		Owner:   td.Owner,
		Comment: table.Description,
		Role:    ir.RoleDBTable,
		Origin:  ir.OriginBehavior,
		BehaviorTable: &ir.BehaviorTable{
			Row:      td.Name,
			Use:      use.UseID(),
			Behavior: use.Name,
			Table:    table.Name,
			Key:      table.Key,
			RowField: rowField,
		},
		Fields: []*ir.FieldDef{{
			Name:        rowField,
			Description: "The " + td.Name + " row the rows are of; deleting it deletes them.",
			TypeRef:     ir.TypeRef{Name: td.Name},
			Required:    true,
			Relation:    &ir.RelationDef{Type: td.Name, OnDelete: "CASCADE"},
		}},
		Indexes: []ir.IndexDef{{Keys: []string{rowField, table.Key}, Unique: true, Origin: ir.OriginBehavior}},
	}
	for _, declared := range table.Fields {
		sub.Fields = append(sub.Fields, &ir.FieldDef{
			Name:        declared.Name,
			Description: declared.Description,
			TypeRef:     ir.TypeRef{Name: declared.Type},
			Required:    declared.Name == table.Key,
		})
	}
	return sub
}

// deadlineQueue is the queue of the deadlines td's behaviors arm (D103),
// arming the behaviors that arm them: one live message per row, use and
// deadline, which the adapters keep by deleting a key's messages before
// they arm it again, and which a partial unique index of the live messages
// backs; due when the deadline falls due. A done message is pruned at
// once: the record of a fire is the behavior's sub-table, not the queue.
func deadlineQueue(td *ir.TypeDef, key *ir.FieldDef, arming []string) *ir.TypeDef {
	return &ir.TypeDef{
		Name:          behaviorlayout.DeadlineQueueTypeName(td.Name),
		Owner:         td.Owner,
		Comment:       "The deadlines " + td.Name + "'s behaviors arm, one message per row, use and deadline, due when it falls due (D103).",
		Role:          ir.RoleEmbeddedStruct,
		Origin:        ir.OriginBehavior,
		BehaviorQueue: &ir.BehaviorQueue{Row: td.Name, Kind: ir.BehaviorQueueDeadlines, Behaviors: slices.Clone(arming)},
		Queue:         &ir.QueueDef{Retain: "0s"},
		Fields: []*ir.FieldDef{
			{Name: behaviorlayout.DeadlineRowKeyField, Description: "The key of the " + td.Name + " row the deadline is armed on.", TypeRef: ir.TypeRef{Name: key.TypeRef.Name}, Required: true},
			{Name: behaviorlayout.DeadlineUseField, Description: "The id of the use that armed it: its anchor field, or the behavior's name for a use the table implements.", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: behaviorlayout.DeadlineNameField, Description: "The deadline's name, the use's own.", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: behaviorlayout.DeadlineAtField, Description: "When it was armed to fall due, in epoch milliseconds.", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Required: true},
			{Name: behaviorlayout.DeadlinePayloadField, Description: "What the use armed it with, which its deadline hook reads.", TypeRef: ir.TypeRef{Name: "Generic.JSON"}},
		},
		Indexes: []ir.IndexDef{
			{Keys: []string{behaviorlayout.DeadlineRowKeyField, behaviorlayout.DeadlineUseField, behaviorlayout.DeadlineNameField}, Unique: true, Where: "state IN ('ready', 'claimed')", Origin: ir.OriginBehavior},
			{Keys: []string{behaviorlayout.DeadlineRowKeyField}, Origin: ir.OriginBehavior},
		},
	}
}

// keyField is td's one key field: its @key field, else its field named id,
// else nil when it has several @key fields or neither.
func keyField(td *ir.TypeDef) *ir.FieldDef {
	var keys []*ir.FieldDef
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			keys = append(keys, fd)
		}
	}
	switch len(keys) {
	case 1:
		return keys[0]
	case 0:
		return fieldNamed(td, "id")
	}
	return nil
}

// rowFieldOf is the field a sub-table of td names its row by: td's name
// with its first letter in lower case (Ticket: ticket), with Row after it
// as often as that name, or its foreign key's column, is one the table
// declares or the surrogate id the build adds, so a table named after a
// declared field keeps both (Outcome: outcomeRow beside Timers' outcome).
func rowFieldOf(td *ir.TypeDef, table registry.BehaviorTable) string {
	fields, columns := map[string]bool{"id": true}, map[string]bool{"id": true}
	for _, declared := range table.Fields {
		fields[declared.Name] = true
		columns[codegen.ToSnakeCase(declared.Name)] = true
	}
	name := lowerFirst(td.Name)
	for fields[name] || columns[codegen.ToSnakeCase(name)+"_id"] {
		name += "Row"
	}
	return name
}

// lowerFirst is name with its first letter in lower case.
func lowerFirst(name string) string {
	if name == "" {
		return name
	}
	r := []rune(name)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func fieldNamed(td *ir.TypeDef, name string) *ir.FieldDef {
	for _, fd := range td.Fields {
		if fd != nil && fd.Name == name {
			return fd
		}
	}
	return nil
}
