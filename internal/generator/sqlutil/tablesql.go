package sqlutil

import (
	"fmt"
	"strings"
)

// TableStatements are the statements of a table on Postgres (D79), written
// here once for both ORMs: the Go ORM's repository.go and query.go carry
// them as Go format strings, and the TypeScript types' ./orm entry carries
// them as data, which the HTTP runtime's ormOf fills in the same order, so
// both run the same SQL. Each is a format string of the verbs %s and %d
// alone, filled by Go's fmt.Sprintf and by the runtime's fill, in the order
// each field's comment gives. Columns is the select list every statement
// that returns rows returns, in the table's column order.
type TableStatements struct {
	// Columns is every column, each in double quotes, joined by ", ": what
	// a read of a whole row selects and a write returns.
	Columns string
	// QualifiedColumns is every column qualified by the table, each part in
	// double quotes ("review"."title"): what FindMany and FindPage select.
	QualifiedColumns string
	// SelectColumn is one column of a selection, the column %s, unquoted,
	// qualified as QualifiedColumns qualifies it: what a read that selects
	// some columns, or includes a relation, lists (D90).
	SelectColumn string

	// GetOne reads the row whose key is $1; GetOneFields the columns %s of
	// it. GetMany reads the rows whose key is one of %s, the placeholders.
	GetOne       string
	GetOneFields string
	GetMany      string

	// Live, LiveWhere and LiveCondition leave a soft-deletable table's
	// deleted rows out: Live after a WHERE's conditions, LiveWhere as the
	// whole WHERE, and LiveCondition as one condition qualified by the
	// table. Empty for a table without deletedAt.
	Live          string
	LiveWhere     string
	LiveCondition string

	// FindOne reads the first row: the columns %s, the WHERE %s and the
	// ORDER BY %s. FindMany reads the columns %s, the WHERE %s, the ORDER BY
	// %s and the LIMIT and OFFSET %s. Count counts the rows of the WHERE %s.
	FindOne  string
	FindMany string
	Count    string

	// Page reads a page: the columns %s, the WHERE %s, the ORDER BY %s and
	// the limit %d, one more than the page holds. PageAfterKey is the
	// keyset of an order by the key: the comparison %s and the key's
	// placeholder %s. PageAfter is the keyset of an order by another
	// column: the column %s, the comparison %s, the column of the key's row
	// %s (its PageKey) and the key's placeholder %s twice. PageOrder orders
	// by the column %s in the direction %s, and PageTiebreak then by the key
	// in the direction %s. PageKeyExists reads whether the key $1 names a
	// row, deleted or not.
	//
	// PageAfterNulls and PageOrderNulls are the keyset and the order of a
	// column that may be null (D112), whose nulls come last in either
	// direction, in key order among themselves. PageAfterNulls reads
	// whether the key's row holds a null, its PageKey %s and the key's
	// placeholder %s: after a null come the rows that hold one, the column
	// %s, past the key, a filled PageAfterKey %s; after a value come the
	// rows that hold a null, the column %s, and the rows past the value, a
	// filled PageAfter %s. A key that names no row answers neither, so it
	// places no row, as PageAfter's does. PageOrderNulls takes what
	// PageOrder takes. Postgres orders a null after every value going up
	// and before every value going down, and SQLite the other way, so the
	// order says where nulls go rather than leave it to either.
	Page           string
	PageAfterKey   string
	PageAfter      string
	PageAfterNulls string
	PageOrder      string
	PageOrderNulls string
	PageTiebreak   string
	PageKeyExists  string

	// Insert inserts one row: the columns %s, their placeholders %s and the
	// columns returned %s. InsertMany inserts the rows %s, each a tuple of
	// placeholders and DEFAULT, of the columns %s first, then returns %s.
	Insert     string
	InsertMany string

	// Update sets %s on the row whose key is $%d. VersionFence follows it
	// for a write fenced on _version $%d, Live for a soft-deletable table,
	// and Returning last. UpdateMany sets %s on the rows of the WHERE %s.
	// UpdatedAt and UpdatedBy are the assignments an update stamps. On a
	// table with a @fenced field, the assignments %s of Update, UpdateMany
	// and Upsert end with the bump of each fence they advance (D119,
	// TableModel.FenceBumps), which the repository that fills them adds.
	Update       string
	VersionFence string
	Returning    string
	UpdateMany   string
	UpdatedAt    string
	UpdatedBy    string

	// Upsert inserts the columns %s with the rows %s, each a tuple of
	// placeholders and DEFAULT as InsertMany's, and on a conflict on the
	// key %s (UniqueKey.Conflict) sets %s on the row it conflicts with,
	// when the WHERE %s holds: the live rows alone on a soft-deletable
	// table (LiveCondition), and, for an upsert only if changed, the rows
	// whose columns differ from the update's values (Clauses.Changed). It
	// returns each row it inserted or updated, and UpsertInserted, true for
	// a row it inserted (D89).
	Upsert string

	// SoftDelete stamps deleted_at $1 and deleted_by $2 on the live row
	// whose key is $3, or deleted_at $1 on the one whose key is $2 for a
	// table without deletedBy; SoftDeleteIfVersion fences it on _version
	// too. SoftDeleteMany stamps the rows of the WHERE %s, whose parameters
	// follow those. Each also advances a @fenced field that covers a column
	// it stamps, or that covers the row (D119). Empty for a table without
	// deletedAt.
	SoftDelete          string
	SoftDeleteIfVersion string
	SoftDeleteMany      string

	// Delete removes the row whose key is $1, and DeleteIfVersion the one
	// at _version $2 too; DeleteMany the rows of the WHERE %s. On a
	// versioned table with a history actor they follow HistoryActorCTE,
	// whose $1 is the actor, so the key is $2.
	Delete          string
	DeleteIfVersion string
	DeleteMany      string

	// Version reads the _version of the live row whose key is $1, which
	// tells a fenced write's miss from a conflict.
	Version string

	// Stale and Reseal re-encrypt a table's Encrypted<T> columns under the
	// current key (D85); empty for a table without one. Stale reads the
	// key and the encrypted columns of the rows, deleted ones too, a column
	// of which another key sealed: $1 is the current key's envelope
	// prefix, a LIKE pattern, and $2 how many, in key order. Reseal sets
	// each encrypted column, in order, from $1, on the row whose key is the
	// parameter after them, when each still holds what Stale read, the
	// parameters after that: a write between the two, which sealed under
	// the current key, is left as it is. Reseal also advances a @fenced
	// field that covers an encrypted column, or that covers the row (D119).
	Stale  string
	Reseal string

	// Nearest reads the rows nearest a vector (D84): the columns %s, the
	// distance %s as _distance, the WHERE %s, the ORDER BY the distance %s
	// again, which a vector index of its metric serves, and the limit %d.
	// The distance is a vector column's NearestMetrics clause, whose
	// parameter, the vector, follows the WHERE's. Empty for a table
	// without a vector column.
	Nearest string
}

