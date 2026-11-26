package ormgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
)

// The Go ORM's repositories on SQLite (D116), for a database whose DB
// service lists the sqlite dialect: sqlite.go holds the SQLite database,
// its transaction and what every table's repository fills and runs its
// statements with, and sqlite_<table>.go each table's repository, which
// implements the table's <Type>RepositoryInterface as the Postgres one
// does. The statements are sqlutil's SQLite statements, the ones the Rust
// repositories run (sqlutil.SQLiteTableStatementsOf); a value crosses to
// its column through its JSON form, as the TypeScript and Rust
// repositories' values do: a UUID as its lowercase hyphenated text, a
// timestamp in RFC 3339 in UTC to the millisecond, a boolean as 1 or 0, a
// list and a JSON value as JSON text, and a decimal as its text.

// SQLiteTable is a repository's table as sqlite_<table>.go writes it.
type SQLiteTable struct {
	// Refusal is why the table has no repository on SQLite, or "" when it
	// has one: its repository then refuses every call with it.
	Refusal string

	// Columns are the table's columns in the order its statements list
	// them, as the repository converts each.
	Columns []SQLiteColumn

	// Statements are the table's statements on SQLite.
	Statements sqlutil.TableStatements

	// Filters are the conditions of each member of the table's filter, on
	// SQLite, and Search the condition of a word of its SearchText.
	Filters []SQLiteFilter
	Search  string

	// UniqueKeys are the keys an upsert is keyed by, as SQLite reads the
	// rows that hold them.
	UniqueKeys []SQLiteUniqueKey

	// Updates are the members of <Type>Update, each with its column,
	// which an update and an upsert assign.
	Updates []SQLiteUpdate

	// Relations are the to-one relations a read hydrates when its fields
	// select them.
	Relations []SQLiteRelation

	// CreatedBy and UpdatedBy are the table's audit fields of who wrote a
	// row, nil without one, and UpdatedByColumn and DeletedByColumn the
	// indexes of updatedBy's and deletedBy's columns. HasUpdatedAt is a
	// table with updatedAt, which an update stamps.
	CreatedBy       *Field
	UpdatedBy       *Field
	UpdatedByColumn int
	DeletedByColumn int
	HasUpdatedAt    bool

	// Key is the index of the primary key's column, and DefaultLimit the
	// rows a page holds unless told.
	Key          int
	DefaultLimit int
}

// SQLiteColumn is a column of a table on SQLite: the member of the row's
// JSON it is, and how its column holds it.
type SQLiteColumn struct {
	// Name is the column, Quoted as a statement lists it, and Member the
	// member of the row's JSON. RelationKey is the member of the target's
	// key a to-one relation's column holds, under Member; empty for any
	// other column.
	Name        string
	Quoted      string
	Member      string
	RelationKey string

	// References is the table a to-one relation's column points at, and
	// Probe the read of whether a row of it holds a key, ?1, which names
	// the column a foreign key refused, since SQLite's refusal names none.
	References string
	Probe      string

	// UUID, Timestamp, Boolean, Interval and Numeric say how the column,
	// or each element of a list, holds its value; JSON is a column that
	// holds a JSON value as its text, and List one that holds a list as a
	// JSON array.
	UUID      bool
	Timestamp bool
	Boolean   bool
	Interval  bool
	Numeric   bool
	JSON      bool
	List      bool

	// Key is the primary key, which a create writes only when set; Auto a
	// column the database fills on a create, which a create never writes;
	// ZeroUnset one a create leaves to its default when it holds its zero
	// value (Default<T, "now">, D88); EnumDefault the schema default a
	// required enum takes when unset.
	Key         bool
	Auto        bool
	ZeroUnset   bool
	EnumDefault string
}

// SQLiteFilter is a member of a table's filter, Field (f.Name, or
// f.AccountID for a relation's key), and its conditions on SQLite.
type SQLiteFilter struct {
	Field      string
	Conditions []SQLiteCondition
}

