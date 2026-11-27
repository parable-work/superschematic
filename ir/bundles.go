package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// BundleRef is one bundle a table implements (D103, step 5): a preset
// wiring of existing behaviors that exposes only the settings that vary.
// A bundle defines no rules of its own. The loader expands it into its
// parts, each an ordinary use of the table ([BehaviorRef], its Bundle
// set), so the behaviors' core never meets one: `implements
// Reservation<Booking, { resource: "desk"; ... }>` in TypeScript, `{
// "name": "Reservation", "settings": { "resource": "desk", ... } }` in
// the data forms.
type BundleRef struct {
	// Name is the bundle's registered name: Reservation.
	Name string `json:"name" yaml:"name"`

	// Settings are the values the table gives the bundle's settings, in
	// canonical JSON ([CanonicalJSON]), so they are the same bytes from
	// every authoring form. Empty when the table gives none; settings of
	// {} are stored as empty.
	Settings json.RawMessage `json:"settings,omitempty" yaml:"settings,omitempty"`
}

// CanonicalizeBundles rewrites every bundle's settings in refs with
// [CanonicalJSON] and empties settings that canonicalize to {}, as
// [CanonicalizeBehaviors] does for a use's config.
func CanonicalizeBundles(refs []BundleRef) error {
	for i := range refs {
		if len(refs[i].Settings) == 0 {
			continue
		}
		canonical, err := CanonicalJSON(refs[i].Settings)
		if err != nil {
			return fmt.Errorf("bundle %q settings: %w", refs[i].Name, err)
		}
		if bytes.Equal(canonical, emptyObject) {
			canonical = nil
		}
		refs[i].Settings = canonical
	}
	return nil
}

// FromBundle reports whether a bundle added the use (D103, step 5).
func (r BehaviorRef) FromBundle() bool {
	return r.Bundle != ""
}

// BundleParts returns td's uses the bundle named name added, in list order
// (D103, step 5).
func (td *TypeDef) BundleParts(name string) []BehaviorRef {
	return td.behaviorsWhere(func(ref BehaviorRef) bool { return ref.Bundle == name })
}

// Bundle returns td's bundle named name, or false.
func (td *TypeDef) Bundle(name string) (BundleRef, bool) {
	if td == nil {
		return BundleRef{}, false
	}
	for _, ref := range td.Bundles {
		if ref.Name == name {
			return ref, true
		}
	}
	return BundleRef{}, false
}
