package tsreader

import (
	ir "github.com/parable-work/superschematic/ir"
)

// The Mergeable trait (D147): a DB table declares that its repository folds
// one row into another with `implements Mergeable`, a core trait
// @superschematic/db exports, as it declares the user model's with User and
// UserRole. It is no schema trait: no class of the schema declares it, so
// it is no TraitRef and RawHeritage leaves it out. The reader records it as
// TypeDef.Mergeable, and the schema writer writes it back from there.
// Whether the table can be merged (a DB table with a key) is the
// verification pass's, as it is for the data forms.

// mergeableTraitName is the trait's name, as @superschematic/db declares it.
const mergeableTraitName = "Mergeable"

// mergeableEntry reports whether an implements-clause entry names
// @superschematic/db's Mergeable, resolved by its origin, so a renamed
// import still names it and a schema's own trait of the name does not.
func (w *walker) mergeableEntry(t *astNode) bool {
	id, ok := w.identityOf(t.AsExpressionWithTypeArguments().Expression)
	return ok && id.is("@superschematic/db", mergeableTraitName)
}

// applyMergeable records one Mergeable entry of td's implements clause.
func (w *walker) applyMergeable(td *ir.TypeDef, t *astNode) {
	if td.Mergeable {
		w.addErr(errorAtNode(t, "%s: the Mergeable trait appears more than once", td.Name))
		return
	}
	if args := t.AsExpressionWithTypeArguments().TypeArguments; args != nil && len(args.Nodes) > 0 {
		w.addErr(errorAtNode(t, "%s: the Mergeable trait takes no type arguments", td.Name))
	}
	td.Mergeable = true
}
