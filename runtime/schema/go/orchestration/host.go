// Package orchestration is what D157's parties and inbound path and D158's
// agent dispatcher run through in Go (D192): the Host the generated ORM of
// a database with a @party table, the conversations, a @runRequests queue
// or the @agents tables implements, over its pool or a transaction, and the
// definitions it is generated with: those tables by part with their
// columns, the @party tables, each table's behaviors' uses, and the two
// queues.
//
// The inbound path and the dispatcher are the HTTP runtime's inbound and
// agents packages, since they send through the channel adapters and start
// the sandboxes an environment configures (D159), which the ORM cannot
// import. A party's principal and whether its channel identity lapsed
// (D157's parties.ts) are here, since the notifier asks the second of
// every address it sends to.
//
// The tables are the loader's (internal/loader/conversations and
// internal/loader/agents), the same fields in every database. The runtime
// reads them by statements over the columns the definitions name, as the
// notifier reads its own (D190), and writes a table that composes
// behaviors or is @versioned through its repository (Create, and the
// notifications.Host's Update), so the core initialises and guards the
// fields its behaviors own; a behavior's operation runs through the core
// (Operation), as a generated repository's own does. Postgres alone, as
// the loader holds the features to.
package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/crosstable"
	"github.com/parable-work/superschematic/runtime/schema/go/notifications"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// The queues a Host enqueues on and claims.
const (
	// RunRequests is D157's @runRequests queue, the run requests an
	// inbound message from a party with an active grant makes.
	RunRequests = "runRequests"
	// Requests is D158's @agents("requests") queue, the dispatcher's.
	Requests = "requests"
)

// Host is the database as the inbound path and the dispatcher run through
// it: the generated ORM's Orchestration(), the notifier's host with what
// they add. A host its Transaction hands fn, and the one In returns, are
// Hosts too (Of).
type Host interface {
	notifications.Host
	// Orchestration are the definitions the ORM is generated with.
	Orchestration() *Definitions
	// Create writes a row of typeName, input its create as JSON, through
	// its repository as ctx's caller, and returns it as its JSON: the
	// core initialises the fields its behaviors own, and a @versioned
	// table records the row's first version.
	Create(ctx context.Context, typeName string, input json.RawMessage) (map[string]any, error)
	// Operation runs operation of the use use of typeName on the row key
	// names through the behaviors' core, as ctx's behavior caller, with
	// params as JSON, and returns its result: the generated repository's
	// own operation, by name.
	Operation(ctx context.Context, typeName, key, use, operation string, params json.RawMessage) (json.RawMessage, error)
	// Behaving is ctx reading every row whose behaviors' commands run as
	// subject with permissions: the dispatcher's caller, or an
	// administrator's.
	Behaving(ctx context.Context, subject string, permissions []string) context.Context
	// Presenting is ctx whose behaviors' commands present preconditions,
	// each use's entry by its use id (D104): a Lease's token.
	Presenting(ctx context.Context, preconditions map[string]any) context.Context
	// ErrorStatus is the HTTP status of an error the repositories
	// returned: 404 for a row that is not there, 403 for a write the
	// caller's access refuses, 0 for any other.
	ErrorStatus(err error) int
	// IssueGrant makes a grant (D156) and its token; RevokeGrant revokes
	// the grant whose id is id at at.
	IssueGrant(ctx context.Context, input access.IssueGrantInput) (access.Grant, string, error)
	RevokeGrant(ctx context.Context, id string, at time.Time) error
	// Grants are holder's grants, every one, or only those active at now
	// when active (newest first either way).
	Grants(ctx context.Context, holder string, active bool, now time.Time) ([]access.Grant, error)
	// GrantEnded is why grant is not active at now, "" when it is.
	GrantEnded(ctx context.Context, grant access.Grant, now time.Time) (string, error)
	// Role is the UserRole row named name, false for none.
	Role(ctx context.Context, name string) (Role, bool, error)
	// AuthenticateGrant establishes the holder of a grant's token (D156).
	AuthenticateGrant(ctx context.Context, token string, now time.Time) (access.GrantAuthentication, error)
	// References and Attachments are the database's (D154, D184), nil for
	// a database with none.
	References() *rowrefs.References
	Attachments() *rowrefs.Attachments
	// CrossTables is the database's search across tables (D155, D187),
	// nil for a database with none.
	CrossTables() crosstable.Host
	// EnqueueOn writes message, JSON of the queue's message type, to the
	// queue named (RunRequests, Requests) in the host's transaction, or
	// one of its own outside one, due after delay.
	EnqueueOn(ctx context.Context, queue string, message json.RawMessage, delay time.Duration) error
	// QueueOf is the queue named as a worker claims it; nil for a queue
	// the database does not declare.
	QueueOf(queue string) Queue
}

