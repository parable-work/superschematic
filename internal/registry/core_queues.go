package registry

import (
	"encoding/json"
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// queueDecorator returns @queue, from @superschematic/db and only in DB
// schemas: a message class of the schema that declares a queue of the
// database (D53). The class stays a type, whose fields are the message's,
// but no table of it: Apply makes it an embedded struct, which every
// language's types declare, and sqlgen writes the queue's table.
func queueDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "queue", Packages: []string{pkgDB}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindDB)},
		Apply: func(n Node, args []any, _ Site) error {
			def, err := queueOf(n.Type, args)
			if err != nil {
				return err
			}
			n.Type.Queue = def
			n.Type.Role = ir.RoleEmbeddedStruct
			return nil
		},
	}
}

// queueArgs are the arguments of @queue, all optional.
type queueArgs struct {
	Retries *any    `json:"retries"`
	Backoff *string `json:"backoff"`
	Lease   *string `json:"lease"`
}

// queueOf reads @queue({ retries, backoff, lease }) on td. Errors in the
// object are ArgErrors, so the frontend points at it.
func queueOf(td *ir.TypeDef, args []any) (*ir.QueueDef, error) {
	if len(td.Fields) == 0 {
		return nil, fmt.Errorf("@queue class %s has no fields; a queue's class holds its message's fields", td.Name)
	}
	def := &ir.QueueDef{}
	if len(args) > 1 {
		return nil, fmt.Errorf("@queue takes at most one options object")
	}
	if len(args) == 0 || args[0] == nil {
		return def, nil
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@queue takes an options object: { retries, backoff, lease }")
	}
	for key := range obj {
		switch key {
		case "retries", "backoff", "lease":
		default:
			return nil, ArgErrorf(0, "@queue has no option %q; it takes retries, backoff and lease", key)
		}
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, ArgErrorf(0, "@queue options: %v", err)
	}
	var a queueArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, ArgErrorf(0, "@queue options: backoff and lease are strings and retries a whole number: %v", err)
	}
	if a.Retries != nil {
		n, ok := (*a.Retries).(float64)
		if !ok || n != float64(int(n)) {
			return nil, ArgErrorf(0, "@queue retries is %v; it is a whole number", *a.Retries)
		}
		retries := int(n)
		def.Retries = &retries
	}
	if a.Backoff != nil {
		def.Backoff = *a.Backoff
	}
	if a.Lease != nil {
		def.Lease = *a.Lease
	}
	if err := CheckQueue(def); err != nil {
		return nil, ArgErrorf(0, "@queue %s: %v", td.Name, err)
	}
	return def, nil
}

// CheckQueue checks a queue's arguments in every form: retries that are
// not negative, and a backoff and a lease of whole seconds.
func CheckQueue(def *ir.QueueDef) error {
	if def.Retries != nil && *def.Retries < 0 {
		return fmt.Errorf("retries is %d; it is zero or more", *def.Retries)
	}
	if def.Backoff != "" {
		if _, err := TimeoutSeconds(def.Backoff); err != nil {
			return fmt.Errorf("backoff: %w", err)
		}
	}
	if def.Lease != "" {
		if _, err := TimeoutSeconds(def.Lease); err != nil {
			return fmt.Errorf("lease: %w", err)
		}
	}
	return nil
}

// QueueSettings are a queue's arguments with their defaults (D53): how
// many times a failed message is retried, and the backoff and the lease in
// seconds.
type QueueSettings struct {
	Retries        int
	BackoffSeconds int
	LeaseSeconds   int
}

// QueueSettingsOf reads a checked queue's arguments with their defaults.
func QueueSettingsOf(def *ir.QueueDef) QueueSettings {
	s := QueueSettings{
		Retries:        ir.DefaultQueueRetries,
		BackoffSeconds: ir.DefaultQueueBackoffSeconds,
		LeaseSeconds:   ir.DefaultQueueLeaseSeconds,
	}
	if def == nil {
		return s
	}
	if def.Retries != nil {
		s.Retries = *def.Retries
	}
	if def.Backoff != "" {
		s.BackoffSeconds, _ = TimeoutSeconds(def.Backoff)
	}
	if def.Lease != "" {
		s.LeaseSeconds, _ = TimeoutSeconds(def.Lease)
	}
	return s
}