// SQLiteCondition is one operation of a filter on SQLite, as
// FilterCondition is on Postgres: SQL holds ?%d, and Placeholder is the
// placeholder of each value of In, ?%d, or the JSON value of one for a JSON
// column's. Value, MinValue and MaxValue are the Go expressions of the
// values it binds, as SQLite holds them.
type SQLiteCondition struct {
	Op          string
	Field       string
	Member      string
	SQL         string
	SQL2        string
	Value       string
	Loop        string
	Placeholder string
	MinValue    string
	MaxValue    string
}

// SQLiteUniqueKey is a key UpsertOne is keyed by on SQLite: Const names
// it, Conflict and Keep are its conflict target and the assignment of an
// update that sets nothing, Rows reads the rows that hold its tuples, %s,
// Held reads the row that holds an expression key's value, and Columns are
// its columns, quoted.
type SQLiteUniqueKey struct {
	Const      string
	Conflict   string
	Keep       string
	Rows       string
	Held       string
	Columns    []string
	Expression bool
}

// SQLiteUpdate is a member of <Type>Update and the column it assigns:
// GoField is its member, Column its column's index among Columns, Quoted
// the column as an assignment names it and Qualified as a comparison does,
// Nullable a member with a SetNull, and Deref a pointer member whose value
// is a pointer itself.
type SQLiteUpdate struct {
	GoField   string
	Column    int
	Name      string
	Quoted    string
	Qualified string
	Nullable  bool
}

// SQLiteRelation is a to-one relation a read hydrates: its field, its
// target, whether the row holds it by a pointer, whether its nested
// selection is a pointer, and the index of its column.
type SQLiteRelation struct {
	GoField         string
	Field           string
	Target          string
	Pointer         bool
	NestedByPointer bool
	TargetKeyIsPtr  bool
	Column          int
}

// sqliteTableOf is repo's table on SQLite.
func sqliteTableOf(repo *Repository) (*SQLiteTable, error) {
	table := repo.Table
	out := &SQLiteTable{Refusal: sqliteRefusal(repo), DefaultLimit: sqlutil.DefaultLimit}
	if out.Refusal == "" {
		out.Statements = sqlutil.SQLiteTableStatementsOf(table)
		if err := out.Statements.CheckFormats(); err != nil {
			return nil, err
		}
	}
	if len(repo.OrderedMembers) != len(table.Columns) {
		return nil, fmt.Errorf("ormgen: %s has %d members and %d columns", repo.TypeName, len(repo.OrderedMembers), len(table.Columns))
	}
	clauses := sqlutil.SQLiteClauses()
	for i, c := range table.Columns {
		column := SQLiteColumn{
			Name:        c.Name,
			Quoted:      `"` + c.Name + `"`,
			Member:      c.Field,
			Key:         c.Key,
			EnumDefault: c.EnumDefault,
			JSON:        c.Class.JSON,
			List:        c.Class.List,
			UUID:        c.Class.UUIDLike,
			Timestamp:   c.Class.DateTimeLike,
			Boolean:     c.Class.BoolLike,
			Interval:    c.Class.Interval,
			Numeric:     c.Class.Decimal,
		}
		member := repo.OrderedMembers[i]
		switch {
		case member.IsRelationship:
			rel := member.Relationship
			column.RelationKey = "id"
			if target, ok := relationTarget(repo, rel); ok {
				column.RelationKey = target.member
				column.References = target.table
				column.Probe = "SELECT 1 FROM " + target.quoted + " WHERE " + target.key + " = ?1"
			}
			if !rel.IsArray {
				out.Relations = append(out.Relations, SQLiteRelation{
					GoField:         codegen.ToPascalCase(rel.FieldName),
					Field:           rel.FieldName,
					Target:          rel.TargetType,
					Pointer:         !rel.IsRequired,
					NestedByPointer: rel.NestedByPointer,
					TargetKeyIsPtr:  rel.TargetPrimaryKeyIsPointer,
					Column:          i,
				})
				if !rel.IsRequired {
					out.Updates = append(out.Updates, SQLiteUpdate{GoField: codegen.ToPascalCase(rel.FieldName) + "ID", Column: i, Name: c.Name, Quoted: c.Quoted, Qualified: c.Qualified, Nullable: true})
				} else {
					out.Updates = append(out.Updates, SQLiteUpdate{GoField: codegen.ToPascalCase(rel.FieldName) + "ID", Column: i, Name: c.Name, Quoted: c.Quoted, Qualified: c.Qualified})
				}
				out.Filters = append(out.Filters, SQLiteFilter{
					Field:      "f." + codegen.ToPascalCase(rel.FieldName) + "ID",
					Conditions: sqliteConditions("f."+codegen.ToPascalCase(rel.FieldName)+"ID", sqlutil.FilterUUID, c.Qualified, false, clauses),
				})
			}
		default:
			f := member.Field
			column.Auto = f.IsAuditTimestamp || f.IsInternalMetadata
			column.ZeroUnset = f.DefaultsToNow
			if f.FilterKind != "" {
				out.Filters = append(out.Filters, SQLiteFilter{
					Field:      "f." + codegen.ToPascalCase(f.Name),
					Conditions: sqliteConditions("f."+codegen.ToPascalCase(f.Name), f.FilterKind, c.Qualified, c.Filter == sqlutil.FilterJSON, clauses),
				})
			}
			if !f.IsPrimaryKey && !f.IsAuditField && !f.IsInternalMetadata && !f.IsOwned {
				out.Updates = append(out.Updates, SQLiteUpdate{GoField: codegen.ToPascalCase(f.Name), Column: i, Name: c.Name, Quoted: c.Quoted, Qualified: c.Qualified, Nullable: !f.IsRequired})
			}
			switch f.Name {
			case sqlutil.AuditCreatedBy:
				field := *f
				out.CreatedBy = &field
			case sqlutil.AuditUpdatedBy:
				field := *f
				out.UpdatedBy, out.UpdatedByColumn = &field, i
			case sqlutil.AuditDeletedBy:
				out.DeletedByColumn = i
			case sqlutil.AuditUpdatedAt:
				out.HasUpdatedAt = true
			}
		}
		if c.Key {
			out.Key = i
		}
		out.Columns = append(out.Columns, column)
	}
	if table.Search {
		out.Search = strings.Replace(clauses.Search, "%s", table.SearchColumn, 1)
	}
	for i, key := range table.UniqueKeys {
		k := key.SQLite()
		out.UniqueKeys = append(out.UniqueKeys, SQLiteUniqueKey{
			Const:      repo.UniqueKeys[i].Const,
			Conflict:   k.Conflict,
			Keep:       k.Keep,
			Rows:       k.Rows,
			Held:       k.Held,
			Columns:    repo.UniqueKeys[i].QuotedColumns,
			Expression: k.Expression,
		})
	}
	return out, nil
}

