package sqlutil

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// DDLPolicy is the naming file's [sql] table as the DDL reads it: the
// defaults and checks a column takes beyond its type. The zero value is
// the core's DDL: D83's checks and defaults, and D88's rule for the time
// of the insert. sqlgen writes the DDL by it, and every reader that asks
// what a column carries (verification, the Rust ORM) asks it, so the
// generated code never counts on a DEFAULT the DDL left out.
type DDLPolicy struct {
	// NoColumnChecks leaves out the CHECK of an enum's members and of a
	// field's Validate bounds (column_checks = "off").
	NoColumnChecks bool
	// NoColumnDefaults leaves out the DEFAULT of a Default<T, V> whose V
	// is not "now" (column_defaults = "off").
	NoColumnDefaults bool
	// NowOnEveryRequiredTime gives every required date, time or timestamp
	// column with no other default the time of the insert
	// (now_defaults = "required").
	NowOnEveryRequiredTime bool
}

// DDLPolicyOf reads the policy from n's [sql] table.
func DDLPolicyOf(n naming.Naming) DDLPolicy {
	return DDLPolicy{
		NoColumnChecks:         n.SQL.ColumnChecks == naming.SQLOff,
		NoColumnDefaults:       n.SQL.ColumnDefaults == naming.SQLOff,
		NowOnEveryRequiredTime: n.SQL.NowDefaults == naming.SQLNowRequired,
	}
}

// ColumnDefault is ColumnDefault under the policy: with NoColumnDefaults,
// only Default<T, "now"> is a DEFAULT.
func (p DDLPolicy) ColumnDefault(field *ir.FieldDef, columnType string) (string, bool) {
	def, ok := ColumnDefault(field, columnType)
	if ok && p.NoColumnDefaults && !codegen.IsDefaultNow(field.TypeRef.Name, field.Default) {
		return "", false
	}
	return def, ok
}

// ColumnCheck is ColumnCheck under the policy: with NoColumnChecks, no
// column has one.
func (p DDLPolicy) ColumnCheck(field *ir.FieldDef, column, columnType string, enum *ir.EnumDef) (Check, bool) {
	if p.NoColumnChecks {
		return Check{}, false
	}
	return ColumnCheck(field, column, columnType, enum)
}

// ImpliedNowDefault is ImpliedNowDefault under the policy: with
// NowOnEveryRequiredTime, every required field. NowDefault still says
// whether its column holds a date, a time or a timestamp, and the caller
// still gives it only to a column with no other default.
func (p DDLPolicy) ImpliedNowDefault(td *ir.TypeDef, field *ir.FieldDef) bool {
	return (p.NowOnEveryRequiredTime && field.Required) || ImpliedNowDefault(td, field)
}