// NearestDistance is the alias Nearest selects a row's distance as.
const NearestDistance = "_distance"

// The metrics nearest orders by (D84): pgvector's cosine distance,
// Euclidean (L2) distance and negative inner product, each the operator
// class a vector index serves it with.
const (
	MetricCosine = "cosine"
	MetricL2     = "l2"
	MetricIP     = "ip"
)

// NearestMetric is a metric nearest orders by: its name, the distance of
// the vector column %s from the parameter $%d, and the operator class of
// the vector index that serves it.
type NearestMetric struct {
	Name     string
	Distance string
	Opclass  string
}

// NearestMetrics are the metrics in the order both ORMs name them, cosine
// first, the default.
func NearestMetrics() []NearestMetric {
	return []NearestMetric{
		{Name: MetricCosine, Distance: "%s <=> $%d", Opclass: "vector_cosine_ops"},
		{Name: MetricL2, Distance: "%s <-> $%d", Opclass: "vector_l2_ops"},
		{Name: MetricIP, Distance: "%s <#> $%d", Opclass: "vector_ip_ops"},
	}
}

// DefaultNearestK is the neighbours nearest reads unless told.
const DefaultNearestK = 10

// HistoryActorCTE prefixes a hard delete of a versioned table that has an
// actor column (D17, D32): it sets the transaction-local history actor
// setting, setting, to $1, which the history trigger reads the delete
// tombstone's actor from; an empty value keeps the row's own. The DELETE
// names history_actor in its USING clause so the CTE runs.
func HistoryActorCTE(setting string) string {
	return "WITH history_actor AS MATERIALIZED (\n\tSELECT set_config('" + setting + "', COALESCE($1::text, ''), true) AS _history_actor\n)\n"
}

