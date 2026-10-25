package ir

// EnqueueOnChange is an `@enqueueOnChange` declaration of a DB table
// (D80): a trigger the database runs after a write to the table's rows,
// which enqueues a message of a queue of the same schema whatever wrote
// the row: a generated repository, a script or psql. sqlgen writes the
// trigger in each dialect the service lists, and the migration planner
// adds, changes and drops it as it does a @versioned table's.
type EnqueueOnChange struct {
	// Queue is the `@queue` class of the schema whose message the trigger
	// enqueues.
	Queue string `json:"queue" yaml:"queue"`

	// On are the writes that enqueue, in the order EnqueueEvents lists
	// them: an insert and an update enqueue from the row as written, a
	// delete from the row as it was.
	On []string `json:"on" yaml:"on"`

	// Fields are the fields of the table whose change makes an update
	// enqueue: an update that changes none of them, as IS DISTINCT FROM
	// compares them, enqueues nothing. Empty enqueues on every update. A
	// relation is its key's column.
	Fields []string `json:"fields,omitempty" yaml:"fields,omitempty"`

	// When are the conditions the row must meet to enqueue, all of them,
	// in order: the row as written for an insert or an update, as it was
	// for a delete.
	When []*EnqueueCondition `json:"when,omitempty" yaml:"when,omitempty"`

	// Message maps each field of the message the trigger sets to the field
	// of the table whose column it takes. A field it leaves out takes its
	// column's default, so every required field of the message is mapped.
	Message map[string]string `json:"message" yaml:"message"`
}

// EnqueueCondition is one condition of an EnqueueOnChange: a field of the
// table that is null, that is not, or that equals a literal, as a
// @projection's literal rules write one. Exactly one of IsNull, NotNull
// and Equals is set.
type EnqueueCondition struct {
	// Field is a field of the table; a relation is its key's column.
	Field string `json:"field" yaml:"field"`

	IsNull  bool `json:"isNull,omitempty" yaml:"isNull,omitempty"`
	NotNull bool `json:"notNull,omitempty" yaml:"notNull,omitempty"`

	// Equals is a string, a number or a boolean the field's value equals:
	// an enum field's member, a text, a number or a boolean.
	Equals any `json:"equals,omitempty" yaml:"equals,omitempty" jsonschema:"oneof_type=string;number;boolean"`
}

// The writes an EnqueueOnChange enqueues on.
const (
	EnqueueOnInsert = "insert"
	EnqueueOnUpdate = "update"
	EnqueueOnDelete = "delete"
)

// EnqueueEvents are the writes an EnqueueOnChange takes, in the order its
// On lists them.
func EnqueueEvents() []string {
	return []string{EnqueueOnInsert, EnqueueOnUpdate, EnqueueOnDelete}
}