// workerDecorator returns @worker, from @superschematic/api and only in API
// schemas: a class of the schema that declares a worker of the API, which
// handles the messages of a queue of the API's database (D53). The class
// holds no fields and is no type: Apply records the worker in
// Schema.Workers, and the TypeScript reader leaves the class out of Types.
// The queue is a class the schema imports, which the argument evaluator
// reads as a class reference and records as an import.
func workerDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "worker", Packages: []string{pkgAPI}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindAPI)},
		Apply: func(n Node, args []any, _ Site) error {
			worker, err := workerOf(n.Type, args)
			if err != nil {
				return err
			}
			if n.Schema.Worker(worker.Name) != nil {
				return fmt.Errorf("@worker class %s is declared twice", worker.Name)
			}
			n.Schema.Workers = append(n.Schema.Workers, worker)
			return nil
		},
	}
}

// workerOf reads @worker({ queue, concurrency, grace }) on td.
func workerOf(td *ir.TypeDef, args []any) (*ir.Worker, error) {
	if len(td.Fields) > 0 {
		return nil, fmt.Errorf("@worker class %s has fields; a worker's message is its queue's, so its class holds none", td.Name)
	}
	if td.Extends != "" || len(td.Implements) > 0 {
		return nil, fmt.Errorf("@worker class %s extends or implements another class; a worker's class declares the worker and nothing else", td.Name)
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("@worker takes one options object: { queue, concurrency, grace }")
	}
	obj, ok := args[0].(map[string]any)
	if !ok {
		return nil, ArgErrorf(0, "@worker takes an options object: { queue, concurrency, grace }")
	}
	worker := &ir.Worker{Name: td.Name, Comment: td.Comment}
	for key, value := range obj {
		switch key {
		case "queue":
			class, err := DecodeClassRef(value)
			if err != nil {
				return nil, ArgErrorf(0, "@worker queue is the @queue class whose messages the worker handles, imported from the API's database: %v", err)
			}
			worker.Queue = class
		case "concurrency":
			n, ok := value.(float64)
			if !ok || n != float64(int(n)) || n < 1 {
				return nil, ArgErrorf(0, "@worker concurrency is %v; it is a whole number, one or more", value)
			}
			worker.Concurrency = int(n)
		case "grace":
			grace, ok := value.(string)
			if !ok {
				return nil, ArgErrorf(0, "@worker grace is %v; it is a duration such as \"8s\"", value)
			}
			worker.Grace = grace
		default:
			return nil, ArgErrorf(0, "@worker has no option %q; it takes queue, concurrency and grace", key)
		}
	}
	if worker.Queue == "" {
		return nil, ArgErrorf(0, "@worker %s names no queue; write @worker({ queue: <the @queue class> })", td.Name)
	}
	if err := CheckWorker(worker); err != nil {
		return nil, ArgErrorf(0, "@worker %s: %v", worker.Name, err)
	}
	return worker, nil
}

// CheckWorker checks a worker's declaration in every form: a name and a
// queue, a concurrency of one or more when set, and a grace of whole
// seconds. Whether the queue is a queue of the API's database is checked
// where that database's schema is read: the API's build and the stack's
// resolution.
func CheckWorker(worker *ir.Worker) error {
	switch {
	case worker.Name == "":
		return fmt.Errorf("a worker has no name")
	case worker.Queue == "":
		return fmt.Errorf("names no queue")
	case worker.Concurrency < 0:
		return fmt.Errorf("concurrency is %d; it is one or more", worker.Concurrency)
	}
	if worker.Grace != "" {
		if _, err := TimeoutSeconds(worker.Grace); err != nil {
			return fmt.Errorf("grace: %w", err)
		}
	}
	return nil
}

// WorkerConcurrency is a checked worker's concurrency, its default when it
// sets none.
func WorkerConcurrency(worker *ir.Worker) int {
	if worker.Concurrency > 0 {
		return worker.Concurrency
	}
	return ir.DefaultWorkerConcurrency
}

// WorkerGraceSeconds is a checked worker's grace in seconds, its default
// when it sets none.
func WorkerGraceSeconds(worker *ir.Worker) int {
	if worker.Grace == "" {
		return ir.DefaultWorkerGraceSeconds
	}
	seconds, _ := TimeoutSeconds(worker.Grace)
	return seconds
}
