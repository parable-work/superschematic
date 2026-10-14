package rustgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The queues of a DB schema in Rust (D53, D65; docs/stack-model.md,
// section 8.8). The types crate carries each queue as data, in
// src/queues.rs, as the TypeScript types carry it in queues.ts: a constant
// per queue holding its descriptor as JSON, with its table, its arguments,
// the column each field of its message writes, and its statements on
// Postgres, which sqlutil.PostgresQueueStatements writes for the Go ORM
// and the TypeScript queue client too. The HTTP runtime's queue module
// parses it (QueueDefinition::parse) and runs it: an enqueue in the
// caller's transaction, and the claims a worker makes. The descriptor's
// members are those of the TypeScript QueueDefinition, so one parser's
// keys are the other's, and the crate depends on no runtime.

// QueuesFile is the file of a types crate that holds its queues.
const QueuesFile = "queues.rs"

// QueueInfo is a queue as queues.rs writes it.
type QueueInfo struct {
	// Name is the @queue class, the message's type, and Const the name of
	// the constant queues.rs holds its descriptor in (ORDER_PLACED_QUEUE).
	Name  string
	Const string

	// DocLines are the lines of the class's comment.
	DocLines []string

	// Descriptor is the queue's descriptor as queues.rs holds it, in
	// JSON. Its members are the TypeScript QueueDefinition's.
	Descriptor QueueDescriptor

	// Literal is Descriptor as the Rust raw string literal the constant
	// holds. Empty for a queue queues.rs leaves out.
	Literal string

	// Unsupported says why queues.rs leaves the queue out: a field whose
	// value the Rust queue client cannot write and read back yet. Empty
	// for a queue it writes.
	Unsupported string
}

// QueueDescriptor is a queue's descriptor (D65): the members of the
// TypeScript QueueDefinition that tsgen's queues.ts writes, with their
// names, so the Rust queue client reads what the TypeScript one reads.
type QueueDescriptor struct {
	// Name is the @queue class.
	Name string `json:"name"`

	// Table is the queue's table, quoted for SQL where it must be.
	Table string `json:"table"`

	// Retries, BackoffSeconds and LeaseSeconds are the queue's arguments
	// with their defaults (registry.QueueSettingsOf).
	Retries        int `json:"retries"`
	BackoffSeconds int `json:"backoffSeconds"`
	LeaseSeconds   int `json:"leaseSeconds"`

	// Fields are the message's fields, in declaration order, the order a
	// claim returns their columns in.
	Fields []QueueField `json:"fields"`

	// Statements are the queue's statements on Postgres, the Go ORM's.
	Statements QueueStatements `json:"statements"`
}

// QueueField is a field of a queue's message and its column.
type QueueField struct {
	// Name is the message's member, as its type serializes it, and Column
	// its column, quoted for SQL where it must be, as the Go ORM quotes it.
	Name   string `json:"name"`
	Column string `json:"column"`

	// JSON is a value the column holds as JSON: a @jsonField type, a map,
	// a list of lists, or a JSON scalar, as the Go ORM marshals each.
	JSON bool `json:"json,omitempty"`

	// List is a list the column holds as a Postgres array.
	List bool `json:"list,omitempty"`

	// Default is the schema's default of an enum field, which a message
	// that leaves the field unset writes, as the Go ORM writes it.
	Default string `json:"default,omitempty"`
}

// QueueStatements are a queue's statements on Postgres
// (sqlutil.QueueStatements), by the names queues.ts gives them.
type QueueStatements struct {
	Insert   string `json:"insert"`
	Expire   string `json:"expire"`
	Claim    string `json:"claim"`
	Complete string `json:"complete"`
	Fail     string `json:"fail"`
	Extend   string `json:"extend"`
	Release  string `json:"release"`
}

// queueStatements are s by the names queues.ts gives them.
func queueStatements(s sqlutil.QueueStatements) QueueStatements {
	return QueueStatements{
		Insert:   s.Insert,
		Expire:   s.Expire,
		Claim:    s.Claim,
		Complete: s.Complete,
		Fail:     s.Fail,
		Extend:   s.Extend,
		Release:  s.Release,
	}
}

// JSON is the descriptor as queues.rs writes it: indented, with no HTML
// escapes, since its statements compare with `<=`.
func (d QueueDescriptor) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return nil, fmt.Errorf("rustgen: encode the descriptor of queue %s: %w", d.Name, err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// QueueConst is the name of the constant queues.rs holds the descriptor of
// the queue whose class is name in: ORDER_PLACED_QUEUE.
func QueueConst(name string) string {
	return strings.ToUpper(codegen.ToSnakeCase(name)) + "_QUEUE"
}

// RustQueues reads the queues of schema, sorted by name, each with what
// queues.rs writes or why it leaves the queue out.
func RustQueues(schema *ir.Schema) ([]QueueInfo, error) {
	sqlTypes := sqlutil.ScalarSQLTypes(schema)
	var queues []QueueInfo
	for _, td := range schema.Queues() {
		if err := registry.CheckQueue(td.Queue); err != nil {
			return nil, fmt.Errorf("queue %s: %w", td.Name, err)
		}
		settings := registry.QueueSettingsOf(td.Queue)
		q := QueueInfo{
			Name:     td.Name,
			Const:    QueueConst(td.Name),
			DocLines: queueDocLines(codegen.DocText(td.Description, td.Comment)),
			Descriptor: QueueDescriptor{
				Name:           td.Name,
				Table:          sqlutil.QuoteIdentifier(codegen.ToSnakeCase(td.Name) + ir.QueueTableSuffix),
				Retries:        settings.Retries,
				BackoffSeconds: settings.BackoffSeconds,
				LeaseSeconds:   settings.LeaseSeconds,
				Fields:         []QueueField{},
			},
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
			q.Descriptor.Fields = append(q.Descriptor.Fields, field)
			columns = append(columns, field.Column)
		}
		q.Descriptor.Statements = queueStatements(sqlutil.PostgresQueueStatements(q.Descriptor.Table, columns))
		if q.Unsupported == "" {
			text, err := q.Descriptor.JSON()
			if err != nil {
				return nil, err
			}
			q.Literal = rustutil.RawString(string(text))
		}
		queues = append(queues, q)
	}
	return queues, nil
}

// queueField is a field of a queue's message as queues.rs writes it, or
// why the Rust queue client cannot hold it yet. It classifies the field as
// the Go ORM (ormgen's extractField) and the TypeScript types (tsgen's
// queueField) do, so the three write each column alike, which
// TestQueuesDescribeWhatTypeScriptWrites holds it to.
func queueField(schema *ir.Schema, f *ir.FieldDef, sqlTypes map[string]string) (QueueField, string) {
	ref := f.TypeRef
	field := QueueField{
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
	case !field.JSON && sqlType == "POINT":
		return field, fmt.Sprintf("is a %s, a POINT column, which the Rust queue client does not read yet", ref.Name)
	case !field.JSON && sqlType == "INTERVAL":
		return field, fmt.Sprintf("is a %s, an INTERVAL column, which the Rust queue client does not read yet", ref.Name)
	}
	if enum := schema.Enums[ref.Name]; enum != nil && f.Required && !ref.IsArray && !ref.IsMap && f.Default != nil {
		field.Default = *f.Default
	}
	return field, ""
}

// queueDocLines splits a comment into lines, or returns none for an empty
// one.
func queueDocLines(doc string) []string {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(doc, "\n") {
		lines = append(lines, strings.TrimSpace(line))
	}
	return lines
}

// WrittenQueues are the queues queues.rs writes.
func (o *ModuleOutput) WrittenQueues() []QueueInfo {
	var out []QueueInfo
	for _, q := range o.Queues {
		if q.Unsupported == "" {
			out = append(out, q)
		}
	}
	return out
}

// LeftOutQueues are the queues queues.rs leaves out, each with why.
func (o *ModuleOutput) LeftOutQueues() []QueueInfo {
	var out []QueueInfo
	for _, q := range o.Queues {
		if q.Unsupported != "" {
			out = append(out, q)
		}
	}
	return out
}