// ClearHistoryActor empties the history actor setting after a hard delete
// in an open transaction, so a later statement in it does not record the
// same actor.
func ClearHistoryActor(setting string) string {
	return "SELECT set_config('" + setting + "', '', true)"
}

// Clauses are the parts of statements both ORMs write around a column or a
// table's statements, each a format string whose first %s is the column,
// as Qualify names it for a condition and an order and as QuoteIdentifier
// does for an assignment (D79). The Go ORM writes them into its statements
// at generation (FilterConditionsOf, ColumnStatementsOf) and the
// TypeScript types' ./orm entry carries them for the runtime to fill.
type Clauses struct {
	// Eq, Gt, Gte, Lt, Lte and ILike compare the column %s with the
	// parameter $%d; In with the placeholders %s; IsNull and IsNotNull
	// test it; Search matches one word of a search text, its pattern the
	// parameter, on the search column %s. Ne and NoneOf are a filter's ne
	// and notIn (D112): the rows Eq and In leave out, a row whose column
	// is NULL among them, which <> and NOT IN would drop.
	Eq, Ne, Gt, Gte, Lt, Lte, ILike, In, NoneOf, IsNull, IsNotNull, Search string
	// Exists and NotExists wrap a probe %s of the rows that point at a row.
	Exists, NotExists string
	// Set and SetNull assign the column %s in an update, Set the parameter
	// $%d. PageKey is the column %s of the row a page key names.
	Set, SetNull, PageKey string
	// Placeholder is the parameter $%d, and Default the value of a column a
	// row of a batch insert leaves unset.
	Placeholder, Default string
	// Or joins the branches of a filter's anyOf, and Group wraps a branch's
	// conditions and then the branches, %s, in parentheses (D90).
	Or, Group string
	// Where begins a WHERE of conditions joined by And. OrderBy begins an
	// ORDER BY of terms joined by ", ", each an OrderTerm: the column %s
	// and its direction %s, ASC or DESC. Limit and Offset end a FindMany:
	// the limit %d, DefaultLimit unless set, and the offset %d when there
	// is one.
	Where, And, OrderBy, OrderTerm, Limit, Offset string
	// SetExcluded assigns the column %s, in an upsert's update, the value
	// the row it would have inserted holds in the column %s, and Excluded
	// is that value. Changed is the condition of an upsert only if changed
	// (D89): the row's columns %s, each as ColumnModel.CompareAs writes it,
	// are distinct from the update's values %s. Never is the condition of
	// one whose update compares no column, which leaves every row as it is,
	// and Null the value an update that sets a column NULL compares. NotIn
	// keeps the rows whose column %s is none of the placeholders %s, as
	// nearest's excludeIds does.
	SetExcluded, Excluded, Changed, Never, Null, NotIn string
}

