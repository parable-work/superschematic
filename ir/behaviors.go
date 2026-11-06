package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// BehaviorRef is one behavior a type composes: code that adds fields,
// operations, checks and storage to the type (D16 in docs/DECISIONS.md). A
// registry declares each behavior by name; the loader checks a reference
// against that declaration. The list order is the order its checks run in.
//
// A use is anchored on the whole type or on one field (D103). A type anchor
// (Field empty) is the @behavior decorator's form (D16): it makes a DB type
// an engine type, whose rows the engine keeps (D58), until D103's step 7
// removes engine types, and a type lists such a behavior at most once. A
// field anchor is the field-type form, `status: Workflow<{ ... }>`: the type
// stays a table, the behavior's declared fields become columns of it that
// the loader adds as fields (see [FieldBehavior]), and a type may carry one
// behavior on any number of fields. A type anchor with Implements set is
// the implements form on a table, `implements Timers<Ticket, { ... }>`: the
// type stays a table, as with a field anchor, and lists the behavior at
// most once. A type takes @behavior or the table's forms (implements and
// field types), never both.
type BehaviorRef struct {
	// Name is the registered behavior name: bare for a core behavior,
	// "<extension>.<Name>" for an extension's.
	Name string `json:"name" yaml:"name"`

	// Field is the anchor field's name for a field anchor (D103): the field
	// whose type is the behavior, which holds the behavior's value field
	// and prefixes its other fields' columns and its operations. Empty for
	// a type anchor.
	Field string `json:"field,omitempty" yaml:"field,omitempty"`

	// Implements is true for a use a DB table implements, the type-anchored
	// form on a table that stays a table (D103): `implements Timers<Ticket,
	// Config>`. Field is empty. A type anchor without it is @behavior's
	// (D16), which makes the type an engine type (D58).
	Implements bool `json:"implements,omitempty" yaml:"implements,omitempty"`

	// Config is the type's configuration of the behavior in canonical JSON
	// ([CanonicalJSON]), as extension data is, so it is the same bytes from
	// every authoring form. Empty when the type configures nothing; a
	// config of {} is stored as empty, as [SetExtension] drops {}.
	Config json.RawMessage `json:"config,omitempty" yaml:"config,omitempty"`
}

// FieldAnchored reports whether the use is anchored on a field (D103).
func (r BehaviorRef) FieldAnchored() bool {
	return r.Field != ""
}

// UseID is the use's id, by which the core addresses its fields,
// sub-tables, operations and create parameters (D103): the anchor field's
// name for a field anchor, the behavior's name for a type anchor.
func (r BehaviorRef) UseID() string {
	if r.Field != "" {
		return r.Field
	}
	return r.Name
}

// FieldBehavior marks a field a behavior owns (D103): a column of the
// type's table that a field-anchored use's declared field takes, which the
// loader adds after the readers run, so every authoring form gets it. Only
// the behavior's operations change it; the generated repositories read it
// and leave it out of their inputs, and a raw SQL write of it is never
// refused. A field with Behavior set is the loader's, as one with an Origin
// is: the data forms have no key for it, and a schema writer writes the use
// in its place.
type FieldBehavior struct {
	// Use is the id of the use that owns the field: its anchor field's
	// name ([BehaviorRef.UseID]).
	Use string `json:"use" yaml:"use"`

	// Behavior is the use's registered behavior name.
	Behavior string `json:"behavior" yaml:"behavior"`

	// Field is the field's name in the behavior's declaration, as the core
	// names it in a row's owned fields and a change set (enteredAt).
	Field string `json:"field" yaml:"field"`

	// Value is true for the use's value field, the one the declaration's
	// value names: the anchor field itself, whose name and column are the
	// anchor's.
	Value bool `json:"value,omitempty" yaml:"value,omitempty"`
}

// CanonicalizeBehaviors rewrites every config in refs with [CanonicalJSON]
// and empties a config that canonicalizes to {}. The data-form readers and
// the TypeScript walker run it, so the persisted IR does not depend on the
// authoring form.
func CanonicalizeBehaviors(refs []BehaviorRef) error {
	for i := range refs {
		if len(refs[i].Config) == 0 {
			continue
		}
		canonical, err := CanonicalJSON(refs[i].Config)
		if err != nil {
			if refs[i].Field != "" {
				return fmt.Errorf("behavior %q on field %s config: %w", refs[i].Name, refs[i].Field, err)
			}
			return fmt.Errorf("behavior %q config: %w", refs[i].Name, err)
		}
		if bytes.Equal(canonical, emptyObject) {
			canonical = nil
		}
		refs[i].Config = canonical
	}
	return nil
}

