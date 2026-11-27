package verify

import (
	"slices"

	ir "github.com/parable-work/superschematic/ir"
)

// FoldedBehaviors are the behaviors D103 folds into schema features, by
// name, each with what replaces it: a storage shape or a query is a
// schema feature, not a behavior (D103, "What a behavior is"). Their
// declarations stay registered while the engine that runs them is in the
// tree, so its crates' declarations still match the registry's, but no
// schema composes one: checkFoldedBehaviors refuses each, in every kind,
// naming its replacement.
var FoldedBehaviors = map[string]string{
	"Links": "a relation replaces it: give the type a field of Relation<Target> " +
		`(in the data forms, a field with "relation": {"type": "Target"}), ` +
		"and name that field where a config named a link",
	"Search": "@searchField replaces it: mark each field to search @searchField " +
		`(in the data forms, "searchField": true), and keep a vector in a ` +
		"Validate<Embedding.Vector, { dimensions }> field (D84)",
	"Queue": "@queue replaces it: claimable work is a queue's messages, which " +
		"its workers claim (D53, D80); to claim rows of a table, read the rows " +
		"to claim and take Lease's acquire on the first (D104)",
	"Branches": "@versioned replaces it: a version graph's members are " +
		"@versioned tables of a @versionGraph (D17, D19)",
}

// IsFoldedBehavior reports whether name is a behavior D103 folds into a
// schema feature (FoldedBehaviors).
func IsFoldedBehavior(name string) bool {
	_, ok := FoldedBehaviors[name]
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
		if replacement, ok := FoldedBehaviors[ref.Name]; ok && !slices.Contains(refused, ref.Name) {
			refused = append(refused, ref.Name)
			r.errorf(td.Owner, "type %s composes %s, which is no longer a behavior (D103): %s", td.Name, ref.Name, replacement)
		}
	}
	return len(refused) > 0
}
