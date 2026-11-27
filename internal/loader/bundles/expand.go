// Package bundles expands the bundles a table implements into their parts
// (step 5 of D103, #587). A bundle defines no rules: it
// presets the wiring of existing behaviors and exposes only the settings
// that vary, and the loader adds its parts to the table as the table's own
// uses would be, so the verification pass, the behaviors' core and every
// generator meet ordinary uses and never a bundle.
//
// The data forms name a bundle and its settings, `{ "name":
// "Reservation", "settings": { ... } }` (ir.TypeDef.Bundles), and Expand
// adds the uses its registered declaration lists, each placeholder of a
// setting replaced by the table's value (registry.Bundle.Expand). The
// TypeScript form, `implements Reservation<Booking, { ... }>`, is read
// through the checker: the reader substitutes the settings into the parts'
// configs of the bundle's type alias, @superschematic/db's for the core's
// bundles, and records the parts itself. For a registered bundle Expand
// then holds those parts to its declaration's, so the two definitions of
// a core bundle cannot drift apart; a bundle a project declares in its own
// schema files is TypeScript's alone, and its parts stand as the reader
// wrote them.
//
// A part is named as the use it is: a part the table implements by its
// behavior (holdsState, the operation set's holds operations), a part
// anchored on a field by the field the bundle names (status,
// statusEnteredAt, statusTransition). The bundle's name is in no column,
// table or operation.
package bundles

import (
	"bytes"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Expand adds to each type of schema the uses its bundles add, after the
// type's own uses, bundle by bundle in the order the type lists them and
// each bundle's parts in its declaration's order. It refuses a bundle on a
// type that is no table of a DB schema, a bundle the registry does not
// declare, one the type lists twice, settings its settings schema does
// not take, a part whose config its behavior's config schema does not
// take, a part whose behavior the type already implements or whose field
// the type already declares or anchors another use on, and parts the
// TypeScript reader recorded that differ from the declaration's. Parts
// already added, as an IR read back holds them, are kept.
func Expand(schema *ir.Schema, reg *registry.Registry) error {
	if schema == nil {
		return nil
	}
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	var errs []string
	for _, name := range names {
		td := schema.Types[name]
		if td == nil || len(td.Bundles) == 0 {
			continue
		}
		if err := expandType(schema, td, reg); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}

// expandType adds td's bundles' parts.
func expandType(schema *ir.Schema, td *ir.TypeDef, reg *registry.Registry) error {
	first := td.Bundles[0].Name
	if schema.Kind != ir.SchemaKindDB || td.Role != ir.RoleDBTable || td.JsonField {
		return fmt.Errorf("type %s implements the bundle %s, and a bundle is implemented by a DB table, which %s is not (D103)", td.Name, first, td.Name)
	}
	if len(td.TypeBehaviors()) > 0 {
		return fmt.Errorf("type %s implements the bundle %s beside @behavior %s, which a DB type does not take: a bundle's parts are a table's uses (D103)", td.Name, first, td.TypeBehaviors()[0].Name)
	}
	var seen []string
	for _, ref := range td.Bundles {
		where := fmt.Sprintf("type %s: bundle %s", td.Name, ref.Name)
		if slices.Contains(seen, ref.Name) {
			return fmt.Errorf("type %s implements the bundle %s twice", td.Name, ref.Name)
		}
		seen = append(seen, ref.Name)
		bundle, ok := reg.Bundle(ref.Name)
		if !ok {
			known := "none are registered"
			if all := reg.Bundles(); len(all) > 0 {
				names := make([]string, len(all))
				for i, b := range all {
					names[i] = b.Name
				}
				known = "registered: " + strings.Join(names, ", ")
			}
			return fmt.Errorf("%s is not a registered bundle (%s)", where, known)
		}
		parts, err := bundle.Expand(ref.Settings)
		if err != nil {
			return fmt.Errorf("type %s: %w", td.Name, err)
		}
		for i, part := range parts {
			behavior, ok := reg.Behavior(part.Name)
			if !ok {
				return fmt.Errorf("%s: part %s: behavior %s is not registered", where, label(part), part.Name)
			}
			if err := behavior.ValidateConfig(part.Config); err != nil {
				return fmt.Errorf("%s: part %s: %w", where, label(part), err)
			}
			parts[i] = part
		}
		if recorded := td.BundleParts(ref.Name); len(recorded) > 0 {
			if err := sameParts(where, recorded, parts); err != nil {
				return err
			}
			continue
		}
		for _, part := range parts {
			if err := conflict(td, part, where); err != nil {
				return err
			}
			td.Behaviors = append(td.Behaviors, part)
		}
	}
	return nil
}

// sameParts holds the parts the TypeScript reader recorded for a bundle,
// read from its type alias through the checker, to those its registered
// declaration expands to: the same uses in the same order with the same
// configs.
func sameParts(where string, recorded, declared []ir.BehaviorRef) error {
	describe := func(refs []ir.BehaviorRef) string {
		out := make([]string, len(refs))
		for i, ref := range refs {
			out[i] = label(ref)
		}
		return strings.Join(out, ", ")
	}
	if len(recorded) != len(declared) {
		return fmt.Errorf("%s: the TypeScript form's parts (%s) are not the registered declaration's (%s)", where, describe(recorded), describe(declared))
	}
	for i := range recorded {
		r, d := recorded[i], declared[i]
		if r.Name != d.Name || r.Field != d.Field || r.Implements != d.Implements {
			return fmt.Errorf("%s: the TypeScript form's parts (%s) are not the registered declaration's (%s)", where, describe(recorded), describe(declared))
		}
		if !bytes.Equal(r.Config, d.Config) {
			return fmt.Errorf("%s: part %s: the TypeScript form's config %s is not the registered declaration's %s", where, label(r), r.Config, d.Config)
		}
	}
	return nil
}

// conflict refuses a part td cannot take beside its uses: a behavior it
// implements already, or a field it declares or anchors a use on.
func conflict(td *ir.TypeDef, part ir.BehaviorRef, where string) error {
	if part.FieldAnchored() {
		for _, fd := range td.Fields {
			if fd != nil && fd.Name == part.Field && (!fd.Owned() || fd.Behavior.Use != part.Field) {
				return fmt.Errorf("%s: part %s anchors %s on field %s, which the type declares; leave the field to the bundle (D103)", where, label(part), part.Name, part.Field)
			}
		}
		if use, ok := td.FieldBehavior(part.Field); ok {
			return fmt.Errorf("%s: part %s anchors %s on field %s, which %s anchors %s on already (D103)", where, label(part), part.Name, part.Field, origin(use), use.Name)
		}
		return nil
	}
	for _, use := range td.ImplementsBehaviors() {
		if use.Name == part.Name {
			return fmt.Errorf("%s: part %s: %s implements %s already, and a table implements a behavior at most once (D103)", where, label(part), origin(use), part.Name)
		}
	}
	return nil
}

// origin names who added a use: the type, or a bundle of it.
func origin(use ir.BehaviorRef) string {
	if use.Bundle != "" {
		return "the bundle " + use.Bundle
	}
	return "the type"
}

// label names a part: its behavior, and its field when it is anchored on
// one.
func label(part ir.BehaviorRef) string {
	if part.FieldAnchored() {
		return part.Name + " on field " + part.Field
	}
	return part.Name
}