// PostgresClauses are the clauses on Postgres.
func PostgresClauses() Clauses {
	return Clauses{
		Eq:          "%s = $%d",
		Ne:          "%s IS DISTINCT FROM $%d",
		Gt:          "%s > $%d",
		Gte:         "%s >= $%d",
		Lt:          "%s < $%d",
		Lte:         "%s <= $%d",
		ILike:       "%s ILIKE $%d",
		In:          "%s IN (%s)",
		NoneOf:      "(%s IN (%s)) IS NOT TRUE",
		IsNull:      "%s IS NULL",
		IsNotNull:   "%s IS NOT NULL",
		Search:      `%s ILIKE $%d ESCAPE '\'`,
		Exists:      "EXISTS (%s)",
		NotExists:   "NOT EXISTS (%s)",
		Set:         "%s = $%d",
		SetNull:     "%s = NULL",
		PageKey:     "page_key.%s",
		Placeholder: "$%d",
		Default:     "DEFAULT",
		Or:          " OR ",
		Group:       "(%s)",
		Where:       " WHERE ",
		And:         " AND ",
		OrderBy:     " ORDER BY ",
		OrderTerm:   "%s %s",
		Limit:       " LIMIT %d",
		Offset:      " OFFSET %d",
		SetExcluded: "%s = EXCLUDED.%s",
		Excluded:    "EXCLUDED.%s",
		Changed:     "(%s) IS DISTINCT FROM (%s)",
		Never:       "FALSE",
		Null:        "NULL",
		NotIn:       "%s NOT IN (%s)",
	}
}

// SQLiteClauses are the clauses on SQLite (D116), written from Postgres's:
// a parameter is ?%d, which SQLite numbers as Postgres numbers $%d, where it
// reads $1 as a name. SQLite has no ILIKE: its LIKE folds the case of ASCII
// letters alone, and takes no escape character unless the pattern's
// condition names one, where Postgres's is a backslash unless it names
// another, so ILike and Search both name it. Default is empty: a VALUES list
// on SQLite takes no DEFAULT, so a repository there inserts a batch a row at
// a time, each row's statement listing the columns the row sets.
func SQLiteClauses() Clauses {
	c := PostgresClauses()
	for _, clause := range []*string{&c.Eq, &c.Ne, &c.Gt, &c.Gte, &c.Lt, &c.Lte, &c.Set, &c.Placeholder} {
		*clause = strings.Replace(*clause, "$%d", "?%d", 1)
	}
	c.ILike = `%s LIKE ?%d ESCAPE '\'`
	c.Search = `%s LIKE ?%d ESCAPE '\'`
	c.Default = ""
	return c
}

// DefaultLimit is the rows FindMany and FindPage read unless told.
const DefaultLimit = 100

// AllClauses are the clauses by name, in the order the struct declares
// them, for the generators that carry them and the tests that compare them.
func (c Clauses) AllClauses() []NamedStatement {
	return []NamedStatement{
		{"eq", c.Eq}, {"ne", c.Ne}, {"gt", c.Gt}, {"gte", c.Gte}, {"lt", c.Lt}, {"lte", c.Lte}, {"ilike", c.ILike},
		{"in", c.In}, {"noneOf", c.NoneOf}, {"isNull", c.IsNull}, {"isNotNull", c.IsNotNull}, {"search", c.Search},
		{"exists", c.Exists}, {"notExists", c.NotExists},
		{"set", c.Set}, {"setNull", c.SetNull}, {"pageKey", c.PageKey},
		{"placeholder", c.Placeholder}, {"default", c.Default}, {"or", c.Or}, {"group", c.Group},
		{"where", c.Where}, {"and", c.And}, {"orderBy", c.OrderBy}, {"orderTerm", c.OrderTerm},
		{"limit", c.Limit}, {"offset", c.Offset},
		{"setExcluded", c.SetExcluded}, {"excluded", c.Excluded}, {"changed", c.Changed}, {"never", c.Never}, {"null", c.Null}, {"notIn", c.NotIn},
	}
}

// tableDialect is what a table's statements differ by between Postgres and
// SQLite: the clauses, how a numbered parameter is written, and whether the
// statements are SQLite's.
type tableDialect struct {
	clauses Clauses
	sqlite  bool
}

// param is the parameter numbered n: $n on Postgres and ?n on SQLite.
func (d tableDialect) param(n int) string {
	return fmt.Sprintf(d.clauses.Placeholder, n)
}

// numbered is the format of a parameter whose number its caller fills, $%d
// or ?%d.
func (d tableDialect) numbered() string {
	return d.clauses.Placeholder
}

// TableStatementsOf writes the statements of table on Postgres.
func TableStatementsOf(table *TableModel) TableStatements {
	return tableStatements(table, tableDialect{clauses: PostgresClauses()})
}

