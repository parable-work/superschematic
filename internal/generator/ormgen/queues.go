package ormgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Queue is a queue of the schema (docs/stack-model.md, section 8.8, D53):
// its message type, its table, its arguments with their defaults, and the
// message's fields, each a column of the table. The ORM enqueues a message
// in the caller's transaction (EnqueueOrderPlaced) and claims, completes,
// fails, extends and releases messages for a worker (OrderPlacedQueue), on
// Postgres, the Go ORM's one dialect. A field reaches its column and comes
// back as a table's field does through a repository: the same conversions,
// the same JSON encoding of a JSON column, and the same scans.
type Queue struct {
	// Name is the @queue class, the message's type in the types package.
	Name string

	// DocLines are the lines of the class's comment, which the queue type's
	// comment quotes after a blank line; none without one.
	DocLines []string

	// TableName is the queue's table, QuotedTableName quoted for SQL.
	TableName       string
	QuotedTableName string

	// Retries, BackoffSeconds, MaxBackoffSeconds, LeaseSeconds and
	// RetainSeconds are the queue's arguments with their defaults
	// (registry.QueueSettingsOf).
	Retries           int
	BackoffSeconds    int
	MaxBackoffSeconds int
	LeaseSeconds      int
	RetainSeconds     int

	// Fields are the message's fields, in declaration order, each the
	// column of its name.
	Fields []Field

	// Statements are the queue's statements on Postgres, which the
	// TypeScript types' queues.ts carries too (sqlutil.QueueStatements).
	Statements sqlutil.QueueStatements

	// SQLiteFields are the message's fields as queues_sqlite.go converts
	// them, and SQLiteStatements the queue's statements on SQLite
	// (sqlutil.SQLiteQueueStatements), which queues.ts and queues.rs carry
	// too, for a DB service that lists the sqlite dialect; nil otherwise,
	// or for a queue whose message SQLiteUnsupported says SQLite's clients
	// cannot hold (D53, amended).
	SQLiteFields      []SQLiteQueueField
	SQLiteStatements  *sqlutil.QueueStatements
	SQLiteUnsupported string
}

// SQLiteQueueField is a field of a queue's message as the SQLite queue
// converts it between its JSON form, the member Name, and its column, as
// the TypeScript and Rust clients mark it: a JSON value as its text, a
// list as a JSON array, an enum's Default for an unset member, a UUID as
// its lowercase hyphenated text, a timestamp in RFC 3339 in UTC, a boolean
// as 1 or 0, and a duration as its canonical text.
type SQLiteQueueField struct {
	Name      string
	Column    string
	JSON      bool
	List      bool
	Default   string
	UUID      bool
	Timestamp bool
	Boolean   bool
	Interval  bool
}

// queuesOf reads the schema's queues, sorted by name. It refuses a field
// the queues do not hold yet: a union, a list of Generic.JSON values in a
// native array, and a list of locations or durations, a POINT[] or an
// INTERVAL[] column, which the TypeScript and Rust clients leave out too. A
// single location or duration it holds (D53, amended).
func queuesOf(schema *ir.Schema, scalars map[string]scalarLookup, unionNames, enumNames map[string]bool, sqlite bool) ([]Queue, error) {
	sqlTypes := sqlutil.ScalarSQLTypes(schema)
	var queues []Queue
	for _, td := range schema.Queues() {
		if err := registry.CheckQueue(td.Queue); err != nil {
			return nil, fmt.Errorf("queue %s: %w", td.Name, err)
		}
		settings := registry.QueueSettingsOf(td.Queue)
		table := sqlutil.QueueTableName(td.Name)
		q := Queue{
			Name:              td.Name,
			DocLines:          docLines(codegen.DocText(td.Description, td.Comment)),
			TableName:         table,
			QuotedTableName:   sqlutil.QuoteIdentifier(table),
			Retries:           settings.Retries,
			BackoffSeconds:    settings.BackoffSeconds,
			MaxBackoffSeconds: settings.MaxBackoffSeconds,
			LeaseSeconds:      settings.LeaseSeconds,
			RetainSeconds:     settings.RetainSeconds,
		}
		for _, f := range td.Fields {
			if f == nil {
				continue
			}
			field := extractField(f, schema, scalars, unionNames)
			if enumNames[field.IRType] && field.IsRequired && !field.IsArray && f.Default != nil {
				field.EnumDefault = *f.Default
			}
			switch {
			case field.IsUnion:
				return nil, fmt.Errorf("queue %s: field %s is a union, which a queue's message does not hold yet; hold it in a @jsonField type", td.Name, f.Name)
			case field.NativeGenericJSONList():
				return nil, fmt.Errorf("queue %s: field %s is a list of Generic.JSON values, which a queue's message does not hold yet; hold it in a @jsonField type", td.Name, f.Name)
			case field.IsGeoPointList:
				return nil, fmt.Errorf("queue %s: field %s is a list of %s values, a POINT[] column, which a queue's message does not hold yet; hold it in a @jsonField type", td.Name, f.Name, field.IRType)
			case field.IsIntervalList:
				return nil, fmt.Errorf("queue %s: field %s is a list of %s values, an INTERVAL[] column, which a queue's message does not hold yet; hold it in a @jsonField type", td.Name, f.Name, field.IRType)
			}
			q.Fields = append(q.Fields, field)
			sqlType := sqlTypes[f.TypeRef.Name]
			kind := ""
			if !field.IsJSONField {
				kind = sqlutil.QueueKindOf(sqlType)
				if refusal := sqlutil.SQLiteQueueRefusal(sqlType); refusal != "" && q.SQLiteUnsupported == "" {
					q.SQLiteUnsupported = fmt.Sprintf("field %s %s", f.Name, refusal)
				}
			}
			q.SQLiteFields = append(q.SQLiteFields, SQLiteQueueField{
				Name:      f.Name,
				Column:    field.QuotedDBName,
				JSON:      field.IsJSONField,
				List:      field.NativeList(),
				Default:   field.EnumDefault,
				UUID:      kind == sqlutil.QueueKindUUID,
				Timestamp: kind == sqlutil.QueueKindTimestamp,
				Boolean:   kind == sqlutil.QueueKindBoolean,
				Interval:  field.IsInterval,
			})
		}
		columns := make([]string, len(q.Fields))
		for i, f := range q.Fields {
			columns[i] = f.QuotedDBName
		}
		spec := sqlutil.QueueSpecOf(td, q.QuotedTableName, columns, q.RetainSeconds > 0)
		q.Statements = sqlutil.PostgresQueueStatements(spec)
		switch {
		case !sqlite:
			q.SQLiteFields, q.SQLiteUnsupported = nil, ""
		case q.SQLiteUnsupported != "":
			q.SQLiteFields = nil
		default:
			statements := sqlutil.SQLiteQueueStatements(spec)
			q.SQLiteStatements = &statements
		}
		queues = append(queues, q)
	}
	return queues, nil
}