// TypeBehaviors returns td's @behavior uses, in list order (D16): those
// anchored on the type and not implemented by a table. An implements use
// is left out, so [TypeDef.IsEngineType] and every engine path see only
// the decorator's form.
func (td *TypeDef) TypeBehaviors() []BehaviorRef {
	return td.behaviorsWhere(func(ref BehaviorRef) bool { return !ref.FieldAnchored() && !ref.Implements })
}

// FieldBehaviors returns td's field-anchored uses, in list order (D103):
// each a field whose type is a behavior.
func (td *TypeDef) FieldBehaviors() []BehaviorRef {
	return td.behaviorsWhere(BehaviorRef.FieldAnchored)
}

// ImplementsBehaviors returns td's implements uses, in list order (D103):
// each a behavior the table implements, anchored on the type.
func (td *TypeDef) ImplementsBehaviors() []BehaviorRef {
	return td.behaviorsWhere(func(ref BehaviorRef) bool { return !ref.FieldAnchored() && ref.Implements })
}

// TableBehaviors returns td's uses on a table that stays a table (D103):
// each implements use and each field use, in list order.
func (td *TypeDef) TableBehaviors() []BehaviorRef {
	return td.behaviorsWhere(func(ref BehaviorRef) bool { return ref.FieldAnchored() || ref.Implements })
}

func (td *TypeDef) behaviorsWhere(keep func(BehaviorRef) bool) []BehaviorRef {
	if td == nil {
		return nil
	}
	var out []BehaviorRef
	for _, ref := range td.Behaviors {
		if keep(ref) {
			out = append(out, ref)
		}
	}
	return out
}

// FieldBehavior returns td's use anchored on the field named field, or
// false when the field anchors none.
func (td *TypeDef) FieldBehavior(field string) (BehaviorRef, bool) {
	if td == nil || field == "" {
		return BehaviorRef{}, false
	}
	for _, ref := range td.Behaviors {
		if ref.Field == field {
			return ref, true
		}
	}
	return BehaviorRef{}, false
}

// IsEngineType reports whether td, a type of a DB schema, is an engine
// type (D58): one that composes a behavior by @behavior, whose rows the
// engine keeps in its own tables, so it gets no table, no repository and
// no migration of its own. A type whose uses are all implements uses and
// field uses is not one: it stays a table (D103).
func (td *TypeDef) IsEngineType() bool {
	return len(td.TypeBehaviors()) > 0
}

// OwnedFields returns td's fields a behavior owns ([FieldDef.Owned]), in
// field order.
func (td *TypeDef) OwnedFields() []*FieldDef {
	if td == nil {
		return nil
	}
	var out []*FieldDef
	for _, f := range td.Fields {
		if f.Owned() {
			out = append(out, f)
		}
	}
	return out
}

// Owned reports whether a behavior owns the field (D103): a column the
// loader added for a field-anchored use, which only the behavior's
// operations change.
func (f *FieldDef) Owned() bool {
	return f != nil && f.Behavior != nil
}

// FindBehavior returns the first type, in name order over Types and then
// Inputs, that composes a behavior, with the name of its first behavior. A
// reader that cannot render behaviors calls it to refuse the schema rather
// than drop the fields and operations they add.
func (s *Schema) FindBehavior() (typeName, behavior string, found bool) {
	if s == nil {
		return "", "", false
	}
	for _, types := range []map[string]*TypeDef{s.Types, s.Inputs} {
		for _, name := range sortedStringMapKeys(types) {
			if td := types[name]; td != nil && len(td.Behaviors) > 0 {
				return td.Name, td.Behaviors[0].Name, true
			}
		}
	}
	return "", "", false
}

// FindTableBehavior returns the first type, in name order over Types and
// then Inputs, with a table behavior ([TypeDef.TableBehaviors]), and its
// first such use (D103): what a generator that renders a table but not
// its behaviors refuses.
func (s *Schema) FindTableBehavior() (typeName string, use BehaviorRef, found bool) {
	if s == nil {
		return "", BehaviorRef{}, false
	}
	for _, types := range []map[string]*TypeDef{s.Types, s.Inputs} {
		for _, name := range sortedStringMapKeys(types) {
			if uses := types[name].TableBehaviors(); len(uses) > 0 {
				return types[name].Name, uses[0], true
			}
		}
	}
	return "", BehaviorRef{}, false
}

