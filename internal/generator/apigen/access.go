package apigen

import ir "github.com/parable-work/superschematic/ir"

// DeclaresAccess reports what per-record access (D156, D180) schema, an
// API's database (nil for none), declares, as its Go ORM serves it: any,
// an @audience, @accessGrants or @accessLog table, whose ORM binds a
// request's caller whole (orm.WithPrincipal); held, an @audience table or
// grants, whose reads keep to the caller; and log, an @accessLog table,
// which each route of an API over it records its calls in.
func DeclaresAccess(schema *ir.Schema) (any, held, log bool) {
	if schema == nil {
		return false, false, false
	}
	held = schema.AccessGrantsTable() != nil || len(schema.AudienceTables()) > 0
	log = schema.AccessLogTable() != nil
	return held || log, held, log
}
