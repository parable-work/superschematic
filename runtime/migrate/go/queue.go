package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// A queue document is one operator's operation on a queue's dead messages
// (`superschematic-migrate queue`, runtime/migrate/README.md, "Queues"):
// list them, make them ready again, or delete them. `superschematic stack
// queue` writes it with the statements the compiler writes for the queue's
// table on Postgres (sqlutil.PostgresQueueOperatorStatements), so the
// runner runs the compiler's SQL as it runs a plan's, and writes none of
// its own. A queue's messages are claimed on Postgres alone (D53, amended),
// and so are these.

// QueueVersion is the version of the queue document and of the result the
// queue command prints.
const QueueVersion = 1

// QueueOperation is what a queue document does.
type QueueOperation string

const (
	// QueueDead lists the dead messages.
	QueueDead QueueOperation = "dead"
	// QueueRetry makes dead messages ready again, with no attempts.
	QueueRetry QueueOperation = "retry"
	// QueueDrop deletes dead messages.
	QueueDrop QueueOperation = "drop"
)

// QueueDocument is a queue document.
type QueueDocument struct {
	// Version is QueueVersion.
	Version int `json:"version"`

	// Service is the DB service that declares the queue, and Queue its
	// `@queue` class.
	Service string `json:"service"`
	Queue   string `json:"queue"`

	// Operation is what the document does.
	Operation QueueOperation `json:"operation"`

	// Limit is how many dead messages QueueDead lists at most.
	Limit int `json:"limit,omitempty"`

	// IDs are the dead messages QueueRetry and QueueDrop take, and All
	// takes every dead message in their place: a document has one or the
	// other.
	IDs []string `json:"ids,omitempty"`
	All bool     `json:"all,omitempty"`

	// Statements are the queue's statements.
	Statements QueueStatements `json:"statements"`
}

// QueueStatements are the statements of a queue document, as the compiler
// writes them (sqlutil.QueueOperatorStatements). Each that takes messages
// by ID takes them as $1, a text array.
type QueueStatements struct {
	// Dead lists up to $1 dead messages: per message its ID, attempts,
	// last error, when it died and the message as JSON, then the number
	// of dead messages in all.
	Dead string `json:"dead"`
	// States returns the ID and state of each message among $1.
	States string `json:"states"`
	// Retry and RetryAll make the dead messages among $1, or every dead
	// message, ready again, and return their IDs.
	Retry    string `json:"retry"`
	RetryAll string `json:"retryAll"`
	// Drop and DropAll delete the dead messages among $1, or every dead
	// message, and return their IDs.
	Drop    string `json:"drop"`
	DropAll string `json:"dropAll"`
}

// queueIDPattern is a message's ID as a queue's table returns it: a UUID
// in lower case.
var queueIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ReadQueueDocument decodes and checks a queue document: its version, its
// queue, its operation with what the operation takes and nothing else, and
// its statements.
func ReadQueueDocument(doc []byte) (*QueueDocument, error) {
	var q QueueDocument
	decoder := json.NewDecoder(bytes.NewReader(doc))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&q); err != nil {
		return nil, fmt.Errorf("queue document: %w", err)
	}
	if q.Version != QueueVersion {
		return nil, fmt.Errorf("queue document version %d; this runner reads version %d", q.Version, QueueVersion)
	}
	if q.Service == "" || q.Queue == "" {
		return nil, fmt.Errorf("queue document: it names no service or no queue")
	}
	switch q.Operation {
	case QueueDead:
		if q.Limit < 1 {
			return nil, fmt.Errorf("queue document: dead lists up to limit messages, one or more; got %d", q.Limit)
		}
		if len(q.IDs) > 0 || q.All {
			return nil, fmt.Errorf("queue document: dead takes no ids and no all")
		}
	case QueueRetry, QueueDrop:
		if q.Limit != 0 {
			return nil, fmt.Errorf("queue document: %s takes no limit", q.Operation)
		}
		if (len(q.IDs) > 0) == q.All {
			return nil, fmt.Errorf("queue document: %s takes either ids or all", q.Operation)
		}
		seen := map[string]bool{}
		for _, id := range q.IDs {
			if !queueIDPattern.MatchString(id) {
				return nil, fmt.Errorf("queue document: id %q is not a message's ID, a UUID in lower case", id)
			}
			if seen[id] {
				return nil, fmt.Errorf("queue document: id %s is listed twice", id)
			}
			seen[id] = true
		}
	default:
		return nil, fmt.Errorf("queue document: operation %q is not dead, retry or drop", q.Operation)
	}
	s := q.Statements
	for name, sql := range map[string]string{
		"dead": s.Dead, "states": s.States, "retry": s.Retry, "retryAll": s.RetryAll, "drop": s.Drop, "dropAll": s.DropAll,
	} {
		if strings.TrimSpace(sql) == "" {
			return nil, fmt.Errorf("queue document: statement %s is empty", name)
		}
	}
	return &q, nil
}

// QueueResult is what a queue document's operation did: the dead messages
// it listed, or the IDs it made ready again or deleted.
type QueueResult struct {
	Version   int            `json:"version"`
	Service   string         `json:"service"`
	Queue     string         `json:"queue"`
	Operation QueueOperation `json:"operation"`

	// Total is the number of dead messages, of which Messages are the
	// oldest deaths, up to the document's limit (QueueDead).
	Total    int64         `json:"total"`
	Messages []DeadMessage `json:"messages"`

	// IDs are the messages made ready again or deleted, in order
	// (QueueRetry, QueueDrop).
	IDs []string `json:"ids"`
}