// SQLiteTableStatementsOf writes the statements of table on SQLite (D116),
// from the one model Postgres's are written from, for a table
// SQLiteTableRefusal does not refuse. They are Postgres's but for these:
//
//   - A parameter is ?n.
//   - Upsert returns no UpsertInserted, since a row of SQLite holds no
//     xmax: a repository reads whether a row holds the key before the
//     statement, in the transaction that runs both, which on SQLite is the
//     one writer.
//   - On an @optimistic table, Update and Upsert's update set _version to
//     the next one themselves. SQLite's bump is an AFTER UPDATE trigger,
//     which runs after RETURNING has read the row, so the row a write
//     returned would hold the version before it; the trigger leaves an
//     update that moves _version as it is (sqlmigrate's sqliteBumpTrigger).
//
// InsertMany is as Postgres's and takes rows that set the same columns: a
// VALUES list on SQLite has no DEFAULT (SQLiteClauses).
func SQLiteTableStatementsOf(table *TableModel) TableStatements {
	return tableStatements(table, tableDialect{clauses: SQLiteClauses(), sqlite: true})
}

// tableStatements writes the statements of table in the dialect d.
func tableStatements(table *TableModel, d tableDialect) TableStatements {
	t := table.Quoted
	key := QuoteIdentifier(table.Key)
	columns := make([]string, len(table.Columns))
	qualified := make([]string, len(table.Columns))
	for i, c := range table.Columns {
		columns[i] = `"` + c.Name + `"`
		qualified[i] = `"` + table.Name + `"."` + c.Name + `"`
	}
	list := strings.Join(columns, ", ")
	p, n := d.param, d.numbered()
	// bump moves _version in the statement itself, on SQLite, where the
	// trigger that moves it runs after RETURNING.
	bump := ""
	if d.sqlite && table.HasVersion {
		bump = ", " + VersionColumn + " = " + VersionColumn + " + 1"
	}
	s := TableStatements{
		Columns:          list,
		QualifiedColumns: strings.Join(qualified, ", "),
		SelectColumn:     `"` + table.Name + `"."%s"`,
		GetOne:           "SELECT " + list + " FROM " + t + " WHERE " + key + " = " + p(1),
		GetOneFields:     "SELECT %s FROM " + t + " WHERE " + key + " = " + p(1),
		GetMany:          "SELECT " + list + " FROM " + t + " WHERE " + key + " IN (%s)",
		FindOne:          "SELECT %s FROM " + t + "%s%s LIMIT 1",
		FindMany:         "SELECT %s FROM " + t + "%s%s%s",
		Count:            "SELECT COUNT(*) FROM " + t + "%s",
		Page:             "SELECT %s FROM " + t + "%s%s LIMIT %d",
		PageAfterKey:     t + "." + key + " %s %s",
		PageAfter:        "(%s, " + t + "." + key + ") %s ((SELECT %s FROM " + t + " AS page_key WHERE page_key." + key + " = %s), %s)",
		PageAfterNulls:   "CASE (SELECT %s IS NULL FROM " + t + " AS page_key WHERE page_key." + key + " = %s) WHEN TRUE THEN %s IS NULL AND %s WHEN FALSE THEN (%s IS NULL OR %s) END",
		PageOrder:        " ORDER BY %s %s",
		PageOrderNulls:   " ORDER BY %s %s NULLS LAST",
		PageTiebreak:     ", " + t + "." + key + " %s",
		PageKeyExists:    "SELECT EXISTS (SELECT 1 FROM " + t + " WHERE " + t + "." + key + " = " + p(1) + ")",
		Insert:           "INSERT INTO " + t + " (%s) VALUES (%s) RETURNING %s",
		InsertMany:       "INSERT INTO " + t + " (%s) VALUES %s RETURNING %s",
		Update:           "UPDATE " + t + " SET %s" + bump + " WHERE " + key + " = " + n,
		VersionFence:     " AND " + VersionColumn + " = " + n,
		Returning:        " RETURNING " + list,
		UpdateMany:       "UPDATE " + t + " SET %s%s",
		UpdatedAt:        strings.Replace(d.clauses.Set, "%s", table.AuditQuoted(AuditUpdatedAt), 1),
		UpdatedBy:        strings.Replace(d.clauses.Set, "%s", table.AuditQuoted(AuditUpdatedBy), 1),
		Version:          "SELECT " + VersionColumn + " FROM " + t + " WHERE " + key + " = " + p(1),
	}
	if table.SoftDelete {
		// The audit columns are the fields', as @column names them (D85).
		deletedAt, deletedBy := table.AuditQuoted(AuditDeletedAt), table.AuditQuoted(AuditDeletedBy)
		s.Live = " AND " + deletedAt + " IS NULL"
		s.LiveWhere = " WHERE " + deletedAt + " IS NULL"
		s.LiveCondition = t + "." + deletedAt + " IS NULL"
		s.Version += s.Live
		if table.DeletedBy {
			// The stamps, then each fence they advance (D119).
			stamps := deletedAt + " = " + p(1) + ", " + deletedBy + " = " + p(2) + table.fenced(deletedAt, deletedBy)
			s.SoftDelete = "UPDATE " + t + " SET " + stamps + " WHERE " + key + " = " + p(3) + " AND " + deletedAt + " IS NULL"
			s.SoftDeleteIfVersion = "UPDATE " + t + " SET " + stamps + " WHERE " + key + " = " + p(3) + " AND _version = " + p(4) + " AND " + deletedAt + " IS NULL"
			s.SoftDeleteMany = "UPDATE " + t + " SET " + stamps + "%s"
		} else {
			stamps := deletedAt + " = " + p(1) + table.fenced(deletedAt)
			s.SoftDelete = "UPDATE " + t + " SET " + stamps + " WHERE " + key + " = " + p(2) + " AND " + deletedAt + " IS NULL"
			s.SoftDeleteIfVersion = "UPDATE " + t + " SET " + stamps + " WHERE " + key + " = " + p(2) + " AND _version = " + p(3) + " AND " + deletedAt + " IS NULL"
			s.SoftDeleteMany = "UPDATE " + t + " SET " + stamps + "%s"
		}
	}
	s.Upsert = "INSERT INTO " + t + " (%s) VALUES %s ON CONFLICT %s DO UPDATE SET %s" + bump + "%s RETURNING " + list
	if !d.sqlite {
		// xmax is 0 in the row version an insert wrote, and holds the
		// conflicting row's lock in the version an update wrote (D89).
		s.Upsert += ", (xmax = 0) AS " + UpsertInserted
	}
	s.Stale, s.Reseal = resealStatements(table, t, key, d)
	if len(table.VectorColumns()) > 0 && !d.sqlite {
		s.Nearest = "SELECT %s, %s AS " + NearestDistance + " FROM " + t + "%s ORDER BY %s LIMIT %d"
	}
	if table.HistoryActor != "" && !d.sqlite {
		s.Delete = "DELETE FROM " + t + " USING history_actor WHERE " + key + " = $2"
		s.DeleteIfVersion = "DELETE FROM " + t + " USING history_actor WHERE " + key + " = $2 AND _version = $3"
		s.DeleteMany = "DELETE FROM " + t + " USING history_actor%s"
	} else {
		s.Delete = "DELETE FROM " + t + " WHERE " + key + " = " + p(1)
		s.DeleteIfVersion = "DELETE FROM " + t + " WHERE " + key + " = " + p(1) + " AND _version = " + p(2)
		s.DeleteMany = "DELETE FROM " + t + "%s"
	}
	return s
}

