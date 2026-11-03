// Package queueops writes the document an operator's operation on a
// queue's dead messages runs as, and reads what the operation did
// (`superschematic stack queue`, docs/stack-model.md, section 8.8). The
// migration runner runs the document (`superschematic-migrate queue`,
// runtime/migrate/README.md, "Queues"), since it holds the database
// drivers, which stay out of the compiler (D27). The document carries the
// statements sqlutil writes for the queue's table, so the runner writes no
// SQL of its own. This package does not import the runner, whose tests
// read the documents of testdata/golden.
package queueops

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// Version is the version of the document and of the result.
const Version = 1

// Operation is what an operator does to a queue's dead messages.
type Operation string

const (
	// Dead lists them.
	Dead Operation = "dead"
	// Retry makes them ready again, due now, with no attempts.
	Retry Operation = "retry"
	// Drop deletes them.
	Drop Operation = "drop"
	// Backfill enqueues a message for each row of the tables whose
	// @enqueueOnChange names the queue that meets its conditions (D88).
	Backfill Operation = "backfill"
)

// DefaultLimit is how many dead messages a listing shows unless told.
const DefaultLimit = 100

// Request is one operation on a queue's dead messages.
type Request struct {
	// Service is the DB service that declares the queue, and Queue its
	// `@queue` class.
	Service string
	Queue   string

	Operation Operation

	// Limit is how many dead messages Dead lists at most, one or more.
	Limit int

	// IDs are the messages Retry and Drop take, each a message's ID
	// (NormalizeID), and All takes every dead message in their place.
	IDs []string
	All bool

	// Key is the quoted key column of a queue that keeps one ready message
	// per key (D80), whose retry leaves each key one ready message; empty
	// for every other queue.
	Key string

	// Table is the queue's table, unquoted, as sqlutil.QueueTableName
	// names it, @table's name among them (D85); empty takes the name of a
	// queue whose class has no @table.
	Table string

	// Backfills are the statements Backfill runs, BackfillStatements's,
	// one or more.
	Backfills []string
}

// document is the runner's queue document (migrate.QueueDocument).
type document struct {
	Version    int        `json:"version"`
	Service    string     `json:"service"`
	Queue      string     `json:"queue"`
	Operation  Operation  `json:"operation"`
	Limit      int        `json:"limit,omitempty"`
	IDs        []string   `json:"ids,omitempty"`
	All        bool       `json:"all,omitempty"`
	Statements statements `json:"statements"`
}

type statements struct {
	Dead     string   `json:"dead"`
	States   string   `json:"states"`
	Retry    string   `json:"retry"`
	RetryAll string   `json:"retryAll"`
	Drop     string   `json:"drop"`
	DropAll  string   `json:"dropAll"`
	Backfill []string `json:"backfill,omitempty"`
}

// Document returns the queue document of req, indented, with the
// statements of the queue's table on Postgres.
func Document(req Request) ([]byte, error) {
	if req.Service == "" || req.Queue == "" {
		return nil, fmt.Errorf("a queue operation names its DB service and its queue")
	}
	switch req.Operation {
	case Dead:
		if req.Limit < 1 {
			return nil, fmt.Errorf("the dead messages of %s are listed up to a limit of one or more; got %d", req.Queue, req.Limit)
		}
		if len(req.IDs) > 0 || req.All {
			return nil, fmt.Errorf("a listing of dead messages takes no IDs")
		}
	case Retry, Drop:
		if (len(req.IDs) > 0) == req.All {
			return nil, fmt.Errorf("%s takes the IDs of dead messages, or all of them", req.Operation)
		}
		for _, id := range req.IDs {
			if normal, err := NormalizeID(id); err != nil || normal != id {
				return nil, fmt.Errorf("%q is not a message's ID in lower case", id)
			}
		}
	case Backfill:
		if req.Limit != 0 || len(req.IDs) > 0 || req.All {
			return nil, fmt.Errorf("a backfill takes no limit, no IDs and no all")
		}
		if len(req.Backfills) == 0 {
			return nil, fmt.Errorf("no @enqueueOnChange of %s enqueues %s, so there is nothing to backfill it from", req.Service, req.Queue)
		}
	default:
		return nil, fmt.Errorf("operation %q is not dead, retry, drop or backfill", req.Operation)
	}
	if req.Operation != Backfill && len(req.Backfills) > 0 {
		return nil, fmt.Errorf("%s takes no backfill statements", req.Operation)
	}
	table := req.Table
	if table == "" {
		table = sqlgen.QueueTableName(&ir.TypeDef{Name: req.Queue})
	}
	s := sqlutil.PostgresQueueOperatorStatements(sqlutil.QuoteIdentifier(table), req.Key)
	doc := document{
		Version:   Version,
		Service:   req.Service,
		Queue:     req.Queue,
		Operation: req.Operation,
		IDs:       req.IDs,
		All:       req.All,
		Statements: statements{
			Dead: s.Dead, States: s.States, Retry: s.Retry, RetryAll: s.RetryAll, Drop: s.Drop, DropAll: s.DropAll,
			Backfill: req.Backfills,
		},
	}
	if req.Operation == Dead {
		doc.Limit = req.Limit
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// idPattern is a UUID in either case.
var idPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// NormalizeID returns a message's ID in lower case, as its table returns
// it, or an error when id is no UUID.
func NormalizeID(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("%q is not a message's ID, a UUID such as 3f2c8a10-5b1e-4c7a-9d2e-8f6b4a1c0e93", id)
	}
	return strings.ToLower(id), nil
}

// Result is what an operation did, as the runner prints it
// (migrate.QueueResult).
type Result struct {
	Version   int       `json:"version"`
	Service   string    `json:"service"`
	Queue     string    `json:"queue"`
	Operation Operation `json:"operation"`

	// Total is the number of dead messages, and Messages the oldest
	// deaths among them, up to the limit (Dead).
	Total    int64         `json:"total"`
	Messages []DeadMessage `json:"messages"`

	// IDs are the messages made ready again or deleted, in order (Retry,
	// Drop).
	IDs []string `json:"ids"`
}

// DeadMessage is a dead message as a listing shows it.
type DeadMessage struct {
	ID        string          `json:"id"`
	Attempts  int64           `json:"attempts"`
	LastError string          `json:"lastError"`
	DiedAt    string          `json:"diedAt"`
	Message   json.RawMessage `json:"message"`
}

// ReadResult reads what the runner printed for req: a result of its
// version, queue and operation.
func ReadResult(req Request, data []byte) (*Result, error) {
	var r Result
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return nil, fmt.Errorf("the runner's result: %w", err)
	}
	switch {
	case r.Version != Version:
		return nil, fmt.Errorf("the runner's result is version %d; this compiler reads version %d", r.Version, Version)
	case r.Service != req.Service || r.Queue != req.Queue || r.Operation != req.Operation:
		return nil, fmt.Errorf("the runner's result is of %s on %s.%s, not of %s on %s.%s", r.Operation, r.Service, r.Queue, req.Operation, req.Service, req.Queue)
	}
	return &r, nil
}