// Of is h as a Host: a host the generated ORM's Orchestration() returned,
// or one its Transaction or In handed on.
func Of(h notifications.Host) (Host, error) {
	if out, ok := h.(Host); ok {
		return out, nil
	}
	return nil, fmt.Errorf("orchestration: %T is no host of a database with parties or agents (D192)", h)
}

// Role is a UserRole row: its permissions, and the paths a grant of it
// reaches from its target (D156).
type Role struct {
	Name        string
	Permissions []string
	Reaches     []string
}

// Claim is a claimed message of one of the queues, as JSON of the
// queue's message type, a UUID in base62.
type Claim struct {
	ID      string
	Token   string
	Attempt int
	Message json.RawMessage
}

// Queue is one of the queues as the generated ORM claims it (D80).
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

// Party is a @party table (D157): the field that titles a row.
type Party struct {
	Title string `json:"title,omitempty"`
}

// Relation is a to-one relation of a table: the table it reaches, and its
// key's column field on the row as the definitions' columns name it.
type Relation struct {
	Target string `json:"target"`
	// Column is the field of the part's columns that holds the target's
	// key; Member the member a create takes the target by.
	Column string `json:"column"`
	Member string `json:"member"`
}

// TableDef is one of the database's tables as the orchestration reads it:
// its behaviors' uses by the member their operations sit under (status,
// lease, retries, budget, comments) to the use's id, its to-one
// relations, whether it is @versioned, and the part it is, if any.
type TableDef struct {
	Uses      map[string]string   `json:"uses,omitempty"`
	Relations map[string]Relation `json:"relations,omitempty"`
	Versioned bool                `json:"versioned,omitempty"`
	Part      string              `json:"part,omitempty"`
}

// Definitions are what the generated ORM is generated with: the
// orchestration's tables by part (conversations and messages, D157;
// definitions, runs, policies, approvals and controls, D158), the @party
// tables by type, every table by type, and the two queues' types.
type Definitions struct {
	Parts       map[string]notifications.Part `json:"parts"`
	Parties     map[string]Party              `json:"parties,omitempty"`
	Tables      map[string]TableDef           `json:"tables"`
	RunRequests string                        `json:"runRequests,omitempty"`
	Requests    string                        `json:"requests,omitempty"`
}

// MustParse reads the definitions the generated ORM holds as JSON; it
// panics on JSON no generator wrote.
func MustParse(definitions string) *Definitions {
	out := &Definitions{}
	if err := json.Unmarshal([]byte(definitions), out); err != nil {
		panic(fmt.Sprintf("orchestration: the definitions: %v", err))
	}
	if out.Parts == nil {
		out.Parts = map[string]notifications.Part{}
	}
	if out.Parties == nil {
		out.Parties = map[string]Party{}
	}
	if out.Tables == nil {
		out.Tables = map[string]TableDef{}
	}
	return out
}

// PartOf is the table of part, false for none.
func (d *Definitions) PartOf(part string) (notifications.Part, bool) {
	p, ok := d.Parts[part]
	return p, ok
}

// UseOf is the id of typeName's use whose operations sit under member,
// false for a table with none.
func (d *Definitions) UseOf(typeName, member string) (string, bool) {
	use, ok := d.Tables[typeName].Uses[member]
	return use, ok
}
