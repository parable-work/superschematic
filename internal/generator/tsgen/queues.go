package tsgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The queues of a DB schema in TypeScript (D53; docs/stack-model.md,
// section 8.8). There is no TypeScript ORM, so the database's types package
// carries each queue as data, in queues.ts, which package.json exports as
// "./queues", as it carries the identity descriptor (D50): its table, its
// arguments, the column each field of its message writes, and its
// statements on Postgres, which sqlutil.PostgresQueueStatements writes for
// the Go ORM too. The HTTP runtime's ./postgres entry runs them: enqueue in
// the caller's transaction, and the claims a worker makes. The file imports
// nothing but the message's type, so the package gains no dependency.

// QueuesFile is the file of a types package that holds its queues.
const QueuesFile = "queues.ts"

// QueueInfo is a queue as queues.ts writes it.
type QueueInfo struct {
	// Name is the @queue class, the message's type, and Const the name of
	// the definition queues.ts exports (orderPlacedQueue).
	Name  string
	Const string

	// DocLines are the lines of the class's comment.
	DocLines []string

	// Table is the queue's table, quoted for SQL where it must be.
	Table string

	// Retries, BackoffSeconds and LeaseSeconds are the queue's arguments
	// with their defaults (registry.QueueSettingsOf).
	Retries        int
	BackoffSeconds int
	LeaseSeconds   int

	// Fields are the message's fields, in declaration order.
	Fields []QueueFieldInfo

	// Statements are the queue's statements on Postgres, the Go ORM's.
	Statements sqlutil.QueueStatements

	// Unsupported says why queues.ts leaves the queue out: a field whose
	// value the TypeScript queue client cannot write and read back yet.
	// Empty for a queue it writes.
	Unsupported string
}

// QueueFieldInfo is a field of a queue's message and its column.
type QueueFieldInfo struct {
	// Name is the message's member, and Column its column, quoted for SQL
	// where it must be, as the Go ORM quotes it.
	Name   string
	Column string

	// JSON is a value the column holds as JSON, written with
	// JSON.stringify: a @jsonField type, a map, a list of lists, or a JSON
	// scalar, as the Go ORM marshals each.
	JSON bool

	// List is a list the column holds as a Postgres array.
	List bool

	// Default is the schema's default of an enum field, which a message
	// that leaves the field unset writes, as the Go ORM writes it.
	Default string

	// Point is a location, {lat, lon}, the column holds as a POINT, which
	// the client writes as (lon,lat), as the Go ORM writes a pgtype.Point.
	Point bool

	// Interval is a duration the column holds as an INTERVAL, which the
	// client writes as its whole microseconds, as the Go ORM writes a
	// time.Duration through pgx.
	Interval bool
}

// QueuesEntry is the entry of a types package that exports its queues, as
// an import names it after the package's name.
const QueuesEntry = "/queues"

// QueueConst is the name queues.ts exports the definition of the queue
// whose class is name as: orderPlacedQueue.
func QueueConst(name string) string { return lowerFirst(name) + "Queue" }

// TypeScriptQueues reads the queues of schema, sorted by name, each with
// what queues.ts writes or why it leaves the queue out.
func TypeScriptQueues(schema *ir.Schema) ([]QueueInfo, error) {
	sqlTypes := sqlutil.ScalarSQLTypes(schema)
	var queues []QueueInfo
	for _, td := range schema.Queues() {
		if err := registry.CheckQueue(td.Queue); err != nil {
			return nil, fmt.Errorf("queue %s: %w", td.Name, err)
		}
		settings := registry.QueueSettingsOf(td.Queue)
		q := QueueInfo{
			Name:           td.Name,
			Const:          QueueConst(td.Name),
			DocLines:       queueDocLines(codegen.DocText(td.Description, td.Comment)),
			Table:          sqlutil.QuoteIdentifier(codegen.ToSnakeCase(td.Name) + ir.QueueTableSuffix),
			Retries:        settings.Retries,
			BackoffSeconds: settings.BackoffSeconds,
			LeaseSeconds:   settings.LeaseSeconds,
		}
		var columns []string
		for _, f := range td.Fields {
			if f == nil {
				continue
			}
			field, unsupported := queueField(schema, f, sqlTypes)
			if unsupported != "" && q.Unsupported == "" {
				q.Unsupported = fmt.Sprintf("field %s %s", f.Name, unsupported)
			}
			q.Fields = append(q.Fields, field)
			columns = append(columns, field.Column)
		}
		q.Statements = sqlutil.PostgresQueueStatements(q.Table, columns)
		queues = append(queues, q)
	}
	return queues, nil
}

// queueField is a field of a queue's message as queues.ts writes it, or
// why the TypeScript queue client cannot hold it yet. It classifies the
// field as the Go ORM does (ormgen's extractField), so the two write each
// column alike.
func queueField(schema *ir.Schema, f *ir.FieldDef, sqlTypes map[string]string) (QueueFieldInfo, string) {
	ref := f.TypeRef
	field := QueueFieldInfo{
		Name:   f.Name,
		Column: sqlutil.QuoteIdentifier(codegen.ToSnakeCase(f.Name)),
	}
	if _, union := schema.Unions[ref.Name]; union {
		return field, "is a union, which a queue's message does not hold yet"
	}
	sqlType := strings.ToUpper(sqlTypes[ref.Name])
	target := schema.Types[ref.Name]
	anyJSON := sqlType == "JSON" || sqlType == "JSONB"
	field.JSON = f.JsonField || ref.IsMap || ref.IsArrayOfArrays || (target != nil && target.JsonField) || (anyJSON && !ref.IsArray)
	field.List = ref.IsArray && !field.JSON
	switch {
	case field.List && anyJSON:
		return field, "is a list of JSON values, which a queue's message does not hold yet; hold it in a @jsonField type"
	case field.List && sqlType == "POINT":
		return field, fmt.Sprintf("is a list of %s values, a POINT[] column, which the TypeScript queue client does not read yet", ref.Name)
	case field.List && sqlType == "INTERVAL":
		return field, fmt.Sprintf("is a list of %s values, an INTERVAL[] column, which the TypeScript queue client does not read yet", ref.Name)
	}
	field.Point = !field.JSON && !field.List && sqlType == "POINT"
	field.Interval = !field.JSON && !field.List && sqlType == "INTERVAL"
	if enum := schema.Enums[ref.Name]; enum != nil && f.Required && !ref.IsArray && !ref.IsMap && f.Default != nil {
		field.Default = *f.Default
	}
	return field, ""
}

// queueDocLines splits a comment into lines, each with "*/" escaped so it
// cannot close the class's JSDoc early (codegen.JSDocEscape), or returns
// none for an empty one.
func queueDocLines(doc string) []string {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(doc, "\n") {
		lines = append(lines, codegen.JSDocEscape(strings.TrimSpace(line)))
	}
	return lines
}

// WrittenQueues are the queues queues.ts writes.
func (o *ModuleOutput) WrittenQueues() []QueueInfo {
	var out []QueueInfo
	for _, q := range o.Queues {
		if q.Unsupported == "" {
			out = append(out, q)
		}
	}
	return out
}

// LeftOutQueues are the queues queues.ts leaves out, each with why.
func (o *ModuleOutput) LeftOutQueues() []QueueInfo {
	var out []QueueInfo
	for _, q := range o.Queues {
		if q.Unsupported != "" {
			out = append(out, q)
		}
	}
	return out
}

// tsTemplateLiteral is s as a TypeScript template literal, which holds its
// newlines as they are. A queue's statement holds no backquote, backslash
// or `${`, since its identifiers are snake case; one that did is refused
// rather than written wrong.
func tsTemplateLiteral(s string) (string, error) {
	if strings.ContainsAny(s, "`\\") || strings.Contains(s, "${") {
		return "", fmt.Errorf("tsgen: the statement %q holds a character a template literal escapes", s)
	}
	return "`" + s + "`", nil
}
