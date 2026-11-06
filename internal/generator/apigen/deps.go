package apigen

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/goutil"
	ir "github.com/parable-work/superschematic/ir"
)

// DepsInfo is what the generated Deps holds beside the config and the
// logger (docs/stack-model.md, section 8.5): the ORM of the API's
// database, a Go SDK client per API it calls, and a bucket per bucket it
// lists (D54).
type DepsInfo struct {
	// Database is the DB service whose ORM Deps.DB is: the API's authDb,
	// or its one DB-kind dependency. Empty when the API has neither.
	Database string

	// ORMModule is Database's Go ORM module.
	ORMModule string

	// FieldKeys is the field of the API's EnvConfig that holds the keys
	// of Database's Encrypted<T> fields, which the entrypoints open its ORM
	// with (D85): its Go name. Empty for a database without one.
	FieldKeys string

	// VersionGraph reports whether Database declares a version graph: its
	// ORM then imports the version-graph core's Go binding, which go.mod
	// replaces.
	VersionGraph bool

	// Rules reports whether a table of Database has a field that is a
	// behavior (D103): its ORM then requires the rules module, which
	// links the behaviors' core, and go.mod requires and replaces it,
	// since Go takes no replace from the ORM's go.mod.
	Rules bool

	// Calls are the API's calls entries, in order.
	Calls []DepsCall

	// DatabaseTypes is Database's Go types module, which declares the
	// messages of its queues, and DatabaseTypesAlias the name deps.go and
	// the scaffold import it as. Set for an API with workers (D53).
	DatabaseTypes      string
	DatabaseTypesAlias string

	// Buckets are the API's buckets entries, in order (D54).
	Buckets []DepsBucket

	// MemoryStores are the memory stores the API lists, its rateLimitStore
	// among them, sorted (D75).
	MemoryStores []DepsMemoryStore

	// RateLimitStore is the memory store the API's @rateLimit routes count
	// in, its config's rateLimitStore; empty when they count in process.
	RateLimitStore string
}

// DepsMemoryStore is a memory store in Deps: the HTTP runtime's
// provider-neutral memorystore.Store, which the entrypoint opens on Redis,
// or disabled (D75).
type DepsMemoryStore struct {
	// Service is the MemoryStore service.
	Service string

	// Field is the Deps field that holds the store.
	Field string

	// Definitions is the module of the store's Go definitions, which an
	// implementation imports to name a channel or a key, empty when its
	// outputs write none; Modules are the Go types modules they reach,
	// which a server's go.mod replaces.
	Definitions string
	Modules     []string
}

// DepsBucket is a bucket in Deps: the HTTP runtime's provider-neutral
// bucket.Bucket, which the entrypoint opens on the bucket's provider
// (D54).
type DepsBucket struct {
	// Service is the Bucket service.
	Service string

	// Field is the Deps field that holds the bucket.
	Field string
}

// DepsCall is a Go SDK client in Deps.
type DepsCall struct {
	// Service is the called API service.
	Service string

	// Field is the Deps field that holds the client.
	Field string

	// Module is the service's Go SDK module, imported as Alias.
	Module string
	Alias  string

	// Client is the SDK's root client type, which its New returns.
	Client string
}

// depsFields are the fields every Deps may hold beside its clients, its
// buckets and its memory stores.
var depsFields = []string{"Config", "DB", "Logger"}

// JobInfo is a job of the API (D52): a method of the generated Jobs
// interface.
type JobInfo struct {
	// Name is the job's `@job` class.
	Name string

	// Method is the Jobs method that runs it.
	Method string

	// Schedule is the decorator's schedule, for the method's comment;
	// empty for a job that runs only on demand.
	Schedule string
}

// jobsOf reads the API's jobs, sorted by name.
func jobsOf(schema *ir.Schema) []JobInfo {
	var jobs []JobInfo
	for _, job := range schema.Jobs {
		if job != nil {
			jobs = append(jobs, JobInfo{Name: job.Name, Method: goutil.GoPublicIdentifier(job.Name), Schedule: job.Schedule})
		}
	}
	slices.SortFunc(jobs, func(a, b JobInfo) int { return strings.Compare(a.Name, b.Name) })
	return jobs
}

// HasJobs reports whether the API declares a job, for which deps.go
// declares Jobs and JobsConstructor.
func (o *APIOutput) HasJobs() bool { return len(o.Jobs) > 0 }