// BehaviorTable marks a sub-table the loader adds for a use's declared
// table (D103): a child table of the row, with a foreign key to it that
// deletes it with the row, which only the behavior's operations write and
// for which no repository is generated. Its rows are keyed within their
// row by the declared key.
type BehaviorTable struct {
	// Row is the row's type: Ticket.
	Row string `json:"row" yaml:"row"`

	// Use is the use's id ([BehaviorRef.UseID]): Timers.
	Use string `json:"use" yaml:"use"`

	// Behavior is the use's registered behavior name: Timers.
	Behavior string `json:"behavior" yaml:"behavior"`

	// Table is the table's name in the behavior's declaration: deadlines.
	Table string `json:"table" yaml:"table"`

	// Key is the declared key field: name.
	Key string `json:"key" yaml:"key"`

	// RowField is the sub-table's relation field to the row: ticket.
	RowField string `json:"rowField" yaml:"rowField"`
}

// BehaviorQueueDeadlines is the kind of the queue a table whose behaviors
// arm deadlines is given (D103): one message per row, use and deadline,
// due when the deadline falls due.
const BehaviorQueueDeadlines = "deadlines"

// BehaviorQueueChanges is the kind of the queue a table whose writes its
// behaviors hear is given (D103): one message per write of a row, whoever
// makes it, which the table's change trigger writes.
const BehaviorQueueChanges = "changes"

// BehaviorQueueEvents is the kind of the queue a table whose behaviors emit
// events is given (D104): one message per event a behavior emits about a
// row, written in the command's own transaction, which a behavior worker
// hands to the project's handler.
const BehaviorQueueEvents = "events"

// BehaviorQueue marks a queue the loader adds for a table's behaviors
// (D103): the queue of the deadlines they arm ([BehaviorQueueDeadlines]),
// of the writes of its rows they hear ([BehaviorQueueChanges]), or of the
// events they emit about its rows ([BehaviorQueueEvents], D104), which a
// @behaviorWorker of an API over the table's database runs.
type BehaviorQueue struct {
	// Row is the table's type: Ticket.
	Row string `json:"row" yaml:"row"`

	// Kind is what the queue holds: [BehaviorQueueDeadlines],
	// [BehaviorQueueChanges] or [BehaviorQueueEvents].
	Kind string `json:"kind" yaml:"kind"`

	// Behaviors are the behaviors of the table's uses whose messages it
	// holds, each once in list order, which a generator's refusal of the
	// worker that runs them names: on a deadline queue those that arm
	// deadlines (Timers), on a change queue those that hear the table's
	// writes (Timers, Reactions), on an event queue those that emit events
	// (Schedules).
	Behaviors []string `json:"behaviors,omitempty" yaml:"behaviors,omitempty"`

	// HeardBy are the tables of the schema whose behaviors hear the table's
	// writes through a relation of it (Project's Reactions hearing Task
	// through task.project), sorted by name; set on a change queue alone.
	HeardBy []string `json:"heardBy,omitempty" yaml:"heardBy,omitempty"`
}

// Same reports whether q and other mark the same queue: of one table, of
// one kind, for the same behaviors, heard by the same tables.
func (q *BehaviorQueue) Same(other *BehaviorQueue) bool {
	if q == nil || other == nil {
		return q == other
	}
	return q.Row == other.Row && q.Kind == other.Kind && slices.Equal(q.Behaviors, other.Behaviors) && slices.Equal(q.HeardBy, other.HeardBy)
}

// BehaviorQueueOf returns the queue the loader added to s for the table
// row's behaviors of kind, or nil.
func (s *Schema) BehaviorQueueOf(row, kind string) *TypeDef {
	if s == nil {
		return nil
	}
	for _, name := range sortedStringMapKeys(s.Types) {
		if td := s.Types[name]; td != nil && td.BehaviorQueue != nil && td.BehaviorQueue.Row == row && td.BehaviorQueue.Kind == kind {
			return td
		}
	}
	return nil
}

// FindFieldBehavior returns the first type, in name order over Types and
// then Inputs, that composes a field-anchored behavior, with its first such
// use's field and behavior (D103). A generator that renders a table but not
// the columns and operations a field behavior adds calls it to refuse the
// schema rather than drop them.
func (s *Schema) FindFieldBehavior() (typeName, field, behavior string, found bool) {
	if s == nil {
		return "", "", "", false
	}
	for _, types := range []map[string]*TypeDef{s.Types, s.Inputs} {
		for _, name := range sortedStringMapKeys(types) {
			if uses := types[name].FieldBehaviors(); len(uses) > 0 {
				return types[name].Name, uses[0].Field, uses[0].Name, true
			}
		}
	}
	return "", "", "", false
}
