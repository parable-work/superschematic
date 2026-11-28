package verify

import (
	"slices"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// FoldedBehaviors are the behaviors D103 folds into schema features, by
// name, each with what replaces it (registry.FoldedBehaviors). No registry
// declares them since the engine that ran them went (D103, #587), so the
// refusal names them itself: checkFoldedBehaviors refuses each a type
// composes, in every kind, naming its replacement, and the data forms'
// reader refuses one in the same words before it would say that no
// behavior of the name is registered.
var FoldedBehaviors = registry.FoldedBehaviors

// IsFoldedBehavior reports whether name is a behavior D103 folds into a
// schema feature (FoldedBehaviors).
func IsFoldedBehavior(name string) bool {
	_, ok := registry.FoldedBehavior(name)
	return ok
}

// checkFoldedBehaviors refuses each folded behavior td composes, on the
// type, by implements or on a field, naming what replaces it, and reports
// whether it refused one. checkBehaviors leaves a type it refuses
// unchecked otherwise: its other behaviors' configs may name a link of the
// Links it composes, which the replacement renames.
func checkFoldedBehaviors(td *ir.TypeDef, r *Result) bool {
	var refused []string
	for _, ref := range td.Behaviors {
		if replacement, ok := registry.FoldedBehavior(ref.Name); ok && !slices.Contains(refused, ref.Name) {
			refused = append(refused, ref.Name)
			r.errorf(td.Owner, "type %s composes %s, which is no longer a behavior (D103): %s", td.Name, ref.Name, replacement)
		}
	}
	return len(refused) > 0
}