// WorkerInfo is a worker of the API (D53): a method of the generated
// Workers interface, which handles one message of its queue; or a behavior
// worker (D103), whose handler behaviors.go holds.
type WorkerInfo struct {
	// Name is the worker's `@worker` class.
	Name string

	// Method is the Workers method that handles a message.
	Method string

	// Queue is the `@queue` class of the API's database whose messages the
	// worker handles: the message's type in the database's Go types.
	Queue string

	// Concurrency is the decorator's concurrency, for the method's
	// comment: how many messages an instance handles at a time unless an
	// environment changes it.
	Concurrency int

	// Behavior is set for a @behaviorWorker (D103): a worker whose handler
	// the generator writes, which the Workers interface has no method for.
	// Method is empty then.
	Behavior *BehaviorWorkerInfo
}

// BehaviorWorkerInfo is what a @behaviorWorker runs (D103): a table's
// behavior queues, each claimed as its messages fall due and run through
// the behaviors' core as the worker.
type BehaviorWorkerInfo struct {
	// Table is the table whose behaviors the worker runs: Ticket.
	Table string

	// Queues are the table's behavior queues, deadlines first: its deadline
	// queue (TicketDeadline), its change queue (TicketChange) and its event
	// queue (ProblemEvent, D104), each when the table has it.
	Queues []string

	// DeadlineQueue is the deadline queue, "" when its behaviors arm none;
	// ChangeQueue the change queue, "" when nothing hears its writes.
	DeadlineQueue string
	ChangeQueue   string

	// EventQueue is the event queue (ProblemEvent), "" when the table's
	// behaviors emit none; Events are the events it carries, each handed to
	// a method of the API's Workers (ir.Worker.Events, D104).
	EventQueue string
	Events     []ir.BehaviorWorkerEvent

	// Subject is the subject the worker's commands run as,
	// ir.BehaviorWorkerSubject(api, worker).
	Subject string

	// Permissions are the permissions the worker's commands hold.
	Permissions []string

	// Behaviors are those of the table's uses that arm deadlines (Timers),
	// which a generator's refusal of the worker names; empty when the
	// API's database is not among the generator's dependencies.
	Behaviors []string

	// Hearing are those of the table's uses that hear its writes (Timers,
	// Reactions), and HeardBy the tables whose behaviors hear them through
	// a relation (Project), from the change queue's marker; empty when
	// deps lacks the database.
	Hearing []string
	HeardBy []string
}

// BehaviorWorkerOf is what the @behaviorWorker w of the API schema runs
// (D103): its table's queues as the loader filled them (w.Queues, else
// w.Queue), each named a deadline, a change or an event queue by its table
// (behaviorlayout.DeadlineQueueTypeName, ChangeQueueTypeName,
// EventQueueTypeName, whose rule behaviorQueueName repeats, since the
// registry imports apigen), the events its event queue carries (w.Events,
// D104), the subject and permissions its commands run with, and from its
// database among deps the behaviors that arm, hear and listen. Nil for a
// worker that is no behavior worker. apigen, tsrestgen and rustrestgen
// build their worker info with it.
func BehaviorWorkerOf(schema *ir.Schema, deps map[string]*ir.Schema, w *ir.Worker) *BehaviorWorkerInfo {
	if w == nil || w.BehaviorWorker == nil {
		return nil
	}
	bw := w.BehaviorWorker
	info := &BehaviorWorkerInfo{
		Table:       bw.Type,
		Queues:      slices.Clone(w.Queues),
		Events:      slices.Clone(w.Events),
		Subject:     ir.BehaviorWorkerSubject(schema.Name, w.Name),
		Permissions: slices.Clone(bw.Permissions),
	}
	if len(info.Queues) == 0 && w.Queue != "" {
		info.Queues = []string{w.Queue}
	}
	for _, queue := range info.Queues {
		switch queue {
		case behaviorQueueName(bw.Type, ir.BehaviorQueueDeadlines):
			info.DeadlineQueue = queue
		case behaviorQueueName(bw.Type, ir.BehaviorQueueChanges):
			info.ChangeQueue = queue
		case behaviorQueueName(bw.Type, ir.BehaviorQueueEvents):
			info.EventQueue = queue
		}
	}
	info.Behaviors = ArmingBehaviors(schema, deps, bw.Type)
	if db, _, ok := schema.Database(); ok {
		if queue := deps[db].BehaviorQueueOf(bw.Type, ir.BehaviorQueueChanges); queue != nil {
			info.Hearing = slices.Clone(queue.BehaviorQueue.Behaviors)
			info.HeardBy = slices.Clone(queue.BehaviorQueue.HeardBy)
		}
	}
	return info
}

