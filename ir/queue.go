package ir

import "sort"

// QueueDef is a queue a DB service declares with `@queue` on a message
// class of its schema (docs/stack-model.md, section 8.8, D53). The class
// stays a type, the message: its fields are the message's, and the
// generated types declare it in every language. Its role is
// RoleEmbeddedStruct, so no generator makes a table of it as a table:
// sqlgen writes the queue's table instead, with a column per field and the
// columns a queue keeps (QueueColumns), and the DB's Go ORM enqueues and
// claims its messages.
//
// The decorator's arguments are all optional. Retries is a pointer, since
// zero retries is a choice the data forms must keep; the durations are
// empty unless set. The Queue* methods give each with its default.
type QueueDef struct {
	// Retries is how many times a message whose handler fails is handled
	// again before it is dead. Nil is DefaultQueueRetries.
	Retries *int `json:"retries,omitempty" yaml:"retries,omitempty"`

	// Backoff is how long a failed message waits before it is due again,
	// a duration of whole seconds as Go writes one (`30s`, `5m`). Empty is
	// DefaultQueueBackoffSeconds.
	Backoff string `json:"backoff,omitempty" yaml:"backoff,omitempty"`

	// Lease is how long a claim holds a message before it returns to
	// ready, unless the worker extends it, which it does while the
	// handler runs. Empty is DefaultQueueLeaseSeconds.
	Lease string `json:"lease,omitempty" yaml:"lease,omitempty"`
}

// The defaults of a queue and a worker that leave an argument out (D53).
const (
	// DefaultQueueRetries handles a message six times in all before it is
	// dead.
	DefaultQueueRetries = 5

	// DefaultQueueBackoffSeconds is how long a failed message waits before
	// it is due again.
	DefaultQueueBackoffSeconds = 30

	// DefaultQueueLeaseSeconds is how long a claim holds a message: a
	// worker that dies holding it gives it back within a minute.
	DefaultQueueLeaseSeconds = 60

	// DefaultWorkerConcurrency is how many messages a worker instance
	// handles at a time: one, so a handler need not be safe to run beside
	// itself unless its worker says so.
	DefaultWorkerConcurrency = 1

	// DefaultWorkerGraceSeconds is how long a worker told to stop lets the
	// handlers that run finish before it gives their messages back: eight
	// seconds, under the ten Cloud Run gives a container between SIGTERM
	// and SIGKILL.
	DefaultWorkerGraceSeconds = 8

	// DefaultWorkerInstances is how many instances of a worker an
	// environment runs unless its settings say otherwise.
	DefaultWorkerInstances = 1
)

// WorkerConcurrencyVariable is the environment variable a worker platform
// sets on each instance of a worker to the concurrency its environment
// gives it (ResolvedWorker.Concurrency), which the worker's entrypoint
// reads (runtime/http/go/worker.ConcurrencyVariable).
const WorkerConcurrencyVariable = "WORKER_CONCURRENCY"

// The states of a queue's message, in its table's state column.
const (
	// QueueStateReady is a message that is due at its due time.
	QueueStateReady = "ready"
	// QueueStateClaimed is a message a worker holds until the claim
	// expires.
	QueueStateClaimed = "claimed"
	// QueueStateDone is a message a handler handled.
	QueueStateDone = "done"
	// QueueStateDead is a message whose handler failed on every try,
	// which stays for an operator to read.
	QueueStateDead = "dead"
)

// The columns a queue's table keeps beside its message's, one per message
// field: the message's ID, its state, how many times a worker has claimed
// it, when it is next due, when the claim on it expires and the claim's
// token, the error of its last failed try, and when it was enqueued and
// last changed. No field of a message takes one of their names.
const (
	QueueColumnID             = "id"
	QueueColumnState          = "state"
	QueueColumnAttempts       = "attempts"
	QueueColumnDueAt          = "due_at"
	QueueColumnClaimExpiresAt = "claim_expires_at"
	QueueColumnClaimToken     = "claim_token"
	QueueColumnLastError      = "last_error"
	QueueColumnCreatedAt      = "created_at"
	QueueColumnUpdatedAt      = "updated_at"
)

// QueueColumns are the columns a queue's table keeps beside its message's,
// in the table's order: the ID first, the rest after the message's.
func QueueColumns() []string {
	return []string{
		QueueColumnID, QueueColumnState, QueueColumnAttempts, QueueColumnDueAt, QueueColumnClaimExpiresAt,
		QueueColumnClaimToken, QueueColumnLastError, QueueColumnCreatedAt, QueueColumnUpdatedAt,
	}
}

// QueueTableSuffix ends the name of a queue's table, which is its class's
// name in snake case, as sqlgen names a table, before it: OrderPlaced's is
// order_placed_queue.
const QueueTableSuffix = "_queue"

// Worker is a worker an API service declares with `@worker` on a class of
// its schema: a deployable that claims the messages of one queue and
// handles each with the API's Deps (docs/stack-model.md, section 8.8,
// D53). The class's name is the worker's, and the class holds nothing
// else, so a worker is no type: the loaders record it in Schema.Workers
// and in no other place, as they do a job (Job).
type Worker struct {
	// Name is the class's name: the method of the API's generated Workers
	// interface that handles a message.
	Name string `json:"name" yaml:"name"`

	// Comment stores the node-attached comment of the class declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Queue is the name of the `@queue` class whose messages the worker
	// handles. It is a class of the DB service the API connects to, its
	// authDb or its one DB dependency, which the schema imports.
	Queue string `json:"queue" yaml:"queue"`

	// Concurrency is how many messages an instance handles at a time.
	// Zero is DefaultWorkerConcurrency; an environment's settings change
	// it.
	Concurrency int `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`

	// Grace is how long a worker told to stop lets its running handlers
	// finish before it gives their messages back, a duration of whole
	// seconds. Empty is DefaultWorkerGraceSeconds.
	Grace string `json:"grace,omitempty" yaml:"grace,omitempty"`
}

// Worker returns the worker named name, or nil.
func (s *Schema) Worker(name string) *Worker {
	for _, worker := range s.Workers {
		if worker != nil && worker.Name == name {
			return worker
		}
	}
	return nil
}

// Queues returns the schema's `@queue` classes, sorted by name.
func (s *Schema) Queues() []*TypeDef {
	var out []*TypeDef
	for _, td := range s.Types {
		if td != nil && td.Queue != nil {
			out = append(out, td)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// WorkerDeployableName names the deployable of worker of the API service
// api as a job's is named (JobDeployableName): the API's name, a hyphen,
// and the worker's name in kebab case (`shop-orders-fulfil-orders`).
func WorkerDeployableName(api, worker string) string {
	return JobDeployableName(api, worker)
}
