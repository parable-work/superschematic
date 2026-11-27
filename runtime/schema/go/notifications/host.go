// Package notifications is what the notifier of D153 runs through in Go
// (D190): the Host the generated ORM of a database that uses the notifier
// implements, over its pool or a transaction and its repositories, and
// the definitions it is generated with: the notifier's tables, a part
// each, by their columns, and every table's key, events and relations.
//
// The notifier itself is the HTTP runtime's notify package, since it
// sends through the channel adapters an environment configures (D159),
// which the ORM cannot import (D184 keeps rowrefs here for the same
// reason). Nothing here opens a connection or sends anything.
//
// The notifier's own tables are the loader's (internal/loader/
// notifications), the same fields in every database, so the notifier
// reads and writes them by statements over the columns the definitions
// name. An application's table it reads through the table's repository,
// as the caller a context names (Host.As): Record, Find and Update take a
// filter or an update in the repositories' filter syntax, as JSON, so a
// recipient's read of a record keeps to the record's audience and the
// recipient's grants as any read of theirs does (D156, D180). Postgres
// alone, as the loader holds the notifier to.
package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// Row is one row a statement returned, by the name its column was read
// as.
type Row map[string]any

// Host is the database as the notifier runs through it: the generated
// ORM's Database, or a transaction of it. Its statements read every row;
// what a recipient may see is what its Record and Find keep to.
type Host interface {
	// Query runs a statement and returns its rows; Exec runs one and
	// returns the rows it touched.
	Query(ctx context.Context, sql string, args ...any) ([]Row, error)
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	// Transaction runs fn in a transaction of the database through a host
	// over it, which commits when fn returns nil; on a host over a
	// transaction, in a savepoint of it, which an error rolls back to.
	Transaction(ctx context.Context, fn func(Host) error) error
	// In is a host over tx, the ORM's transaction (its TxInterface) a
	// caller holds, so what the notifier writes commits with the caller's
	// change or not at all.
	In(tx any) (Host, error)
	// Tx is the ORM's transaction the host runs in, nil outside one.
	Tx() any
	// As is ctx acting as principal, as a generated route binds its caller
	// (WithPrincipal); System is ctx reading every row; Acting is ctx as
	// principal whose behaviors' commands run with permissions
	// (WithBehaviorCaller), what a reply's action runs as.
	As(ctx context.Context, principal access.Principal) context.Context
	System(ctx context.Context) context.Context
	Acting(ctx context.Context, principal access.Principal, permissions []string) context.Context
	// Definitions are the notifier's tables and the database's, as the ORM
	// is generated with them.
	Definitions() *Definitions
	// Enqueue writes work to the notifier's dispatch queue in the host's
	// transaction, or one of its own outside one, due after delay.
	Enqueue(ctx context.Context, work Work, delay time.Duration) error
	// Dispatch is the dispatch queue as a worker claims it.
	Dispatch() Queue
	// Record is the row of the table typeName whose key is key, read
	// through its repository as ctx's caller, as its JSON; false when the
	// caller sees no such row.
	Record(ctx context.Context, typeName, key string) (map[string]any, bool, error)
	// Find is up to limit rows of typeName filter keeps, a filter in the
	// repositories' syntax as JSON (null for every row), through its
	// repository as ctx's caller, each as its JSON.
	Find(ctx context.Context, typeName string, filter json.RawMessage, limit int) ([]map[string]any, error)
	// Update writes update, the table's update as JSON, to the row of
	// typeName whose key is key, through its repository as ctx's caller.
	Update(ctx context.Context, typeName, key string, update json.RawMessage) error
	// Permissions are the permissions user holds through its roles (the
	// UserRoleGrant and UserRole tables); none for a database with
	// neither. RoleHolders are the users who hold the role named role.
	Permissions(ctx context.Context, user string) ([]string, error)
	RoleHolders(ctx context.Context, role string) ([]string, error)
	// ActiveGrants are holder's grants active at now, each with its role's
	// reaches (D156): what a party or an agent run reads a record through.
	// None for a database with no @accessGrants table.
	ActiveGrants(ctx context.Context, holder string, now time.Time) ([]access.GrantReach, error)
}

