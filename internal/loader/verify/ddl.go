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
// or an expression that holds more than one statement; and what pgvector
// does not build (checkVectorIndex, D84).
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
			checkVectorIndex(schema, scalarTypes, td, idx, label, r)
		}
	}
}

// The bounds pgvector holds a vector index's parameters to, and the most
// dimensions it indexes (D84).
const (
	vectorIndexMinM              = 2
	vectorIndexMaxM              = 100
	vectorIndexDefaultM          = 16
	vectorIndexMinEFConstruction = 4
	vectorIndexMaxEFConstruction = 1000
	vectorIndexMinLists          = 1
	vectorIndexMaxLists          = 32768
	vectorIndexMaxDimensions     = 2000
)

// VectorOpclasses are the operator classes a vector index takes, one per
// metric nearest orders by: cosine distance, Euclidean (L2) distance and
// negative inner product.
var VectorOpclasses = []string{"vector_cosine_ops", "vector_l2_ops", "vector_ip_ops"}

// checkVectorIndex holds an @index to what pgvector builds (D84): m and
// efConstruction on an hnsw index alone and lists on an ivfflat one, each
// in pgvector's range; and an hnsw or ivfflat index over one key, a vector
// column of at most 2000 dimensions, with no expression, not unique, and
// with the operator class of the metric its queries order by.
func checkVectorIndex(schema *ir.Schema, scalarTypes map[string]string, td *ir.TypeDef, idx ir.IndexDef, label string, r *Result) {
	if (idx.M != nil || idx.EFConstruction != nil) && idx.Method != ir.IndexMethodHNSW {
		r.errorf(td.Owner, "%s: %s sets m or efConstruction, which are the hnsw method's; set method: \"hnsw\" or leave them out", td.Name, label)
	}
	if idx.Lists != nil && idx.Method != ir.IndexMethodIVFFlat {
		r.errorf(td.Owner, "%s: %s sets lists, which is the ivfflat method's; set method: \"ivfflat\" or leave it out", td.Name, label)
	}
	m := vectorIndexDefaultM
	if idx.M != nil {
		m = *idx.M
		if m < vectorIndexMinM || m > vectorIndexMaxM {
			r.errorf(td.Owner, "%s: %s m is %d; pgvector takes %d to %d", td.Name, label, m, vectorIndexMinM, vectorIndexMaxM)
		}
	}
	if ef := idx.EFConstruction; ef != nil && (*ef < vectorIndexMinEFConstruction || *ef > vectorIndexMaxEFConstruction || *ef < 2*m) {
		r.errorf(td.Owner, "%s: %s efConstruction is %d; pgvector takes %d to %d, and at least twice m (%d)", td.Name, label, *ef, vectorIndexMinEFConstruction, vectorIndexMaxEFConstruction, m)
	}
	if lists := idx.Lists; lists != nil && (*lists < vectorIndexMinLists || *lists > vectorIndexMaxLists) {
		r.errorf(td.Owner, "%s: %s lists is %d; pgvector takes %d to %d", td.Name, label, *lists, vectorIndexMinLists, vectorIndexMaxLists)
	}
	if idx.Method != ir.IndexMethodHNSW && idx.Method != ir.IndexMethodIVFFlat {
		return
	}
	switch {
	case len(idx.Keys) != 1:
		r.errorf(td.Owner, "%s: %s uses the %s method, which indexes one vector column; list one key", td.Name, label, idx.Method)
	case idx.Expression != "":
		r.errorf(td.Owner, "%s: %s uses the %s method over an expression; a vector index covers its vector column itself", td.Name, label, idx.Method)
	case idx.Unique:
		r.errorf(td.Owner, "%s: %s uses the %s method, which cannot be unique", td.Name, label, idx.Method)
	}
	if !slices.Contains(VectorOpclasses, idx.Ops) {
		r.errorf(td.Owner, "%s: %s uses the %s method, whose ops names the metric its nearest-neighbour queries order by: %s", td.Name, label, idx.Method, strings.Join(VectorOpclasses, ", "))
	}
	for _, key := range idx.Keys {
		fd := indexKeyField(td, key)
		if fd == nil {
			continue // checkIndexKeys refuses the key.
		}
		column := sqlutil.ColumnType(schema, fd, scalarTypes)
		switch {
		case !isVectorColumn(column):
			r.errorf(td.Owner, "%s: %s uses the %s method, which indexes a vector column, and %s.%s is %s; a vector column is an Embedding.Vector with dimensions, Validate<Embedding.Vector, { dimensions: 1024 }>", td.Name, label, idx.Method, td.Name, fd.Name, column)
		case sqlutil.VectorDimensions(column) > vectorIndexMaxDimensions:
			r.errorf(td.Owner, "%s: %s uses the %s method on %s.%s, a %s; pgvector indexes vectors of at most %d dimensions", td.Name, label, idx.Method, td.Name, fd.Name, column, vectorIndexMaxDimensions)
		}
	}
}

// checkVectorColumns holds a table's Embedding.Vector fields to the column
// pgvector stores (D84): a single vector with dimensions, vector(n). A
// vector without dimensions has no column type, and a list of vectors no
// column a vector index or nearest reads; JSON holds either, as JsonField<T>
// or in a @jsonField type.
func checkVectorColumns(schema *ir.Schema, r *Result) {
	scalarTypes := sqlutil.ScalarSQLTypes(schema)
	for _, td := range sqlutil.TableTypes(schema) {
		for _, fd := range td.Fields {
			if fd.TypeRef.Name != ir.VectorScalarName {
				continue
			}
			column := sqlutil.ColumnType(schema, fd, scalarTypes)
			switch {
			case column == "JSONB":
			case fd.TypeRef.IsArray:
				r.errorf(td.Owner, "%s.%s is a list of %s, which no column holds: keep each vector in a row of its own, or the list as JSON (JsonField<%s[]>)", td.Name, fd.Name, ir.VectorScalarName, ir.VectorScalarName)
			case !isVectorColumn(column):
				r.errorf(td.Owner, "%s.%s is an %s with no dimensions, so its column has no type: declare them, Validate<%s, { dimensions: 1024 }>, a vector(1024) column", td.Name, fd.Name, ir.VectorScalarName, ir.VectorScalarName)
			}
		}
	}
}

// isVectorColumn reports whether a column type is pgvector's vector(n),
// which the vector index methods read.
func isVectorColumn(columnType string) bool {
	return sqlutil.IsVectorType(columnType)
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
