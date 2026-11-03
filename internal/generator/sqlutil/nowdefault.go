package sqlutil

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// The time of the insert as a column's default (D88). A required date,
// time or timestamp column takes CURRENT_TIMESTAMP, CURRENT_DATE or
// CURRENT_TIME only when something says it should: its field declares
// Default<T, "now"> (codegen.DefaultNow), or it is a timestamp the
// generated code leaves to the database (ImpliedNowDefault). Any other
// such column has no default, so an import or a raw insert that leaves out
// a posting date fails NOT NULL rather than record today.

// NowDefault is the DEFAULT that gives a column of type columnType, which
// holds values of the schema type irType, the time of the insert:
// CURRENT_TIMESTAMP for a timestamp, CURRENT_DATE for a date and
// CURRENT_TIME for a time of day; "" for any other column. Postgres and
// SQLite both read each.
func NowDefault(columnType, irType string) string {
	traits := codegen.GuessScalarTraits(irType)
	switch base := sqlTypeBase(columnType); {
	case base == "TIMESTAMPTZ" || traits.IsDateTimeLike:
		return "CURRENT_TIMESTAMP"
	case base == "DATE" || traits.IsDateLike:
		return "CURRENT_DATE"
	case base == "TIME" || traits.IsTimeLike:
		return "CURRENT_TIME"
	}
	return ""
}

// DefaultsToNow reports whether the column's field declares
// Default<T, "now">: the database fills it in when an insert leaves it
// out, so a create of either ORM may (D88).
func (c ColumnModel) DefaultsToNow() bool {
	return c.Def != nil && c.Required && !c.IsArray() && !c.Def.TypeRef.IsMap && codegen.IsDefaultNow(c.Def.TypeRef.Name, c.Def.Default)
}

// ImpliedNowDefault reports whether field's required date, time or
// timestamp column, of the table td, takes the time of the insert though
// the field declares no default: an audit timestamp, createdAt or
// updatedAt, which the repositories leave to the database; and a
// timestamp of a table or a field the loader adds, for the user model
// (Session, UserCredential, UserRoleGrant, D50) or a version graph (D17),
// whose runtimes write rows without some of them. Every other column
// takes the time only from Default<T, "now">.
func ImpliedNowDefault(td *ir.TypeDef, field *ir.FieldDef) bool {
	switch {
	case field.Name == AuditCreatedAt || field.Name == AuditUpdatedAt:
		return true
	case field.Origin == ir.OriginIdentity || field.Origin == ir.OriginVersionGraph:
		return true
	}
	return td != nil && (td.Origin == ir.OriginIdentity || td.Origin == ir.OriginVersionGraph)
}
