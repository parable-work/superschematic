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
	Page          string
	PageAfterKey  string
	PageAfter     string
	PageOrder     string
	PageTiebreak  string
	PageKeyExists string

	// Insert inserts one row: the columns %s, their placeholders %s and the
	// columns returned %s. InsertMany inserts the rows %s, each a tuple of
	// placeholders and DEFAULT, of the columns %s first, then returns %s.
	Insert     string
	InsertMany string

	// Update sets %s on the row whose key is $%d. VersionFence follows it
	// for a write fenced on _version $%d, Live for a soft-deletable table,
	// and Returning last. UpdateMany sets %s on the rows of the WHERE %s.
	// UpdatedAt and UpdatedBy are the assignments an update stamps.
	Update       string
	VersionFence string
	Returning    string
	UpdateMany   string
	UpdatedAt    string
	UpdatedBy    string

	// Upsert inserts the columns %s with the placeholders %s, and on a
	// conflict on the key %s (UniqueKey.Conflict) sets %s on the row it
	// conflicts with, a live one alone on a soft-deletable table, and
	// returns the row.
	Upsert string

	// SoftDelete stamps deleted_at $1 and deleted_by $2 on the live row
	// whose key is $3, or deleted_at $1 on the one whose key is $2 for a
	// table without deletedBy; SoftDeleteIfVersion fences it on _version
	// too. SoftDeleteMany stamps the rows of the WHERE %s, whose parameters
	// follow those. Empty for a table without deletedAt.
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
	// the current key, is left as it is.
	Stale  string
	Reseal string
}

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
	// parameter, on the search column %s.
	Eq, Gt, Gte, Lt, Lte, ILike, In, IsNull, IsNotNull, Search string
	// Exists and NotExists wrap a probe %s of the rows that point at a row.
	Exists, NotExists string
	// Set and SetNull assign the column %s in an update, Set the parameter
	// $%d. PageKey is the column %s of the row a page key names.
	Set, SetNull, PageKey string
	// Placeholder is the parameter $%d, and Default the value of a column a
	// row of a batch insert leaves unset.
	Placeholder, Default string
	// Where begins a WHERE of conditions joined by And. OrderBy begins an
	// ORDER BY of terms joined by ", ", each an OrderTerm: the column %s
	// and its direction %s, ASC or DESC. Limit and Offset end a FindMany:
	// the limit %d, DefaultLimit unless set, and the offset %d when there
	// is one.
	Where, And, OrderBy, OrderTerm, Limit, Offset string
}

// PostgresClauses are the clauses on Postgres.
func PostgresClauses() Clauses {
	return Clauses{
		Eq:          "%s = $%d",
		Gt:          "%s > $%d",
		Gte:         "%s >= $%d",
		Lt:          "%s < $%d",
		Lte:         "%s <= $%d",
		ILike:       "%s ILIKE $%d",
		In:          "%s IN (%s)",
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
		Where:       " WHERE ",
		And:         " AND ",
		OrderBy:     " ORDER BY ",
		OrderTerm:   "%s %s",
		Limit:       " LIMIT %d",
		Offset:      " OFFSET %d",
	}
}

// DefaultLimit is the rows FindMany and FindPage read unless told.
const DefaultLimit = 100

// AllClauses are the clauses by name, in the order the struct declares
// them, for the generators that carry them and the tests that compare them.
func (c Clauses) AllClauses() []NamedStatement {
	return []NamedStatement{
		{"eq", c.Eq}, {"gt", c.Gt}, {"gte", c.Gte}, {"lt", c.Lt}, {"lte", c.Lte}, {"ilike", c.ILike},
		{"in", c.In}, {"isNull", c.IsNull}, {"isNotNull", c.IsNotNull}, {"search", c.Search},
		{"exists", c.Exists}, {"notExists", c.NotExists},
		{"set", c.Set}, {"setNull", c.SetNull}, {"pageKey", c.PageKey},
		{"placeholder", c.Placeholder}, {"default", c.Default},
		{"where", c.Where}, {"and", c.And}, {"orderBy", c.OrderBy}, {"orderTerm", c.OrderTerm},
		{"limit", c.Limit}, {"offset", c.Offset},
	}
}

