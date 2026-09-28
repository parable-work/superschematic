// Package scalarcore lists the scalars of the scalar core this module links,
// read from the core's generated metadata table. The parse and validate
// Default*Registry constructors derive their name sets from it instead of
// carrying hand-maintained lists.
//
// The linked core is github.com/parable-work/superscalar/go, so Default()
// is the generic set; an extension's scalars arrive through its runtime
// registry ([runtime.WithRegistry]).
package scalarcore

import (
	"slices"
	"sort"

	scalarlib "github.com/parable-work/superscalar/go"
)

// Names returns every canonical scalar name the linked core knows, sorted. The
// result is a copy, so a caller may change it without touching the core's list.
func Names() []string {
	names := slices.Clone(scalarlib.VALID_SCALARS)
	sort.Strings(names)
	return names
}

// ParseNames returns the sorted names whose metadata marks a custom parse
// step, the only names a Parser consults its parse registry for.
func ParseNames() []string {
	return namesWhere(func(m *scalarlib.ScalarMetadata) bool { return m.HasCustomParse })
}

// NormalizeNames returns the sorted names whose metadata marks a custom
// normalize step, the only names a Parser consults its normalize registry
// for.
func NormalizeNames() []string {
	return namesWhere(func(m *scalarlib.ScalarMetadata) bool { return m.HasCustomNormalize })
}

func namesWhere(keep func(*scalarlib.ScalarMetadata) bool) []string {
	var names []string
	for name, meta := range scalarlib.ScalarMetadataByCanonical {
		if keep(meta) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
