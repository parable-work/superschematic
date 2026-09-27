package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// BehaviorRef is one behavior a type composes: code that adds fields,
// operations, checks and storage to the type when an engine runs the schema
// (D16 in docs/DECISIONS.md). A registry declares each behavior by name; the
// loader checks a reference against that declaration. A type lists a
// behavior at most once, and the list order is the order its checks run in.
type BehaviorRef struct {
	// Name is the registered behavior name: bare for a core behavior,
	// "<extension>.<Name>" for an extension's.
	Name string `json:"name" yaml:"name"`

	// Config is the type's configuration of the behavior in canonical JSON
	// ([CanonicalJSON]), as extension data is, so it is the same bytes from
	// every authoring form. Empty when the type configures nothing; a
	// config of {} is stored as empty, as [SetExtension] drops {}.
	Config json.RawMessage `json:"config,omitempty" yaml:"config,omitempty"`
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
			return fmt.Errorf("behavior %q config: %w", refs[i].Name, err)
		}
		if bytes.Equal(canonical, emptyObject) {
			canonical = nil
		}
		refs[i].Config = canonical
	}
	return nil
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
