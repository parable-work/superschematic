package sqlutil

import (
	"fmt"
	"strings"

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

// ColumnType returns the PostgreSQL column type for an IR field of schema.
// Maps, @jsonField values and arrays of arrays (T[][]) are JSONB. A
// @jsonField value is a field marked JsonField<T> or a field whose type is a
// @jsonField class, alone or in a list: the Go ORM marshals either as one
// JSON document (D83). A T[][] is never a native multi-dimensional array:
// Postgres requires those to be rectangular, and a list of lists is often
// ragged. A list is a native array of its element's type. A nil schema
// knows no @jsonField class.
//
// A field's Validate rules narrow its element's type (D84): a decimal with
// a precision is NUMERIC(p,s), and an Embedding.Vector with dimensions is
// pgvector's vector(n). An Embedding.Vector without dimensions keeps the
// TEXT its scalar's missing SQL type falls back to.
func ColumnType(schema *ir.Schema, field *ir.FieldDef, scalarTypes map[string]string) string {
	if field.JsonField || field.TypeRef.IsMap || field.TypeRef.IsArrayOfArrays || IsJSONFieldType(schema, field.TypeRef.Name) {
		return "JSONB"
	}
	element := ElementType(field.TypeRef.Name, scalarTypes)
	switch {
	case field.ValidatePrecision != nil && strings.EqualFold(element, "NUMERIC"):
		element = fmt.Sprintf("NUMERIC(%d,%d)", *field.ValidatePrecision, field.DecimalScale())
	case field.ValidateDimensions != nil && field.TypeRef.Name == ir.VectorScalarName:
		element = VectorType(*field.ValidateDimensions)
	}
	if field.TypeRef.IsArray {
		return element + "[]"
	}
	return element
}

// VectorType is the column type of a vector of dimensions numbers,
// pgvector's vector(n) (D84).
func VectorType(dimensions int) string {
	return fmt.Sprintf("vector(%d)", dimensions)
}

// IsVectorType reports whether a column type is pgvector's vector(n), a
// single vector: the type the vector index methods and nearest read.
func IsVectorType(columnType string) bool {
	return strings.HasPrefix(strings.ToLower(columnType), "vector(") && !strings.HasSuffix(columnType, "]")
}

// VectorDimensions is n of a vector(n) column type, or 0 for another type.
func VectorDimensions(columnType string) int {
	if !IsVectorType(columnType) {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(strings.ToLower(columnType), "vector(%d)", &n); err != nil {
		return 0
	}
	return n
}

// IsJSONFieldType reports whether name is a @jsonField class of schema: a
// type stored as JSON in its parent's column, never a table of its own.
func IsJSONFieldType(schema *ir.Schema, name string) bool {
	if schema == nil {
		return false
	}
	td := schema.Types[name]
	return td != nil && td.JsonField
}