// resealStatements are Stale and Reseal of table, whose name is t and key
// column key, both quoted, in the dialect d; empty for a table without an
// Encrypted<T> column. Reseal sets every encrypted column, and with them
// advances each fence they move (D119).
func resealStatements(table *TableModel, t, key string, d tableDialect) (stale, reseal string) {
	encrypted := table.EncryptedColumns()
	if len(encrypted) == 0 {
		return "", ""
	}
	selected := []string{key}
	var older, sets, guards, sealed []string
	for i, c := range encrypted {
		selected = append(selected, c.Quoted)
		older = append(older, c.Quoted+" NOT LIKE "+d.param(1))
		sets = append(sets, c.Quoted+" = "+d.param(i+1))
		guards = append(guards, c.Quoted+" IS NOT DISTINCT FROM "+d.param(len(encrypted)+2+i))
		sealed = append(sealed, c.Quoted)
	}
	stale = "SELECT " + strings.Join(selected, ", ") + " FROM " + t + " WHERE " + strings.Join(older, " OR ") + " ORDER BY " + key + " LIMIT " + d.param(2)
	reseal = "UPDATE " + t + " SET " + strings.Join(sets, ", ") + table.fenced(sealed...) + " WHERE " + key + " = " + d.param(len(encrypted)+1) + " AND " + strings.Join(guards, " AND ")
	return stale, reseal
}