// TableStatementsOf writes the statements of table.
func TableStatementsOf(table *TableModel) TableStatements {
	t := table.Quoted
	key := QuoteIdentifier(table.Key)
	columns := make([]string, len(table.Columns))
	qualified := make([]string, len(table.Columns))
	for i, c := range table.Columns {
		columns[i] = `"` + c.Name + `"`
		qualified[i] = `"` + table.Name + `"."` + c.Name + `"`
	}
	list := strings.Join(columns, ", ")
	s := TableStatements{
		Columns:          list,
		QualifiedColumns: strings.Join(qualified, ", "),
		GetOne:           "SELECT " + list + " FROM " + t + " WHERE " + key + " = $1",
		GetOneFields:     "SELECT %s FROM " + t + " WHERE " + key + " = $1",
		GetMany:          "SELECT " + list + " FROM " + t + " WHERE " + key + " IN (%s)",
		FindOne:          "SELECT %s FROM " + t + "%s%s LIMIT 1",
		FindMany:         "SELECT %s FROM " + t + "%s%s%s",
		Count:            "SELECT COUNT(*) FROM " + t + "%s",
		Page:             "SELECT %s FROM " + t + "%s%s LIMIT %d",
		PageAfterKey:     t + "." + key + " %s %s",
		PageAfter:        "(%s, " + t + "." + key + ") %s ((SELECT %s FROM " + t + " AS page_key WHERE page_key." + key + " = %s), %s)",
		PageOrder:        " ORDER BY %s %s",
		PageTiebreak:     ", " + t + "." + key + " %s",
		PageKeyExists:    "SELECT EXISTS (SELECT 1 FROM " + t + " WHERE " + t + "." + key + " = $1)",
		Insert:           "INSERT INTO " + t + " (%s) VALUES (%s) RETURNING %s",
		InsertMany:       "INSERT INTO " + t + " (%s) VALUES %s RETURNING %s",
		Update:           "UPDATE " + t + " SET %s WHERE " + key + " = $%d",
		VersionFence:     " AND " + VersionColumn + " = $%d",
		Returning:        " RETURNING " + list,
		UpdateMany:       "UPDATE " + t + " SET %s%s",
		UpdatedAt:        strings.Replace(PostgresClauses().Set, "%s", QuoteIdentifier("updated_at"), 1),
		UpdatedBy:        strings.Replace(PostgresClauses().Set, "%s", QuoteIdentifier("updated_by"), 1),
		Version:          "SELECT " + VersionColumn + " FROM " + t + " WHERE " + key + " = $1",
	}
	upsertGuard := ""
	if table.SoftDelete {
		s.Live = " AND deleted_at IS NULL"
		s.LiveWhere = " WHERE deleted_at IS NULL"
		s.LiveCondition = t + ".deleted_at IS NULL"
		s.Version += s.Live
		upsertGuard = " WHERE " + s.LiveCondition
		if table.DeletedBy {
			s.SoftDelete = "UPDATE " + t + " SET deleted_at = $1, deleted_by = $2 WHERE " + key + " = $3 AND deleted_at IS NULL"
			s.SoftDeleteIfVersion = "UPDATE " + t + " SET deleted_at = $1, deleted_by = $2 WHERE " + key + " = $3 AND _version = $4 AND deleted_at IS NULL"
			s.SoftDeleteMany = "UPDATE " + t + " SET deleted_at = $1, deleted_by = $2%s"
		} else {
			s.SoftDelete = "UPDATE " + t + " SET deleted_at = $1 WHERE " + key + " = $2 AND deleted_at IS NULL"
			s.SoftDeleteIfVersion = "UPDATE " + t + " SET deleted_at = $1 WHERE " + key + " = $2 AND _version = $3 AND deleted_at IS NULL"
			s.SoftDeleteMany = "UPDATE " + t + " SET deleted_at = $1%s"
		}
	}
	s.Upsert = "INSERT INTO " + t + " (%s) VALUES (%s) ON CONFLICT %s DO UPDATE SET %s" + upsertGuard + " RETURNING " + list
	s.Stale, s.Reseal = resealStatements(table, t, key)
	if table.HistoryActor != "" {
		s.Delete = "DELETE FROM " + t + " USING history_actor WHERE " + key + " = $2"
		s.DeleteIfVersion = "DELETE FROM " + t + " USING history_actor WHERE " + key + " = $2 AND _version = $3"
		s.DeleteMany = "DELETE FROM " + t + " USING history_actor%s"
	} else {
		s.Delete = "DELETE FROM " + t + " WHERE " + key + " = $1"
		s.DeleteIfVersion = "DELETE FROM " + t + " WHERE " + key + " = $1 AND _version = $2"
		s.DeleteMany = "DELETE FROM " + t + "%s"
	}
	return s
}