// MarshalJSON writes the members of the result's operation alone: total
// and messages for QueueDead, ids for the others.
func (r QueueResult) MarshalJSON() ([]byte, error) {
	type head struct {
		Version   int            `json:"version"`
		Service   string         `json:"service"`
		Queue     string         `json:"queue"`
		Operation QueueOperation `json:"operation"`
	}
	h := head{r.Version, r.Service, r.Queue, r.Operation}
	if r.Operation == QueueDead {
		messages := r.Messages
		if messages == nil {
			messages = []DeadMessage{}
		}
		return json.Marshal(struct {
			head
			Total    int64         `json:"total"`
			Messages []DeadMessage `json:"messages"`
		}{h, r.Total, messages})
	}
	ids := r.IDs
	if ids == nil {
		ids = []string{}
	}
	return json.Marshal(struct {
		head
		IDs []string `json:"ids"`
	}{h, ids})
}

// DeadMessage is a dead message as QueueDead lists it.
type DeadMessage struct {
	ID       string `json:"id"`
	Attempts int64  `json:"attempts"`
	// LastError is the error its last try failed with; empty when it has
	// none.
	LastError string `json:"lastError"`
	// DiedAt is when it died, RFC 3339 in UTC to the second.
	DiedAt string `json:"diedAt"`
	// Message is the message: a JSON object of its columns.
	Message json.RawMessage `json:"message"`
}

// QueueRefusal is a retry or a drop of IDs some of which are no dead
// message, which changes none of them.
type QueueRefusal struct {
	Document *QueueDocument
	// Reasons say, per ID that is no dead message, what it is.
	Reasons []string
}

func (e *QueueRefusal) Error() string {
	verb := "retried"
	if e.Document.Operation == QueueDrop {
		verb = "dropped"
	}
	return fmt.Sprintf("queue %s of %s: %d of the %d message(s) named are not dead, so none was %s: %s",
		e.Document.Queue, e.Document.Service, len(e.Reasons), len(e.Document.IDs), verb, strings.Join(e.Reasons, "; "))
}

// RunQueue runs a queue document's operation on a Postgres database: the
// listing in a read-only transaction, a retry or a drop in one that changes
// every message it names or, when one of them is no dead message, none
// (QueueRefusal).
func RunQueue(ctx context.Context, driver Driver, doc *QueueDocument) (*QueueResult, error) {
	if driver.Dialect() != Postgres {
		return nil, fmt.Errorf("queue %s of %s: a queue's messages are claimed on Postgres alone, and the database is %s", doc.Queue, doc.Service, driver.Dialect())
	}
	result := &QueueResult{Version: QueueVersion, Service: doc.Service, Queue: doc.Queue, Operation: doc.Operation}
	if doc.Operation == QueueDead {
		err := driver.Transact(ctx, TxOptions{ReadOnly: true}, func(ctx context.Context, conn Conn) error {
			return conn.Query(ctx, doc.Statements.Dead, []any{int64(doc.Limit)}, func(scan func(dest ...any) error) error {
				var m DeadMessage
				var message string
				if err := scan(&m.ID, &m.Attempts, &m.LastError, &m.DiedAt, &message, &result.Total); err != nil {
					return err
				}
				var compact bytes.Buffer
				if err := json.Compact(&compact, []byte(message)); err != nil {
					return fmt.Errorf("message %s is no JSON: %w", m.ID, err)
				}
				m.Message = compact.Bytes()
				result.Messages = append(result.Messages, m)
				return nil
			})
		})
		if err != nil {
			return nil, fmt.Errorf("queue %s of %s: list the dead messages: %w", doc.Queue, doc.Service, err)
		}
		return result, nil
	}
	statement, args := doc.Statements.Retry, []any{doc.IDs}
	switch {
	case doc.Operation == QueueRetry && doc.All:
		statement, args = doc.Statements.RetryAll, nil
	case doc.Operation == QueueDrop && doc.All:
		statement, args = doc.Statements.DropAll, nil
	case doc.Operation == QueueDrop:
		statement = doc.Statements.Drop
	}
	err := driver.Transact(ctx, TxOptions{}, func(ctx context.Context, conn Conn) error {
		changed := map[string]bool{}
		if err := conn.Query(ctx, statement, args, func(scan func(dest ...any) error) error {
			var id string
			if err := scan(&id); err != nil {
				return err
			}
			changed[id] = true
			result.IDs = append(result.IDs, id)
			return nil
		}); err != nil {
			return err
		}
		if doc.All || len(changed) == len(doc.IDs) {
			return nil
		}
		states := map[string]string{}
		if err := conn.Query(ctx, doc.Statements.States, []any{doc.IDs}, func(scan func(dest ...any) error) error {
			var id, state string
			if err := scan(&id, &state); err != nil {
				return err
			}
			states[id] = state
			return nil
		}); err != nil {
			return err
		}
		refusal := &QueueRefusal{Document: doc}
		for _, id := range doc.IDs {
			switch state, ok := states[id]; {
			case changed[id]:
			case ok:
				refusal.Reasons = append(refusal.Reasons, fmt.Sprintf("%s is %s", id, state))
			default:
				refusal.Reasons = append(refusal.Reasons, id+" is not in the queue")
			}
		}
		return refusal
	})
	if err != nil {
		var refusal *QueueRefusal
		if errors.As(err, &refusal) {
			return nil, err
		}
		return nil, fmt.Errorf("queue %s of %s: %s the dead messages: %w", doc.Queue, doc.Service, doc.Operation, err)
	}
	sort.Strings(result.IDs)
	return result, nil
}