// ColumnStatements are a column's parts of a table's statements: Qualified
// names it with its table, Set and SetNull assign it in an update (Set's
// $%d its value's number), PageKey is its value in the row a page key
// names, and Filter its conditions. SetExcluded assigns it, in an upsert's
// update, the value of the row the upsert would have inserted; Compared,
// CompareParam ($%d) and CompareExcluded are it, a parameter and that value
// as an upsert only if changed compares them (D89).
type ColumnStatements struct {
	Qualified       string
	Set             string
	SetNull         string
	PageKey         string
	Filter          FilterConditions
	SetExcluded     string
	Compared        string
	CompareParam    string
	CompareExcluded string
}

// ColumnStatementsOf writes the parts of column c of its table.
func ColumnStatementsOf(c *ColumnModel) ColumnStatements {
	clauses := PostgresClauses()
	on := func(clause string) string { return strings.Replace(clause, "%s", c.Quoted, 1) }
	compare := func(value string) string { return strings.Replace(c.CompareAs(), "%s", value, 1) }
	excluded := strings.Replace(clauses.Excluded, "%s", c.Quoted, 1)
	return ColumnStatements{
		Qualified:       c.Qualified,
		Set:             on(clauses.Set),
		SetNull:         on(clauses.SetNull),
		PageKey:         on(clauses.PageKey),
		Filter:          FilterConditionsOf(c.Qualified),
		SetExcluded:     strings.Replace(on(clauses.SetExcluded), "%s", c.Quoted, 1),
		Compared:        compare(c.Qualified),
		CompareParam:    compare(clauses.Placeholder),
		CompareExcluded: compare(excluded),
	}
}

// ReferenceProbe is the subquery of a filter on a reference: the rows of
// the source that point at the table's row, and Live, which leaves the
// source's deleted rows out, on a soft-deletable source. Exists and
// NotExists wrap it as a condition.
type ReferenceProbe struct {
	Probe     string
	Live      string
	Exists    string
	NotExists string
}

// ReferenceProbeOf is the probe of ref on table.
func ReferenceProbeOf(table *TableModel, ref Reference) ReferenceProbe {
	p := ReferenceProbe{
		Probe:     "SELECT 1 FROM " + ref.SourceTable + " WHERE " + ref.SourceTable + "." + ref.SourceColumn + " = " + table.Quoted + "." + QuoteIdentifier(table.Key),
		Exists:    PostgresClauses().Exists,
		NotExists: PostgresClauses().NotExists,
	}
	if ref.SourceSoftDelete {
		p.Live = " AND " + ref.SourceTable + "." + ref.SourceDeletedAt + " IS NULL"
	}
	return p
}

// SearchConditionOf is the condition of one word of a search on table's
// search column, a view's source's on a view (D99), or "" for a table
// without search fields.
func SearchConditionOf(table *TableModel) string {
	if !table.Search {
		return ""
	}
	return SearchCondition(table.SearchColumn)
}

