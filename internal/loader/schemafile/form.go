package schemafile

// SingleDefKind names the single-definition file form a definition uses, for
// writers emitting the inverse of Decode. TypeDef files carry no "kind"
// discriminator (they dispatch on "role"), so their kind is empty.
type SingleDefKind string

const (
	// SingleDefType is a single type-definition file (no discriminator).
	SingleDefType SingleDefKind = ""

	// SingleDefEnum is a single enum file ("kind": "Enum").
	SingleDefEnum SingleDefKind = "Enum"

	// SingleDefUnion is a single union file ("kind": "Union").
	SingleDefUnion SingleDefKind = "Union"

	// SingleDefScalar is a single scalar file ("kind": "Scalar").
	SingleDefScalar SingleDefKind = "Scalar"

	// SingleDefOperationSet is a single operation-set file
	// ("kind": "OperationSet").
	SingleDefOperationSet SingleDefKind = "OperationSet"
)

// SingleDefinition reports whether a document is exactly one definition with
// no document-level metadata -- the shape Decode produces for the
// single-definition file forms. Writers use this to emit the single
// definition back in its file form instead of wrapping it in a document.
//
// The returned definition is the *ir.TypeDef, *ir.EnumDef, *ir.UnionDef,
// *ir.ScalarDef, or *ir.OperationSet held by the document.
func SingleDefinition(doc *Document) (SingleDefKind, any, bool) {
	if doc.Name != "" || doc.Kind != "" || doc.Description != "" || doc.Comment != "" || len(doc.Imports) > 0 ||
		len(doc.Documents) > 0 || len(doc.Extensions) > 0 {
		return SingleDefType, nil, false
	}

	total := len(doc.Types) + len(doc.Enums) + len(doc.Unions) + len(doc.Scalars) +
		len(doc.OperationSets)
	if total != 1 {
		return SingleDefType, nil, false
	}

	for _, def := range doc.Types {
		return SingleDefType, def, true
	}
	for _, def := range doc.Enums {
		return SingleDefEnum, def, true
	}
	for _, def := range doc.Unions {
		return SingleDefUnion, def, true
	}
	for _, def := range doc.Scalars {
		return SingleDefScalar, def, true
	}
	if len(doc.OperationSets) == 1 {
		return SingleDefOperationSet, doc.OperationSets[0], true
	}
	// Combinators, primitives, plots, and ontology definitions have no
	// single-definition file form; they always live in a document.
	return SingleDefType, nil, false
}
