package registry

// FoldedBehaviors are the behaviors D103 folds into schema features, by
// name, each with what replaces it: a storage shape or a query is a schema
// feature, not a behavior (D103, "What a behavior is"). No registry
// declares them since the engine that ran them went (D103, #587), so the
// readers and the verification pass refuse each by this name, naming its
// replacement, where they would otherwise say only that no behavior of
// the name is registered.
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

// FoldedBehavior is what replaces name, a behavior D103 folds into a schema
// feature (FoldedBehaviors), and whether it is one.
func FoldedBehavior(name string) (string, bool) {
	replacement, ok := FoldedBehaviors[name]
	return replacement, ok
}
