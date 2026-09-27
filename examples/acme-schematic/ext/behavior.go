package ext

import (
	_ "embed"
	"encoding/json"

	"github.com/parable-work/superschematic/registry"
)

// RatingBehavior is acme's behavior: shoppers rate a catalog item from one
// star to the maximum a type configures. It adds the ratingCount and
// ratingAverage fields and the rate and ratingSummary operations. An
// extension's behavior is named <extension>.<Name>.
const RatingBehavior = Name + ".Rating"

// ratingDeclaration is the behavior's declaration: one JSON file beside
// this package, which embeds it. The compiler checks every type that lists
// the behavior against it; the engine that runs the behavior reads the same
// file.
//
//go:embed rating.behavior.json
var ratingDeclaration json.RawMessage

// registerBehavior declares acme.Rating. A type composes it with
// behaviors: [{name: acme.Rating, config: {maxStars: 5}}] in the data
// forms.
func registerBehavior(r *registry.Registry) error {
	return r.RegisterBehavior(registry.BehaviorSpec{Extension: Name, Declaration: ratingDeclaration})
}