// AllStatements are the table's statements by name, sorted, for the tests
// that hold the two ORMs to them.
func (s TableStatements) AllStatements() []NamedStatement {
	return []NamedStatement{
		{"columns", s.Columns}, {"qualifiedColumns", s.QualifiedColumns}, {"selectColumn", s.SelectColumn},
		{"getOne", s.GetOne}, {"getOneFields", s.GetOneFields}, {"getMany", s.GetMany},
		{"live", s.Live}, {"liveWhere", s.LiveWhere}, {"liveCondition", s.LiveCondition},
		{"findOne", s.FindOne}, {"findMany", s.FindMany}, {"count", s.Count},
		{"page", s.Page}, {"pageAfterKey", s.PageAfterKey}, {"pageAfter", s.PageAfter}, {"pageAfterNulls", s.PageAfterNulls},
		{"pageOrder", s.PageOrder}, {"pageOrderNulls", s.PageOrderNulls}, {"pageTiebreak", s.PageTiebreak}, {"pageKeyExists", s.PageKeyExists},
		{"insert", s.Insert}, {"insertMany", s.InsertMany},
		{"update", s.Update}, {"versionFence", s.VersionFence}, {"returning", s.Returning},
		{"updateMany", s.UpdateMany}, {"updatedAt", s.UpdatedAt}, {"updatedBy", s.UpdatedBy},
		{"upsert", s.Upsert},
		{"softDelete", s.SoftDelete}, {"softDeleteIfVersion", s.SoftDeleteIfVersion}, {"softDeleteMany", s.SoftDeleteMany},
		{"delete", s.Delete}, {"deleteIfVersion", s.DeleteIfVersion}, {"deleteMany", s.DeleteMany},
		{"version", s.Version}, {"nearest", s.Nearest},
		{"stale", s.Stale}, {"reseal", s.Reseal},
	}
}

// CheckFormat reports a statement that holds a verb other than %s, %d and
// %%, which the TypeScript runtime's fill does not read.
func CheckFormat(name, statement string) error {
	for i := 0; i < len(statement); i++ {
		if statement[i] != '%' {
			continue
		}
		if i+1 >= len(statement) || !strings.ContainsRune("sd%", rune(statement[i+1])) {
			return fmt.Errorf("sqlutil: statement %s holds %q, which is not %%s, %%d or %%%%", name, statement)
		}
		i++
	}
	return nil
}

// CheckFormats reports a statement of s with a verb the runtimes' fill
// does not read.
func (s TableStatements) CheckFormats() error {
	for _, st := range s.AllStatements() {
		if err := CheckFormat(st.Name, st.SQL); err != nil {
			return err
		}
	}
	return nil
}

// sqliteReserved are the words SQLite reads as keywords wherever a name
// may stand that QuoteIdentifier, which quotes for Postgres, leaves bare:
// a table or a column named one has no statement SQLite parses.
var sqliteReserved = map[string]bool{
	"alter": true, "autoincrement": true, "delete": true, "drop": true,
	"escape": true, "exists": true, "insert": true, "raise": true,
}

// SQLiteTableRefusal says why table has no repository on SQLite (D116), or
// is "" when it has one. The SQLite DDL refuses, for the whole service, a
// @versioned table, a projection, a vector and a column SQLite has no
// storage for (sqlmigrate), so a table that reaches here is one its DDL
// holds; what is left is a column of bytes, a BLOB there, which the
// repositories' values have no one form for, as a queue's have none
// (SQLiteQueueRefusal), and a name SQLite reserves and the statements
// leave bare.
func SQLiteTableRefusal(table *TableModel) string {
	if sqliteReserved[strings.ToLower(table.Name)] && QuoteIdentifier(table.Name) == table.Name {
		return fmt.Sprintf("its table is named %s, a word SQLite reserves, which the repository's statements leave unquoted", table.Name)
	}
	for _, c := range table.Columns {
		if sqliteReserved[strings.ToLower(c.Name)] && QuoteIdentifier(c.Name) == c.Name {
			return fmt.Sprintf("its column %s is named for a word SQLite reserves, which the repository's statements leave unquoted", c.Name)
		}
		if IsBinaryType(c.SQLType()) {
			return fmt.Sprintf("its column %s is a BYTEA column, a BLOB on SQLite, which the SQLite repositories do not hold yet", c.Name)
		}
		if reason := SQLiteQueueRefusal(c.SQLType()); reason != "" {
			return fmt.Sprintf("its column %s %s", c.Name, reason)
		}
	}
	return ""
}
