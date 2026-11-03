package rustgen

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// ScalarRustTypes returns the Rust type each scalar of schema has in its
// types crate, by the scalar's schema name (`Identity.UUID`): the type its
// alias in scalars.rs names, with the scalar crate as names spells it
// (`superscalar::Uuid`, `i64`). A scalar that the crate declares as a
// struct of its own is left out. What a crate that takes a scalar's value
// without depending on the schema's types crate names it by, as a
// MemoryStore's definitions do for a part (D75).
func ScalarRustTypes(schema *ir.Schema, names naming.Naming) map[string]string {
	extraction := codegen.ExtractionConfig{
		Language:            "rust",
		PrimitiveMapper:     inferRustType,
		FieldTypeMapper:     fieldTypeMapperRust,
		FieldNameMapper:     toRustFieldName,
		ScalarPostProcessor: refineRustScalarType,
	}
	out := map[string]string{}
	for _, s := range convertScalars(codegen.ExtractScalars(schema, extraction), names.OrDefault().ScalarRustCrateIdent()) {
		if isStructDef(s.RustType) {
			continue
		}
		out[s.CanonicalName] = s.RustType
	}
	return out
}