// Work is a message of the dispatch queue (ir.NotifierDispatch): a step
// and what it is of, each a key in its wire form.
type Work struct {
	Step         string          `json:"step"`
	Event        json.RawMessage `json:"event,omitempty"`
	Notice       string          `json:"notice,omitempty"`
	Delivery     string          `json:"delivery,omitempty"`
	Recipient    string          `json:"recipient,omitempty"`
	Subscription string          `json:"subscription,omitempty"`
}

// Claim is a claimed message of the dispatch queue.
type Claim struct {
	ID      string
	Token   string
	Attempt int
	Work    Work
}

// Queue is the dispatch queue as the generated ORM claims it (D80).
type Queue interface {
	Name() string
	Lease() time.Duration
	Claim(ctx context.Context, n int, lease time.Duration) ([]Claim, error)
	Complete(ctx context.Context, id, token string) error
	Fail(ctx context.Context, id, token, cause string) (bool, error)
	Extend(ctx context.Context, id, token string, lease time.Duration) error
	Release(ctx context.Context, id, token string) error
	Prune(ctx context.Context, limit int) (int, error)
	Slot(ctx context.Context, slots int) (release func(), ok bool, err error)
	NextDue(ctx context.Context) (time.Duration, bool, error)
}

// Column is a column of one of the notifier's tables: its name, quoted,
// and whether it holds a UUID, which the notifier writes in the
// database's form and reads in base62.
type Column struct {
	Name string `json:"name"`
	UUID bool   `json:"uuid,omitempty"`
}

// Part is one of the notifier's tables (ir.NotifierParts, and
// channelIdentities): its type, the table, quoted, and its columns by
// field.
type Part struct {
	Type    string            `json:"type"`
	Table   string            `json:"table"`
	Columns map[string]Column `json:"columns"`
}

// Event is one @event of a table (ir.EventDef).
type Event struct {
	Kind    string   `json:"kind"`
	Payload []string `json:"payload,omitempty"`
	Title   string   `json:"title,omitempty"`
	Urgency string   `json:"urgency,omitempty"`
}

// Relation is a to-one relation a recipient's path steps through: the
// table it reaches, and the member of a row's JSON that holds the target,
// its key or an object of it.
type Relation struct {
	Target string `json:"target"`
	Member string `json:"member"`
}

// Table is one of the database's tables as the notifier reads it: the
// field of its key, the events it declares, its to-one relations by name,
// and the notifier part it is, if any.
type Table struct {
	Key       string              `json:"key"`
	Events    []Event             `json:"events,omitempty"`
	Relations map[string]Relation `json:"relations,omitempty"`
	Part      string              `json:"part,omitempty"`
}

// Definitions are what the generated ORM is generated with: the
// notifier's tables by part (inbox, deliveries, routes, preferences,
// subscriptions, channelTrust, replies and channelIdentities; dispatch is
// the queue Host.Dispatch claims) and the database's tables by type.
type Definitions struct {
	Parts  map[string]Part  `json:"parts"`
	Tables map[string]Table `json:"tables"`
}

// MustParse reads the definitions the generated ORM holds as JSON; it
// panics on JSON no generator wrote.
func MustParse(definitions string) *Definitions {
	out := &Definitions{}
	if err := json.Unmarshal([]byte(definitions), out); err != nil {
		panic(fmt.Sprintf("notifications: the definitions: %v", err))
	}
	if out.Parts == nil {
		out.Parts = map[string]Part{}
	}
	if out.Tables == nil {
		out.Tables = map[string]Table{}
	}
	return out
}

// EventOf is typeName's @event of kind, false for none.
func (d *Definitions) EventOf(typeName, kind string) (Event, bool) {
	for _, event := range d.Tables[typeName].Events {
		if event.Kind == kind {
			return event, true
		}
	}
	return Event{}, false
}
