// Package crosstable is the reads across tables of the Go runtimes
// (D187): the calendar of D152, which merges the dates of every
// @calendarSource into one read and one iCalendar feed, and search across
// tables of D155, which ranks the rows of every @searchable table in one
// query. It ports the TypeScript runtime's calendar.ts and
// search-tables.ts, with their entries, statements, ranking, states and
// refusals.
//
// Nothing here opens a connection. The generated ORM of a database with a
// calendar source or a @searchable table implements Host (its
// CrossTables method) over its pool, its access predicate (D180) and its
// grants, and holds the definitions it was generated with. Go has an
// ambient caller, the context, and a read across tables still takes the
// caller's principal and binds it itself (Host.As), as the Rust runtime's
// does (D188): a feed's route reads as the feed's holder, not as the
// request's caller. On SQLite (#615) the generated ORM's SQLite database
// is a DialectHost: the calendar and search write their statements in
// SQLite's words there, search's words an FTS5 table ranked by bm25, as
// the TypeScript runtime's on SQLite and D1 (D186), and embeddings are
// refused (dialect.go, search_sqlite.go).
package crosstable

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/fieldcrypt"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// Row is one row a statement returned, by column.
type Row map[string]any

// Host is what the calendar and search run through: the generated ORM's
// database. Its statements run as written, reading every row; what a
// caller sees is what Conditions keeps a read of a table to.
type Host interface {
	// Query runs a statement and returns its rows.
	Query(ctx context.Context, sql string, args ...any) ([]Row, error)
	// Exec runs a statement and returns the rows it touched.
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	// Transaction runs fn in a transaction of the database, through a
	// host over it, which commits when fn returns nil.
	Transaction(ctx context.Context, fn func(Host) error) error
	// As is ctx reading as principal, as a generated route binds its
	// caller (orm.WithPrincipal), and System is ctx reading every row.
	As(ctx context.Context, principal access.Principal) context.Context
	System(ctx context.Context) context.Context
	// Conditions are the conditions that keep a read of the table
	// typeName to what the caller of ctx may see, each value appended to
	// args and its placeholder numbered after offset: the ORM's
	// AccessConditions, the predicate its repositories apply (D156, D180).
	Conditions(ctx context.Context, typeName string, args *[]any, offset int) []string
	// Definitions are the database's calendar sources and search index,
	// as the ORM is generated with them.
	Definitions() *Definitions
	// Grants are the database's @accessGrants table's, nil for a database
	// with none, which a calendar feed then cannot be issued in.
	Grants() Grants
}

// Grants are the grants of a database's @accessGrants table (D156) a
// calendar feed is (D152), through the generated ORM's.
type Grants interface {
	// Issue makes a grant and its token, writing as ctx is.
	Issue(ctx context.Context, input access.IssueGrantInput) (id, token string, err error)
	// Revoke revokes the grant whose id is id at at.
	Revoke(ctx context.Context, id string, at time.Time) error
	// ByID and ByToken read a grant as the system, false for none.
	ByID(ctx context.Context, id string) (access.Grant, bool, error)
	ByToken(ctx context.Context, token string) (access.Grant, bool, error)
	// Ended is why grant is not active at now, "" when it is.
	Ended(ctx context.Context, grant access.Grant, now time.Time) (string, error)
	// RolePermissions are the permissions of the UserRole row named role,
	// none for a role that is no row.
	RolePermissions(ctx context.Context, role string) ([]string, error)
}

// Caller is whom a read across tables keeps to: its principal, whole, and
// the permissions it holds, which a source's or a table's permission
// (CalendarOptions.Permissions, SearchOptions.Permissions) is checked
// against. The zero Caller is the anonymous caller.
type Caller struct {
	Principal   access.Principal
	Permissions []string
}

// holds reports whether the caller holds permission, by name.
func (c Caller) holds(permission string) bool {
	return slices.Contains(c.Permissions, permission)
}

// Invalid is a request a read across tables cannot read: a route answers
// it 400.
type Invalid string

func (e Invalid) Error() string { return string(e) }

func invalid(format string, args ...any) error {
	return Invalid(fmt.Sprintf(format, args...))
}

// CalendarOccurrences is the occurrences sub-table of a table's
// Recurrence, which an occurrences source reads: the table and its
// columns of the row's key, an occurrence's key, its wall due, its due
// instant and its state, each quoted.
type CalendarOccurrences struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	Key   string `json:"key"`
	Due   string `json:"due,omitempty"`
	DueAt string `json:"dueAt"`
	State string `json:"state"`
}

// CalendarSource is one @calendarSource of a table (D152): its name, kind
// and colour key, and the columns, quoted, its title, its field and end,
// and its owner are of; Timestamp when its field is a date-time, and
// OwnerUUID when its owner holds a UUID.
type CalendarSource struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Color     string `json:"color,omitempty"`
	Title     string `json:"title"`
	Field     string `json:"field,omitempty"`
	End       string `json:"end,omitempty"`
	Timestamp bool   `json:"timestamp,omitempty"`
	Owner     string `json:"owner,omitempty"`
	OwnerUUID bool   `json:"ownerUuid,omitempty"`
	// Permission is the permission the schema's source needs (#601);
	// CalendarOptions.Permissions may name another, which wins.
	Permission string `json:"permission,omitempty"`
	// TitleSealed says the title's column holds an Encrypted<T> field's
	// envelope (D85, #616), which the read opens through the host's keys
	// (Unsealer), bound to the table type, TitleField and the row's key.
	TitleSealed bool                 `json:"titleSealed,omitempty"`
	TitleField  string               `json:"titleField,omitempty"`
	Occurrences *CalendarOccurrences `json:"occurrences,omitempty"`
}

