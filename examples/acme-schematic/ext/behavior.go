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
// the behavior against it; the TypeScript engine runs @acme/behaviors and
// the Rust engine the acme-behaviors crate, each over the copy `acme-schematic
// behaviors` writes from this registration.
//
//go:embed rating.behavior.json
var ratingDeclaration json.RawMessage

// ratingCrate is the Rust crate that implements acme.Rating (D58),
// crates/behaviors in this module, the project the schemas root sits in:
// the projectengine generator writes, for a schema that composes the
// behavior, an engine crate that depends on it there and registers its
// Rating.
var ratingCrate = registry.BehaviorRust{
	Crate:    "acme-behaviors",
	Path:     "crates/behaviors",
	Register: "acme_behaviors::Rating",
}

// registerBehavior declares acme.Rating. A type composes it with
// @behavior("acme.Rating", { maxStars: 5 }), whose config
// packages/schema/src/behaviors.ts types, or with
// behaviors: [{name: acme.Rating, config: {maxStars: 5}}] in the data
// forms.
func registerBehavior(r *registry.Registry) error {
	return r.RegisterBehavior(registry.BehaviorSpec{Extension: Name, Rust: ratingCrate, Declaration: ratingDeclaration})
}