// SQLiteQueues are the queues queues_sqlite.go claims on SQLite.
func (o *ORMOutput) SQLiteQueues() []Queue {
	var out []Queue
	for _, q := range o.Queues {
		if q.SQLiteStatements != nil {
			out = append(out, q)
		}
	}
	return out
}

// SQLiteLeftOut are the queues of a database that lists the sqlite dialect
// which queues_sqlite.go leaves out, each with why.
func (o *ORMOutput) SQLiteLeftOut() []Queue {
	var out []Queue
	for _, q := range o.Queues {
		if q.SQLiteUnsupported != "" {
			out = append(out, q)
		}
	}
	return out
}

// docLines splits a comment into lines, after a blank one, or returns none
// for an empty comment.
func docLines(doc string) []string {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return nil
	}
	lines := []string{""}
	for _, line := range strings.Split(doc, "\n") {
		lines = append(lines, strings.TrimSpace(line))
	}
	return lines
}

// QueueScanTemp is the Go type of the variable a claim scans the field's
// column into before it copies the value into the message, as a
// repository scans a table's field, or "" when it scans into the message's
// field itself.
func (f Field) QueueScanTemp() string {
	switch {
	case f.IsJSONField:
		return "[]byte"
	case f.ScansElementPointers():
		return "[]*" + f.GoType
	case f.IsGeoPoint:
		return "pgtype.Point"
	case f.IsInterval && f.IsNullableScalar:
		return "*time.Duration"
	case f.IsInterval:
		return "time.Duration"
	case !f.IsArray && f.IsDateLike:
		return "pgtype.Date"
	case !f.IsRequired && !f.IsArray && f.GoType == "string":
		return "sql.NullString"
	case !f.IsRequired && !f.IsArray:
		return "*" + f.GoType
	}
	return ""
}

// QueuesImportSQL, QueuesImportReflect and QueuesImportPgtype report
// whether queues.go uses database/sql (an optional string field), reflect
// (an optional field whose zero value is no nil) and pgtype (a date field,
// or a location, which crosses as a POINT).
func (o *ORMOutput) QueuesImportSQL() bool {
	return queuesNeed(o.Queues, func(f Field) bool { return f.QueueScanTemp() == "sql.NullString" })
}

func (o *ORMOutput) QueuesImportReflect() bool {
	return queuesNeed(o.Queues, func(f Field) bool { return (!f.IsRequired || f.IsAutoGenerated) && !f.OptionalNilCheck })
}

func (o *ORMOutput) QueuesImportPgtype() bool {
	return queuesNeed(o.Queues, func(f Field) bool { return f.QueueScanTemp() == "pgtype.Date" || f.IsGeoPoint })
}

// HasInterval reports whether a field of the queue is a duration, which
// crosses as its microseconds rather than as a table's field does.
func (q Queue) HasInterval() bool {
	return q.needs(func(f Field) bool { return f.IsInterval })
}

// needs reports whether a field of the queue matches.
func (q Queue) needs(match func(Field) bool) bool {
	for _, f := range q.Fields {
		if match(f) {
			return true
		}
	}
	return false
}

// queuesNeed reports whether a field of any queue matches.
func queuesNeed(queues []Queue, match func(Field) bool) bool {
	for _, q := range queues {
		if q.needs(match) {
			return true
		}
	}
	return false
}