// Unsealer is a Host that holds the keys of its database's Encrypted<T>
// fields (D85): a calendar source titled by one opens each title through
// them (#616). A Host that is none reads such a title as an error that
// wraps fieldcrypt.ErrNoKeys.
type Unsealer interface {
	FieldKeys() *fieldcrypt.Keyring
}

// permissionUnder is the permission a caller needs to read the source
// under options, and whether it needs one: the one options name for it,
// else the schema's.
func (s CalendarSource) permissionUnder(options CalendarOptions) (string, bool) {
	if permission, ok := options.Permissions[s.Name]; ok {
		return permission, true
	}
	return s.Permission, s.Permission != ""
}

// CalendarTable is a table with calendar sources: the table and its key
// column, quoted, the key's SQL type, whether its wire form is base62,
// its live condition (a soft-deleting table's), and its sources.
type CalendarTable struct {
	Table   string           `json:"table"`
	Key     string           `json:"key"`
	KeyType string           `json:"keyType"`
	UUID    bool             `json:"uuid,omitempty"`
	Live    string           `json:"live,omitempty"`
	Sources []CalendarSource `json:"sources"`
}

// SearchAudience are an @audience table's resolved columns, quoted.
type SearchAudience struct {
	Effective string `json:"effective"`
	Agents    string `json:"agents"`
}

// SearchComments is a table's Comments messages sub-table, whose posted
// bodies an index row holds.
type SearchComments struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	Body  string `json:"body"`
	State string `json:"state"`
	Order string `json:"order"`
}

// SearchFiles is a table's attachments sub-table, whose files' extracted
// text an index row holds.
type SearchFiles struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	Text  string `json:"text"`
	State string `json:"state"`
}

// Searchable is a @searchable table's index (D155), member for member the
// TypeScript runtime's SearchableDefinition: the index table and the
// expression of an index row's weighed words over it as si; the table,
// its key column and the key's SQL type, and whether its wire form is
// base62; the columns an index row's title, body and scope are of; a
// soft-deleting table's deleted_at; an @audience table's resolved
// columns; and the sub-tables whose text it holds too.
type Searchable struct {
	Index     string          `json:"index"`
	Document  string          `json:"document"`
	Table     string          `json:"table"`
	Key       string          `json:"key"`
	KeyType   string          `json:"keyType"`
	UUID      bool            `json:"uuid,omitempty"`
	Title     string          `json:"title"`
	Body      []string        `json:"body,omitempty"`
	Scope     string          `json:"scope,omitempty"`
	DeletedAt string          `json:"deletedAt,omitempty"`
	Audience  *SearchAudience `json:"audience,omitempty"`
	Comments  *SearchComments `json:"comments,omitempty"`
	Files     *SearchFiles    `json:"files,omitempty"`
	// FTS is the FTS5 table of the index's words on SQLite, quoted, which
	// a search there matches and ranks in place of Document, and
	// IndexAudience how the index's audience, a JSON array, is compared
	// there (D186).
	FTS           string               `json:"fts,omitempty"`
	IndexAudience *SearchIndexAudience `json:"indexAudience,omitempty"`
}

// Definitions are what the generated ORM is generated with: its tables'
// calendar sources and its @searchable tables, each by type.
type Definitions struct {
	Calendar   map[string]CalendarTable
	Searchable map[string]Searchable
}

// MustParse reads the definitions the generated ORM holds as JSON, each
// "" for none; it panics on JSON no generator wrote.
func MustParse(calendar, searchable string) *Definitions {
	out := &Definitions{Calendar: map[string]CalendarTable{}, Searchable: map[string]Searchable{}}
	if calendar != "" {
		if err := json.Unmarshal([]byte(calendar), &out.Calendar); err != nil {
			panic(fmt.Sprintf("crosstable: the calendar definition: %v", err))
		}
	}
	if searchable != "" {
		if err := json.Unmarshal([]byte(searchable), &out.Searchable); err != nil {
			panic(fmt.Sprintf("crosstable: the search definition: %v", err))
		}
	}
	return out
}

// sortedKeys are a map's keys, sorted.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// ---- row values ----

func text(row Row, column string) string {
	switch v := row[column].(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	case time.Time:
		return iso(v)
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

func integer(row Row, column string) int64 {
	switch v := row[column].(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		var n int64
		_, _ = fmt.Sscan(v, &n)
		return n
	}
	return 0
}

// iso is an instant in RFC 3339, in UTC, to the millisecond, as
// JavaScript's toISOString writes one.
func iso(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// wireKey is a key's wire form, given its column's text.
func wireKey(uuid bool, key string) string {
	if uuid {
		return rowrefs.UUIDFromDB(key)
	}
	return key
}

// sameSubject reports whether two subjects are one, a UUID in either form.
func sameSubject(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	x, y := rowrefs.UUIDToDB(a), rowrefs.UUIDToDB(b)
	if x != "" && y != "" {
		return x == y
	}
	return a == b
}
