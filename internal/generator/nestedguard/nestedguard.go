// Package nestedguard stops a generator that does not render arrays of
// arrays (T[][]) yet, so it fails the build instead of emitting T[].
//
// Each such generator calls Check once, at its entry, in a block marked
// "nested-arrays guard". The change that teaches a generator T[][] deletes
// its own block and nothing else; when no block remains, this package goes
// too.
package nestedguard

import (
	"fmt"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// Source is what a generator reads its field types from: an *ir.Schema, or
// the *apigen.APIOutput the SDK generators start from.
type Source interface {
	FindArrayOfArrays() (where string, found bool)
}

// Check returns "<generator> does not support arrays of arrays yet" when
// source declares T[][], naming the first place it does, and nil otherwise.
func Check(generator string, source Source) error {
	if where, found := source.FindArrayOfArrays(); found {
		return unsupported(generator, where)
	}
	return nil
}

// CheckWithDependencies is Check over a schema and the loaded dependency
// schemas a generator also renders types from, in dependency name order.
func CheckWithDependencies(generator string, schema *ir.Schema, dependencies map[string]*ir.Schema) error {
	if err := Check(generator, schema); err != nil {
		return err
	}
	names := make([]string, 0, len(dependencies))
	for name := range dependencies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if where, found := dependencies[name].FindArrayOfArrays(); found {
			return unsupported(generator, name+" "+where)
		}
	}
	return nil
}

func unsupported(generator, where string) error {
	return fmt.Errorf("%s does not support arrays of arrays yet (%s)", generator, where)
}