// behaviorQueueName is the name of the behavior queue of kind the loader
// adds for table (D103): behaviorlayout.DeadlineQueueTypeName's
// TicketDeadline, ChangeQueueTypeName's TicketChange, EventQueueTypeName's
// ProblemEvent (D104).
func behaviorQueueName(table, kind string) string {
	switch kind {
	case ir.BehaviorQueueChanges:
		return table + "Change"
	case ir.BehaviorQueueEvents:
		return table + "Event"
	}
	return table + "Deadline"
}

// Emits reports whether the worker's table's behaviors emit events (D104),
// each of which its event queue hands to a method of the API's Workers.
func (b *BehaviorWorkerInfo) Emits() bool { return len(b.Events) > 0 }

// Runs names what the worker runs for a generator's refusal: Deadlines()
// alone, "<Table>'s changes" alone, "<Table>'s deadlines and changes", or,
// with the events of a table whose behaviors emit them (D104), the parts
// it has joined as a list ("<Table>'s deadlines, changes and events").
func (b *BehaviorWorkerInfo) Runs() string {
	if b.EventQueue == "" {
		switch {
		case b.ChangeQueue == "":
			return b.Deadlines()
		case b.DeadlineQueue == "":
			return b.Table + "'s changes"
		}
		return b.Table + "'s deadlines and changes"
	}
	var parts []string
	if b.DeadlineQueue != "" {
		parts = append(parts, "deadlines")
	}
	if b.ChangeQueue != "" {
		parts = append(parts, "changes")
	}
	parts = append(parts, "events")
	return b.Table + "'s " + listed(parts)
}