// sqliteTarget is a table a relation points at: the member of its key in
// its row's JSON, its table, and its table and key as a statement names
// them.
type sqliteTarget struct {
	member, table, quoted, key string
}

// relationTarget is rel's target table, and whether the repository knows
// it.
func relationTarget(repo *Repository, rel *Relationship) (sqliteTarget, bool) {
	target, ok := repo.sqliteTargets[rel.TargetType]
	return target, ok
}

// sqliteRefusal is why repo's table has no repository on SQLite, or "":
// what the SQLite DDL does not hold (sqlutil.SQLiteTableRefusal), and what
// the Go repositories do not hold there yet.
func sqliteRefusal(repo *Repository) string {
	if reason := sqlutil.SQLiteTableRefusal(repo.Table); reason != "" {
		return reason
	}
	switch {
	case repo.Versioned:
		return "it is @versioned, whose history the SQLite DDL does not keep"
	case len(repo.VectorFields) > 0:
		return "it has a vector column, which SQLite has no storage for"
	case len(repo.EncryptedFields) > 0:
		return "it has an Encrypted<T> field, which the Go repositories do not seal on SQLite yet"
	case repo.Merge != nil:
		return "it implements Mergeable, whose MergeInto the Go repositories run on Postgres alone"
	case repo.Released:
		return "it holds a version graph's released rows, which the version graph keeps on Postgres alone"
	case repo.HasBehaviors:
		return "it composes behaviors, whose writes the Go repositories run through the core on Postgres alone"
	}
	for _, f := range repo.Fields {
		if f.IsGeoPoint || f.IsGeoPointList {
			return fmt.Sprintf("its column %s is a POINT column, which SQLite has no storage for", f.DBName)
		}
	}
	return ""
}