// resealStatements are Stale and Reseal of table, whose name is t and key
// column key, both quoted; empty for a table without an Encrypted<T>
// column.
func resealStatements(table *TableModel, t, key string) (stale, reseal string) {
	encrypted := table.EncryptedColumns()
	if len(encrypted) == 0 {
		return "", ""
	}
	selected := []string{key}
	var older, sets, guards []string
	for i, c := range encrypted {
		selected = append(selected, c.Quoted)
		older = append(older, c.Quoted+" NOT LIKE $1")
		sets = append(sets, fmt.Sprintf("%s = $%d", c.Quoted, i+1))
		guards = append(guards, fmt.Sprintf("%s IS NOT DISTINCT FROM $%d", c.Quoted, len(encrypted)+2+i))
	}
	stale = "SELECT " + strings.Join(selected, ", ") + " FROM " + t + " WHERE " + strings.Join(older, " OR ") + " ORDER BY " + key + " LIMIT $2"
	reseal = "UPDATE " + t + " SET " + strings.Join(sets, ", ") + fmt.Sprintf(" WHERE %s = $%d AND ", key, len(encrypted)+1) + strings.Join(guards, " AND ")
	return stale, reseal
}

// ColumnStatements are a column's parts of a table's statements: Qualified
// names it with its table, Set and SetNull assign it in an update (Set's
// $%d its value's number), PageKey is its value in the row a page key
// names, and Filter its conditions.
type ColumnStatements struct {
	Qualified string
	Set       string
	SetNull   string
	PageKey   string
	Filter    FilterConditions
}

// ColumnStatementsOf writes the parts of column c of its table.
func ColumnStatementsOf(c *ColumnModel) ColumnStatements {
	clauses := PostgresClauses()
	on := func(clause string) string { return strings.Replace(clause, "%s", c.Quoted, 1) }
	return ColumnStatements{
		Qualified: c.Qualified,
		Set:       on(clauses.Set),
		SetNull:   on(clauses.SetNull),
		PageKey:   on(clauses.PageKey),
		Filter:    FilterConditionsOf(c.Qualified),
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
		p.Live = " AND " + ref.SourceTable + ".deleted_at IS NULL"
	}
	return p
}

// SearchConditionOf is the condition of one word of a search on table's
// search column, or "" for a table without search fields.
func SearchConditionOf(table *TableModel) string {
	if !table.Search {
		return ""
	}
	return SearchCondition(Qualify(table.Name, SearchTextColumn))
}

// AllStatements are the table's statements by name, sorted, for the tests
// that hold the two ORMs to them.
func (s TableStatements) AllStatements() []NamedStatement {
	return []NamedStatement{
		{"columns", s.Columns}, {"qualifiedColumns", s.QualifiedColumns},
		{"getOne", s.GetOne}, {"getOneFields", s.GetOneFields}, {"getMany", s.GetMany},
		{"live", s.Live}, {"liveWhere", s.LiveWhere}, {"liveCondition", s.LiveCondition},
		{"findOne", s.FindOne}, {"findMany", s.FindMany}, {"count", s.Count},
		{"page", s.Page}, {"pageAfterKey", s.PageAfterKey}, {"pageAfter", s.PageAfter},
		{"pageOrder", s.PageOrder}, {"pageTiebreak", s.PageTiebreak}, {"pageKeyExists", s.PageKeyExists},
		{"insert", s.Insert}, {"insertMany", s.InsertMany},
		{"update", s.Update}, {"versionFence", s.VersionFence}, {"returning", s.Returning},
		{"updateMany", s.UpdateMany}, {"updatedAt", s.UpdatedAt}, {"updatedBy", s.UpdatedBy},
		{"upsert", s.Upsert},
		{"softDelete", s.SoftDelete}, {"softDeleteIfVersion", s.SoftDeleteIfVersion}, {"softDeleteMany", s.SoftDeleteMany},
		{"delete", s.Delete}, {"deleteIfVersion", s.DeleteIfVersion}, {"deleteMany", s.DeleteMany},
		{"version", s.Version},
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
