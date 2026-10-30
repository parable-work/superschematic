package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
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
// behavior on any number of fields. A type uses one form or the other,
// never both.
type BehaviorRef struct {
	// Name is the registered behavior name: bare for a core behavior,
	// "<extension>.<Name>" for an extension's.
	Name string `json:"name" yaml:"name"`

	// Field is the anchor field's name for a field anchor (D103): the field
	// whose type is the behavior, which holds the behavior's value field
	// and prefixes its other fields' columns and its operations. Empty for
	// a type anchor.
	Field string `json:"field,omitempty" yaml:"field,omitempty"`

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

// TypeBehaviors returns td's type-anchored uses, in list order: the
// @behavior decorator's (D16).
func (td *TypeDef) TypeBehaviors() []BehaviorRef {
	return td.behaviorsWhere(false)
}

// FieldBehaviors returns td's field-anchored uses, in list order (D103):
// each a field whose type is a behavior.
func (td *TypeDef) FieldBehaviors() []BehaviorRef {
	return td.behaviorsWhere(true)
}

func (td *TypeDef) behaviorsWhere(fieldAnchored bool) []BehaviorRef {
	if td == nil {
		return nil
	}
	var out []BehaviorRef
	for _, ref := range td.Behaviors {
		if ref.FieldAnchored() == fieldAnchored {
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
// type (D58): one that composes a type-anchored behavior, whose rows the
// engine keeps in its own tables, so it gets no table, no repository and
// no migration of its own. A type whose uses are all field-anchored is not
// one: it stays a table (D103).
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