// sqliteConditions are the conditions of a filter of kind on the member
// field over the column qualified, on SQLite: json is a Generic.JSON
// column, whose values compare as JSON values (sqlutil.SQLiteJSONClauses).
func sqliteConditions(field string, kind sqlutil.FilterKind, qualified string, json bool, clauses sqlutil.Clauses) []SQLiteCondition {
	on := func(clause string) string { return strings.Replace(clause, "%s", qualified, 1) }
	placeholder := clauses.Placeholder
	eq, ne, in, noneOf := on(clauses.Eq), on(clauses.Ne), on(clauses.In), on(clauses.NoneOf)
	if json {
		j := sqlutil.SQLiteJSONClauses()
		eq, ne, in, noneOf = on(j.Eq), on(j.Ne), on(j.In), on(j.NoneOf)
		placeholder = j.Value
	}
	value := func(expr string) string {
		switch kind {
		case sqlutil.FilterUUID:
			return "sqliteUUIDText(" + expr + ")"
		case sqlutil.FilterDateTime:
			return "sqliteTimeText(" + expr + ")"
		case sqlutil.FilterBool:
			return "sqliteBoolValue(" + expr + ")"
		case sqlutil.FilterDuration:
			return "sqliteDurationText(" + expr + ")"
		}
		return expr
	}
	min, max := "Min", "Max"
	if kind == sqlutil.FilterDateTime || kind == sqlutil.FilterDate {
		min, max = "Start", "End"
	}
	var out []SQLiteCondition
	for _, op := range sqlutil.FilterOps(goFilterKind(kind)) {
		c := SQLiteCondition{Op: op, Field: field, Placeholder: placeholder}
		switch op {
		case sqlutil.FilterEq:
			c.Member, c.SQL, c.Value = "Eq", eq, value("*"+field+".Eq")
		case sqlutil.FilterNe:
			c.Member, c.SQL, c.Value = "Ne", ne, value("*"+field+".Ne")
		case sqlutil.FilterGt:
			c.Member, c.SQL, c.Value = "Gt", on(clauses.Gt), value("*"+field+".Gt")
		case sqlutil.FilterGte:
			c.Member, c.SQL, c.Value = "Gte", on(clauses.Gte), value("*"+field+".Gte")
		case sqlutil.FilterLt:
			c.Member, c.SQL, c.Value = "Lt", on(clauses.Lt), value("*"+field+".Lt")
		case sqlutil.FilterLte:
			c.Member, c.SQL, c.Value = "Lte", on(clauses.Lte), value("*"+field+".Lte")
		case sqlutil.FilterILike:
			c.Member, c.SQL, c.Value = "ILike", on(clauses.ILike), `"%"+*`+field+`.ILike+"%"`
		case sqlutil.FilterIn:
			c.Member, c.SQL, c.Loop, c.Value = "In", in, "value", value("value")
		case sqlutil.FilterNotIn:
			c.Op, c.Member, c.SQL, c.Loop, c.Value = sqlutil.FilterIn, "NotIn", noneOf, "value", value("value")
		case sqlutil.FilterBetween:
			c.Member, c.SQL, c.SQL2 = "Between", on(clauses.Gte), on(clauses.Lte)
			c.MinValue, c.MaxValue = value(field+".Between."+min), value(field+".Between."+max)
		case sqlutil.FilterIsNull:
			c.Member, c.SQL, c.SQL2 = "IsNull", on(clauses.IsNull), on(clauses.IsNotNull)
		}
		out = append(out, c)
	}
	return out
}

// SQLiteRepositories are the repositories of an output that runs on
// SQLite, and SQLiteRefused those of them whose table SQLite does not
// hold.
func (o *ORMOutput) SQLiteRefused() []Repository {
	var out []Repository
	for _, repo := range o.Repositories {
		if repo.SQLite != nil && repo.SQLite.Refusal != "" {
			out = append(out, repo)
		}
	}
	return out
}

// HasSQLiteRepositories is a database whose DB service lists the sqlite
// dialect: sqlite.go and a sqlite_<table>.go per repository are written.
func (o *ORMOutput) HasSQLiteRepositories() bool {
	return o.SQLite && len(o.Repositories) > 0
}
