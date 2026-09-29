package sqlutil

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// ScalarSQLTypes returns the scalar-name -> SQL-type map the sql generator
// stores columns by, with the builtins string, number and boolean. Scalars
// with an explicit TypeMappings["sql"] entry use it (via the shared codegen
// mapping); scalars without one are inferred from their semantic traits
// (UUID-like -> UUID, datetime-like -> TIMESTAMPTZ, ...) before bottoming
// out on the host-language primitive.
func ScalarSQLTypes(schema *ir.Schema) map[string]string {
	mapping := codegen.BuildScalarSQLMapping(schema)

	for _, scalarDef := range schema.Scalars {
		if _, ok := scalarDef.TypeMappings["sql"]; ok {
			continue
		}
		tokens := codegen.BuildScalarTokens(scalarDef.Name)
		traits := codegen.BuildScalarTraits(scalarDef, tokens, "")
		if inferred := sqlTypeFromTraits(traits); inferred != "" {
			mapping[scalarDef.Name] = inferred
		}
	}

	return mapping
}

// sqlTypeFromTraits infers a SQL type from scalar semantics. Returns "" when
// the traits carry no SQL-relevant signal.
func sqlTypeFromTraits(traits codegen.ScalarTraits) string {
	switch {
	case traits.IsUUIDLike:
		return "UUID"
	case traits.IsDateTimeLike:
		return "TIMESTAMPTZ"
	case traits.IsDateLike:
		return "DATE"
	case traits.IsTimeLike:
		return "TIME"
	case traits.IsDurationLike, traits.IsIntegerLike:
		return "BIGINT"
	case traits.IsFloatLike:
		return "DOUBLE PRECISION"
	case traits.IsJSONLike, traits.IsObjectLike:
		return "JSONB"
	}
	return ""
}

// ElementType returns the SQL type of one value of the named type, from
// scalarTypes (ScalarSQLTypes): a scalar or builtin it maps, else TEXT.
func ElementType(typeName string, scalarTypes map[string]string) string {
	if sqlType, ok := scalarTypes[typeName]; ok {
		return sqlType
	}
	return "TEXT"
}

// ColumnType returns the PostgreSQL column type for an IR field. Maps,
// @jsonField values and arrays of arrays (T[][]) are JSONB. A T[][] is never
// a native multi-dimensional array: Postgres requires those to be
// rectangular, and a list of lists is often ragged. A list is a native array
// of its element's type.
func ColumnType(field *ir.FieldDef, scalarTypes map[string]string) string {
	if field.JsonField || field.TypeRef.IsMap || field.TypeRef.IsArrayOfArrays {
		return "JSONB"
	}
	element := ElementType(field.TypeRef.Name, scalarTypes)
	if field.TypeRef.IsArray {
		return element + "[]"
	}
	return element
}
