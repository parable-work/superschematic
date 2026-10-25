package verify

import (
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The DDL a DB schema declares beyond its tables (D83): the options of an
// @index, and the raw SQL of @rawSql.

// checkIndexOptions refuses an @index option the DDL cannot honor: a
// method or an operator class it does not know, which a data form can
// write; any of method, ops, where, expression and concurrently on an
// engine type, whose index the engine keeps over its fields alone; a where
// or an expression that holds more than one statement; and the vector
// methods, hnsw and ivfflat, on a key whose column is not a vector, which
// every column is until vector columns arrive in a later decision.
func checkIndexOptions(schema *ir.Schema, r *Result) {
	scalarTypes := sqlutil.ScalarSQLTypes(schema)
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		for _, idx := range td.Indexes {
			if !idx.HasTableOptions() {
				continue
			}
			label := indexLabel(idx)
			if idx.Method != "" && !slices.Contains(ir.IndexMethods, idx.Method) {
				r.errorf(td.Owner, "%s: %s method %q is not one of %s", td.Name, label, idx.Method, strings.Join(ir.IndexMethods, ", "))
			}
			if idx.Ops != "" && !sqlutil.ValidOpclass(idx.Ops) {
				r.errorf(td.Owner, "%s: %s ops %q must name an operator class, a lowercase identifier such as gin_trgm_ops", td.Name, label, idx.Ops)
			}
			if codegen.IsEngineType(td) {
				r.errorf(td.Owner, "%s: %s sets method, ops, where, expression or concurrently, which only a table's index has; %s is an engine type, whose index the engine keeps over its fields alone", td.Name, label, td.Name)
				continue
			}
			for _, part := range []struct{ name, sql string }{{"where", idx.Where}, {"expression", idx.Expression}} {
				if part.sql == "" {
					continue
				}
				statements, err := sqlutil.SplitStatements(part.sql)
				switch {
				case err != nil:
					r.errorf(td.Owner, "%s: %s %s: %v", td.Name, label, part.name, err)
				case len(statements) != 1:
					r.errorf(td.Owner, "%s: %s %s must be one SQL expression, with no semicolon", td.Name, label, part.name)
				}
			}
			if idx.Method == ir.IndexMethodHNSW || idx.Method == ir.IndexMethodIVFFlat {
				for _, key := range idx.Keys {
					fd := indexKeyField(td, key)
					if fd == nil {
						continue // checkIndexKeys refuses the key.
					}
					if column := sqlutil.ColumnType(schema, fd, scalarTypes); !isVectorColumn(column) {
						r.errorf(td.Owner, "%s: %s uses the %s method, which indexes a vector column, and %s.%s is %s; vector columns follow", td.Name, label, idx.Method, td.Name, fd.Name, column)
					}
				}
			}
		}
	}
}

// isVectorColumn reports whether a column type is pgvector's vector(n),
// which the vector index methods read.
func isVectorColumn(columnType string) bool {
	return strings.HasPrefix(strings.ToLower(columnType), "vector(")
}

// indexKeyField returns the field of td an @index key resolves to, or nil.
func indexKeyField(td *ir.TypeDef, key string) *ir.FieldDef {
	for _, fd := range td.Fields {
		if fd.Name == key {
			return fd
		}
	}
	return nil
}

// checkRawSQL refuses @rawSql on a type that gets no table, which is where
// the decorator lives (a base class, a @jsonField type, a queue's message,
// an engine type, a projection), and every problem sqlutil.CheckRawSQL
// finds: a piece the DDL could not place, or could not run again when it
// changes.
func checkRawSQL(schema *ir.Schema, r *Result) {
	bases := codegen.BaseTypeNames(schema)
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if len(td.RawSQL) == 0 {
			continue
		}
		var on string
		switch {
		case td.Role == ir.RoleProjection:
			on = "a @projection"
		case td.Role != ir.RoleDBTable:
			on = "a type of role " + string(td.Role)
		case td.JsonField:
			on = "a @jsonField type"
		case td.Queue != nil:
			on = "a @queue message"
		case codegen.IsEngineType(td):
			on = "an engine type, whose instances the engine keeps"
		case bases[td.Name]:
			on = "a base class"
		default:
			continue
		}
		r.errorf(td.Owner, "%s: @rawSql is on %s, which gets no table; write it on a table of the service", td.Name, on)
	}
	for _, err := range sqlutil.CheckRawSQL(schema) {
		r.errorf("", "%s", err)
	}
}
