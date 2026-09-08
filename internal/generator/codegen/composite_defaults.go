package codegen

import (
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// CompositeDefaultInfo is the language-neutral rendering input for one
// validated platform default.
type CompositeDefaultInfo struct {
	Name          string
	Type          string
	CanonicalJSON string
}

// ExtractCompositeDefaults returns platform defaults in target-type order.
func ExtractCompositeDefaults(schema *ir.Schema) []CompositeDefaultInfo {
	if schema == nil || len(schema.CompositeDefaults) == 0 {
		return nil
	}
	names := make([]string, 0, len(schema.CompositeDefaults))
	for name := range schema.CompositeDefaults {
		names = append(names, name)
	}
	sort.Strings(names)

	defaults := make([]CompositeDefaultInfo, 0, len(names))
	for _, name := range names {
		def := schema.CompositeDefaults[name]
		if def == nil {
			continue
		}
		defaults = append(defaults, CompositeDefaultInfo{
			Name:          name,
			Type:          def.Type,
			CanonicalJSON: def.CanonicalJSON,
		})
	}
	return defaults
}
