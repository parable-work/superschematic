package rustrestgen

import (
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// A behavior's own events (D104): a table one of whose uses' behaviors
// emits events about its rows (superschematic_rules::Cx::emit) keeps an
// event queue, which the adapter writes each event to in the command's
// transaction. The table's @behaviorWorker runs it as its third queue,
// after its deadlines and its changes: src/behaviors.rs opens it as a queue
// its handler completes itself, and the handler spends each claimed
// message in a transaction of the pool (the adapter's spend_event) and
// hands the event to the API's Workers method of its use and kind, a
// method the project writes, with an EventRun that holds that transaction
// and the worker's caller, so the method's writes and the message's
// completion commit together. The method's error rolls both back and fails
// the message, which the queue retries after its backoff until it is dead.
// Workers, create_workers and the scaffold then exist for a behavior
// worker as they do for a @worker; a behavior worker of a table that emits
// none has no Workers method, and its crate stays as it was.

// EventHandler is a method of the API's Workers that handles an event a
// behavior worker's event queue carries (D104): ir.BehaviorWorkerEvent as
// the Rust crate names it.
type EventHandler struct {
	ir.BehaviorWorkerEvent

	// Worker is the @behaviorWorker class whose event queue carries the
	// event, Table its table, and Queue the queue (ProblemEvent).
	Worker string
	Table  string
	Queue  string

	// Fn is the Workers method, Method in snake case
	// (problem_wake_ups_schedules_run).
	Fn string
}

// EventHandlers are the methods of the API's Workers that handle its
// behavior workers' events (D104): each worker's, by its name, in the
// order of the events its event queue carries.
func (o *APIOutput) EventHandlers() []EventHandler {
	var handlers []EventHandler
	for _, w := range o.BehaviorWorkers {
		handlers = append(handlers, w.EventHandlers()...)
	}
	return handlers
}

// EmitsEvents reports whether a behavior worker of the API runs an event
// queue (D104): src/behaviors.rs then has EventRun and the event queues'
// functions and handlers.
func (o *APIOutput) EmitsEvents() bool {
	return o.hasQueueOfKind(ir.BehaviorQueueEvents)
}

// EventHandlers are the methods of the API's Workers that handle the
// events the worker's event queue carries (D104), in its order.
func (w WorkerInfo) EventHandlers() []EventHandler {
	if w.Behavior == nil || w.Behavior.EventQueue == "" {
		return nil
	}
	handlers := make([]EventHandler, len(w.Behavior.Events))
	for i, event := range w.Behavior.Events {
		handlers[i] = EventHandler{
			BehaviorWorkerEvent: event,
			Worker:              w.Name,
			Table:               w.Behavior.Table,
			Queue:               w.Behavior.EventQueue,
			Fn:                  rustutil.ToSnakeCase(event.Method),
		}
	}
	return handlers
}

// EventDispatchFunc is the function of src/behaviors.rs that hands an
// event of the worker's event queue to the Workers method of its use and
// kind (D104): problem_wake_ups_event.
func (w WorkerInfo) EventDispatchFunc() string {
	return rustutil.ToSnakeCase(w.Name) + "_event"
}

// EventHandlerSignature is the signature of the function that runs an
// event claimed from the worker's event queue (D104), through its opening
// brace: it takes the API's Workers beside the pool and the claim.
func (q BehaviorQueue) EventHandlerSignature() string {
	return rustFn("", "pub async fn "+q.Handler, []string{"pool: &Pool", "workers: &dyn crate::Workers", "claimed: Claimed<" + q.Queue + ">"}, "Result<(), BoxError>")
}

// SpendEventCall is the event handler's call of the adapter's spend_event
// for the worker's table, as rustfmt lays it out.
func (w WorkerInfo) SpendEventCall() string {
	args := []string{"&host", "&*tx", rustString(w.Behavior.Table), "Some(&claim)"}
	return rustCall("    ", "let spent = postgres::spend_event", args, ".await?;")
}

// EventHandlerDoc is the line of an event handler's method doc that says
// what it handles (D104): "Handles an event run of Problem's Schedules,
// which the behavior worker ProblemWakeUps claims from ProblemEvent,", its
// words wrapped at 80 columns under indent's "/// ".
func (h EventHandler) EventHandlerDoc(indent string) string {
	text := "Handles an event " + h.Kind + " of " + h.Table + "'s " + h.Use + ", which the behavior worker " + h.Worker + " claims from " + h.Queue + ", in `run`'s transaction, the one its completion commits in, as the worker's caller, whose `operation` runs a behavior operation there. The event is done when the method returns `Ok`; an error, or a panic, fails it, and the queue retries it after its backoff until its retries are spent. An event may come again after a worker dies holding it, so a method is idempotent, by the row's key and what the payload says makes it so."
	return docLines(indent+"///", text, 80)
}

// hasQueueOfKind reports whether a behavior worker of the API runs a
// table's queue of kind.
func (o *APIOutput) hasQueueOfKind(kind string) bool {
	for _, w := range o.BehaviorWorkers {
		for _, q := range w.BehaviorQueues {
			if q.Kind == kind {
				return true
			}
		}
	}
	return false
}