// listed joins parts as a list: "a", "a and b", "a, b and c".
func listed(parts []string) string {
	if len(parts) < 2 {
		return strings.Join(parts, "")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// Deadlines names the deadlines the worker runs for a generator's
// refusal: the table's, and the behaviors that arm them when known
// ("Ticket's deadlines, which Timers arms").
func (b *BehaviorWorkerInfo) Deadlines() string {
	switch len(b.Behaviors) {
	case 0:
		return b.Table + "'s deadlines"
	case 1:
		return fmt.Sprintf("%s's deadlines, which %s arms", b.Table, b.Behaviors[0])
	}
	return fmt.Sprintf("%s's deadlines, which %s and %s arm", b.Table, strings.Join(b.Behaviors[:len(b.Behaviors)-1], ", "), b.Behaviors[len(b.Behaviors)-1])
}

// ArmingBehaviors are the behaviors of table's uses that arm the deadlines
// a behavior worker of the API schema runs, as the loader marked its
// database's deadline queue (ir.BehaviorQueue.Behaviors), or none when
// deps holds no such queue of the API's database.
func ArmingBehaviors(schema *ir.Schema, deps map[string]*ir.Schema, table string) []string {
	db, _, ok := schema.Database()
	if !ok {
		return nil
	}
	if queue := deps[db].BehaviorQueueOf(table, ir.BehaviorQueueDeadlines); queue != nil {
		return slices.Clone(queue.BehaviorQueue.Behaviors)
	}
	return nil
}

// workersOf reads the API's workers, sorted by name: a behavior worker
// with what it runs (BehaviorWorkerOf: its table, its queues as the loader
// filled them, the subject its commands run as and their permissions, the
// behaviors that arm and hear) and no method.
func workersOf(schema *ir.Schema, deps map[string]*ir.Schema) []WorkerInfo {
	var workers []WorkerInfo
	for _, worker := range schema.Workers {
		if worker == nil {
			continue
		}
		concurrency := worker.Concurrency
		if concurrency <= 0 {
			concurrency = ir.DefaultWorkerConcurrency
		}
		info := WorkerInfo{Name: worker.Name, Method: goutil.GoPublicIdentifier(worker.Name), Queue: worker.Queue, Concurrency: concurrency}
		if worker.BehaviorWorker != nil {
			info.Method = ""
			info.Behavior = BehaviorWorkerOf(schema, deps, worker)
		}
		workers = append(workers, info)
	}
	slices.SortFunc(workers, func(a, b WorkerInfo) int { return strings.Compare(a.Name, b.Name) })
	return workers
}

// QueuesFunc is the function of the API package that returns a behavior
// worker's queues (D103): TicketTimersQueues.
func (w WorkerInfo) QueuesFunc() string {
	return goutil.GoPublicIdentifier(w.Name) + "Queues"
}

// QueuesDoc is the doc comment of a behavior worker's QueuesFunc, the
// worker's queues over the tables of database, wrapped as gofmt keeps it.
func (w WorkerInfo) QueuesDoc(database string) string {
	if w.Behavior == nil {
		return ""
	}
	if w.Behavior.Emits() {
		var kinds []string
		if w.Behavior.DeadlineQueue != "" {
			kinds = append(kinds, "deadlines")
		}
		if w.Behavior.ChangeQueue != "" {
			kinds = append(kinds, "changes")
		}
		kinds = append(kinds, "events")
		return wrapComment("", fmt.Sprintf("%s are the queues of the behavior worker %s (D103, D104): %s's %s's %s, each claimed as it falls due and run through the behaviors' core as the worker, %s, in one transaction with its message's completion, and each event handed in that transaction to the method of workers its use and kind name (%s). Its main runs them with worker.RunAll; logger, nil for none, logs a message spent with nothing run: its claim lost, its row gone, or its fire vetoed and released.",
			w.QueuesFunc(), w.Name, database, w.Behavior.Table, listed(kinds), w.Behavior.Subject, w.EventFunc()))
	}
	kinds := "deadlines"
	switch {
	case w.Behavior.ChangeQueue != "" && w.Behavior.DeadlineQueue != "":
		kinds = "deadlines and changes"
	case w.Behavior.ChangeQueue != "":
		kinds = "changes"
	}
	text := fmt.Sprintf("%s are the queues of the behavior worker %s (D103): %s's %s's %s, each claimed as it falls due and run through the behaviors' core as the worker, %s, in one transaction with its message's completion. Its main runs them with worker.RunAll; logger, nil for none, logs a message spent with nothing run: its claim lost, its row gone, or its fire vetoed and released.",
		w.QueuesFunc(), w.Name, database, w.Behavior.Table, kinds, w.Behavior.Subject)
	return wrapComment("", text)
}

// wrapComment is text as a doc comment at indent, wrapped as gofmt keeps
// it, each line at most 78 columns of words past its indent.
func wrapComment(indent, text string) string {
	var lines []string
	line := indent + "//"
	for _, word := range strings.Fields(text) {
		if len(line)-len(indent)+1+len(word) > 78 && line != indent+"//" {
			lines = append(lines, line)
			line = indent + "//"
		}
		line += " " + word
	}
	return strings.Join(append(lines, line), "\n")
}

// EventFunc is the function behaviors.go holds that hands an event of a
// behavior worker's event queue to the Workers method of its use and kind
// (D104): problemWakeUpsEvent.
func (w WorkerInfo) EventFunc() string {
	return goutil.GoPrivateIdentifier(w.Name) + "Event"
}

// EventFuncDoc is the doc comment of a behavior worker's EventFunc,
// wrapped as gofmt keeps it.
func (w WorkerInfo) EventFuncDoc() string {
	if w.Behavior == nil {
		return ""
	}
	return wrapComment("", fmt.Sprintf("%s hands an event of %s's event queue to the Workers method of its use and kind (D104), its payload read as the method's type through encoding/json. An event no method takes fails its message, which is dead after its retries, for an operator.",
		w.EventFunc(), w.Behavior.Table))
}

// EventField is the Go field of a message of a behavior worker's event
// queue that holds the member of the field name (behaviorlayout's
// EventRowKeyField, ...), as typegen names a queue message's fields.
func (w WorkerInfo) EventField(name string) string {
	return codegen.ToPascalCase(name)
}

// WorkerEventInfo is a method of the API's Workers that handles an event a
// behavior worker's table's behaviors emit (D104): the worker, its table,
// its event queue and the subject it runs as, and the event as the loader
// names it (ir.BehaviorWorkerEvent): its use, member, behavior, kind,
// method and the API types of the event and its payload.
type WorkerEventInfo struct {
	Worker  string
	Table   string
	Queue   string
	Subject string
	ir.BehaviorWorkerEvent
}

// Doc is the doc comment of the event's Workers method, indented in the
// interface, wrapped as gofmt keeps it.
func (e WorkerEventInfo) Doc() string {
	return wrapComment("\t", fmt.Sprintf("%s handles an event %s of %s's %s, which the behavior worker %s claims from the queue %s: tx is the transaction its completion commits in, and ctx names the worker as the behavior caller (orm.WithBehaviorCaller), so the repositories' operations run as %s. The event is done when it returns nil; an error, or a panic, fails it, and the queue retries it after its backoff until its retries are spent. An event may come again after a worker dies holding it: a method is idempotent by the row's key and what the payload says makes it so.",
		e.Method, e.Kind, e.Table, e.Use, e.Worker, e.Queue, e.Subject))
}

// WorkerEvents are the events the API's behavior workers hand to methods
// of its Workers (D104), worker by worker in name order, each worker's in
// the order the loader listed them.
func (o *APIOutput) WorkerEvents() []WorkerEventInfo {
	var out []WorkerEventInfo
	for _, w := range o.BehaviorWorkers() {
		for _, event := range w.Behavior.Events {
			out = append(out, WorkerEventInfo{Worker: w.Name, Table: w.Behavior.Table, Queue: w.Behavior.EventQueue, Subject: w.Behavior.Subject, BehaviorWorkerEvent: event})
		}
	}
	return out
}

// WorkersDoc is the doc comment of Workers when a behavior worker of the
// API hands events to its methods (D104), wrapped as gofmt keeps it.
func (o *APIOutput) WorkersDoc() string {
	return wrapComment("", fmt.Sprintf("Workers are the workers of %s: a method per @worker class, each of which handles one message of its queue, and a method per event its behavior workers' tables' behaviors emit (D104), which the behavior worker hands it in the transaction the event's completion commits in. A worker's entrypoint calls its method once per message it claims, with a context that ends when the worker stops and its grace is over. The message is done when the method returns nil; an error, or a panic, fails it, and the queue retries it after its backoff until its retries are spent. A message may come again after a worker dies holding it, so a method is idempotent.", o.SchemaName))
}

// HasWorkerEvents reports whether a behavior worker of the API hands
// events to methods of its Workers (D104).
func (o *APIOutput) HasWorkerEvents() bool { return len(o.WorkerEvents()) > 0 }

// HasOrdinaryWorkers reports whether the API declares a @worker, each a
// method of Workers that handles a message of a queue of its database,
// whose Go types deps.go and the scaffold import.
func (o *APIOutput) HasOrdinaryWorkers() bool { return len(o.OrdinaryWorkers()) > 0 }

// OrdinaryWorkers are the API's @worker workers, each a method of the
// generated Workers interface; a behavior worker is none (D103).
func (o *APIOutput) OrdinaryWorkers() []WorkerInfo {
	var out []WorkerInfo
	for _, w := range o.Workers {
		if w.Behavior == nil {
			out = append(out, w)
		}
	}
	return out
}

// BehaviorWorkers are the API's @behaviorWorker workers (D103), whose
// handlers behaviors.go holds.
func (o *APIOutput) BehaviorWorkers() []WorkerInfo {
	var out []WorkerInfo
	for _, w := range o.Workers {
		if w.Behavior != nil {
			out = append(out, w)
		}
	}
	return out
}

// HasWorkers reports whether deps.go declares Workers and
// WorkersConstructor: the API declares a @worker, or a @behaviorWorker
// whose table's behaviors emit events, each handed to a method of Workers
// (D104). A behavior worker's deadline and change handlers are generated
// and are no methods of it (D103).
func (o *APIOutput) HasWorkers() bool {
	return o.HasOrdinaryWorkers() || o.HasWorkerEvents()
}

// HasBehaviorWorkers reports whether the API declares a @behaviorWorker
// (D103), whose queues behaviors.go writes.
func (o *APIOutput) HasBehaviorWorkers() bool { return len(o.BehaviorWorkers()) > 0 }

// HasBackgroundWork reports whether deps.go declares Jobs or Workers, which
// take a context.
func (o *APIOutput) HasBackgroundWork() bool { return o.HasJobs() || o.HasWorkers() }

// SetDeps sets what Deps holds, refusing a client or a bucket whose field
// would take the name of another of its fields.
func (o *APIOutput) SetDeps(deps DepsInfo) error {
	seen := map[string]string{}
	for _, name := range depsFields {
		seen[name] = "Deps." + name
	}
	for _, call := range deps.Calls {
		if prev, clash := seen[call.Field]; clash {
			return fmt.Errorf("apigen: %s calls %s, whose client would be Deps.%s, the field of %s", o.SchemaName, call.Service, call.Field, prev)
		}
		seen[call.Field] = call.Service
	}
	for _, b := range deps.Buckets {
		if prev, clash := seen[b.Field]; clash {
			return fmt.Errorf("apigen: %s lists bucket %s, which would be Deps.%s, the field of %s", o.SchemaName, b.Service, b.Field, prev)
		}
		seen[b.Field] = b.Service
	}
	for _, m := range deps.MemoryStores {
		if prev, clash := seen[m.Field]; clash {
			return fmt.Errorf("apigen: %s lists memory store %s, which would be Deps.%s, the field of %s", o.SchemaName, m.Service, m.Field, prev)
		}
		seen[m.Field] = m.Service
	}
	o.Deps = deps
	return nil
}

// DepsRequiresORM reports whether go.mod requires and replaces the ORM of
// the Deps database itself: a public API already does for its upstream
// auth schema's.
func (o *APIOutput) DepsRequiresORM() bool {
	return o.Deps.ORMModule != "" && (!o.IsPublic || o.Deps.ORMModule != o.ORMModule)
}

// HasEnvConfig reports whether config.go declares EnvConfig, which
// Deps.Config holds.
func (o *APIOutput) HasEnvConfig() bool {
	return o.EnvConfig != nil && o.EnvConfig.EnvConfig
}

// ImplementationFile is the file the implementation scaffold writes.
const ImplementationFile = "implementation.go"

// WriteImplementationScaffold writes the scaffold of the API's
// implementation into dir, the package the naming file's
// [implementation_paths] go template places it in, when the package is
// missing: when dir holds no .go file. It never writes into a package that
// exists, so it never overwrites the engineer's code, and reports whether
// it wrote. The scaffold's New has the generated Constructor's signature
// and each method returns a not-implemented error (docs/stack-model.md,
// section 8.5).
func WriteImplementationScaffold(output *APIOutput, dir string) (bool, error) {
	exists, err := ImplementationExists(dir)
	if err != nil || exists {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create implementation package %s: %w", dir, err)
	}
	// The implementation has no method for a user model operation, which
	// the identity runtime serves (D50).
	output, err = ImplementedOutput(output)
	if err != nil {
		return false, err
	}
	funcs, err := templateFuncs(output.Provider)
	if err != nil {
		return false, err
	}
	generator := codegen.NewFileGenerator(templatesFS, funcs)
	if err := generateFile(generator, templatesFS, "implementation.tmpl", filepath.Join(dir, ImplementationFile), output, funcs); err != nil {
		return false, fmt.Errorf("write implementation scaffold: %w", err)
	}
	return true, nil
}

// JobsFunc is the function an implementation of an API with jobs
// declares, of the generated JobsConstructor's signature.
const JobsFunc = "NewJobs"

// WorkersFunc is the function an implementation of an API with workers
// declares, of the generated WorkersConstructor's signature (D53).
const WorkersFunc = "NewWorkers"

// ManualRoutesFunc is the function an implementation of an API with a
// @manualRouteRegistration operation declares, of the generated
// ManualRoutesConstructor's signature: a stack's server builds the
// handlers RegisterRoutes mounts with it (D51).
const ManualRoutesFunc = "NewManualRoutes"

// DeclaresFunc reports whether the Go package at dir declares a function
// named name, in a file that is not a test.
func DeclaresFunc(dir, name string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("read implementation package %s: %w", dir, err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		file := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, filepath.Join(dir, file), nil, parser.SkipObjectResolution)
		if err != nil {
			return false, fmt.Errorf("read implementation package %s: %w", dir, err)
		}
		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
				return true, nil
			}
		}
	}
	return false, nil
}

// ImplementationExists reports whether dir holds a Go package: a .go file.
func ImplementationExists(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read implementation package %s: %w", dir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			return true, nil
		}
	}
	return false, nil
}
