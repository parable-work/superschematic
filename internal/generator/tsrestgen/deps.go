package tsrestgen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/tsutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The implementation side of a TypeScript API (D51; docs/stack-model.md,
// sections 8.5 and 8.6): deps.ts declares Deps, what the implementation is
// built from, and the types of the implementation's create and
// authenticate, and of its createManualRoutes for an API with a
// @manualRouteRegistration operation, the twin of the Go API's deps.go;
// the scaffold writes the implementation's package once, at the naming
// file's [implementation_paths] typescript template.

const (
	// PGPeerRange is the pg the package peers on when Deps holds a pool:
	// the HTTP runtime's ./postgres entry's range.
	PGPeerRange = ">=8.11.0"
	// PGTypesVersion pins the @types/pg deps.ts type-checks against.
	PGTypesVersion = "8.23.1"
	// NodeTypesVersion pins the @types/node the generated package and the
	// scaffold type-check against.
	NodeTypesVersion = "22.19.9"
)

// DepsInfo is what Deps holds beside the config and the logger: the API's
// database, a pg Pool or a SqlDatabase by its DB service's first dialect,
// an SDK client per API it calls, and a Bucket per bucket it lists (D54).
type DepsInfo struct {
	// Database is the DB service Deps.db reaches: the API's authDb, or its
	// one DB-kind dependency. Empty when the API has neither.
	Database string

	// Dialect is the first dialect Database's outputs.sql.dialects lists,
	// which types Deps.db (D64; docs/stack-model.md, section 18.6):
	// postgres, or empty, a pg Pool; sqlite, the HTTP runtime's
	// SqlDatabase, D1's interface, which a D1 binding gives a Worker and
	// openSqlite gives a server under stack dev.
	Dialect string

	// Calls are the API's calls entries, in order.
	Calls []DepsCall

	// Buckets are the API's buckets entries, in order (D54).
	Buckets []DepsBucket

	// DatabaseTypesPackage is the TypeScript types package of Database
	// when the API declares a worker (D53): it declares the queues'
	// messages, which the Workers interface takes, and exports their
	// definitions from its ./queues entry, which a worker claims with and
	// an implementation enqueues with. Empty otherwise.
	DatabaseTypesPackage string
}

// DepsBucket is a bucket in Deps: the HTTP runtime's provider-neutral
// Bucket, which the entrypoint opens on the bucket's provider (D54).
type DepsBucket struct {
	// Service is the Bucket service.
	Service string

	// Field is the Deps member that holds the bucket.
	Field string
}

// DepsCall is an SDK client in Deps.
type DepsCall struct {
	// Service is the called API service.
	Service string

	// Field is the Deps member that holds the client.
	Field string

	// Package is the service's TypeScript SDK package, which exports
	// Client.
	Package string

	// Client is the SDK's root client class.
	Client string
}

// SQLiteDialect is outputs.sql.dialects' name of SQLite.
const SQLiteDialect = "sqlite"

// DepsPool reports whether Deps.db is a pg Pool: the API has a database
// whose DB service lists postgres first.
func (o *APIOutput) DepsPool() bool {
	return o.Deps.Database != "" && !o.DepsSQLite()
}

// DepsSQLite reports whether Deps.db is a SqlDatabase: the API has a
// database whose DB service lists sqlite first (D64).
func (o *APIOutput) DepsSQLite() bool {
	return o.Deps.Database != "" && o.Deps.Dialect == SQLiteDialect
}

// depsFields are the members every Deps may hold beside its clients and
// its buckets.
var depsFields = []string{"config", "db", "logger"}

// SetDeps sets what Deps holds, refusing a client or a bucket whose member
// would take the name of another of its members.
func (o *APIOutput) SetDeps(deps DepsInfo) error {
	seen := map[string]string{}
	for _, name := range depsFields {
		seen[name] = "Deps." + name
	}
	for _, call := range deps.Calls {
		if prev, clash := seen[call.Field]; clash {
			return fmt.Errorf("tsrestgen: %s calls %s, whose client would be Deps.%s, the member of %s", o.SchemaName, call.Service, call.Field, prev)
		}
		seen[call.Field] = call.Service
	}
	for _, b := range deps.Buckets {
		if prev, clash := seen[b.Field]; clash {
			return fmt.Errorf("tsrestgen: %s lists bucket %s, which would be Deps.%s, the member of %s", o.SchemaName, b.Service, b.Field, prev)
		}
		seen[b.Field] = b.Service
	}
	o.Deps = deps
	return nil
}

// ChecksEndUsers reports whether an operation requires an authenticated
// end user, which the router establishes with RouterOptions.authenticate:
// the implementation then exports authenticate, an AuthenticatorFactory.
// An API whose authDb has a User table has the identity service establish
// every end user (D50), so its implementation exports none, as a Go
// implementation of such an API writes no AuthMiddleware.
func (o *APIOutput) ChecksEndUsers() bool {
	if o.Identity != nil {
		return false
	}
	for _, ep := range o.Endpoints {
		if ep.RequiresAuth && !ep.PublicRoute {
			return true
		}
	}
	return false
}

// DepsPeerPackages lists the packages deps.ts imports beside the runtime:
// each callee's SDK, in order, then the types of the database whose queues
// the Workers interface's messages are, when the API declares a worker.
func (o *APIOutput) DepsPeerPackages() []string {
	var out []string
	for _, call := range o.Deps.Calls {
		out = append(out, call.Package)
	}
	if o.HasWorkers() && o.Deps.DatabaseTypesPackage != "" && !slices.Contains(o.PeerPackages, o.Deps.DatabaseTypesPackage) {
		out = append(out, o.Deps.DatabaseTypesPackage)
	}
	return out
}

// JobInfo is a job of the API (D52): a method of the generated Jobs
// interface, which a job's entrypoint calls once per run.
type JobInfo struct {
	// Name is the job's `@job` class, and Method the Jobs method that runs
	// it, the class in camel case.
	Name   string
	Method string

	// Schedule is the decorator's schedule, for the method's comment;
	// empty for a job that runs only on demand.
	Schedule string
}

// WorkerInfo is a worker of the API (D53): a method of the generated
// Workers interface, which handles one message of its queue.
type WorkerInfo struct {
	// Name is the worker's `@worker` class, and Method the Workers method
	// that handles a message, the class in camel case.
	Name   string
	Method string

	// Queue is the `@queue` class of the API's database whose messages the
	// worker handles: the message's type in the database's TypeScript
	// types.
	Queue string

	// Concurrency is the decorator's concurrency, for the method's
	// comment.
	Concurrency int
}

// jobsOf reads the API's jobs, sorted by name.
func jobsOf(schema *ir.Schema) []JobInfo {
	var jobs []JobInfo
	for _, job := range schema.Jobs {
		if job != nil {
			jobs = append(jobs, JobInfo{Name: job.Name, Method: tsutil.ToCamelCase(job.Name), Schedule: job.Schedule})
		}
	}
	slices.SortFunc(jobs, func(a, b JobInfo) int { return strings.Compare(a.Name, b.Name) })
	return jobs
}

// workersOf reads the API's workers, sorted by name.
func workersOf(schema *ir.Schema) []WorkerInfo {
	var workers []WorkerInfo
	for _, w := range schema.Workers {
		if w == nil {
			continue
		}
		concurrency := w.Concurrency
		if concurrency <= 0 {
			concurrency = ir.DefaultWorkerConcurrency
		}
		workers = append(workers, WorkerInfo{Name: w.Name, Method: tsutil.ToCamelCase(w.Name), Queue: w.Queue, Concurrency: concurrency})
	}
	slices.SortFunc(workers, func(a, b WorkerInfo) int { return strings.Compare(a.Name, b.Name) })
	return workers
}

// HasJobs reports whether the API declares a job, for which deps.ts
// declares Jobs and JobsConstructor.
func (o *APIOutput) HasJobs() bool { return len(o.Jobs) > 0 }

// HasWorkers reports whether the API declares a worker, for which deps.ts
// declares Workers and WorkersConstructor.
func (o *APIOutput) HasWorkers() bool { return len(o.Workers) > 0 }

// WorkerQueues are the queues the API's workers handle, sorted and each
// once: the message types deps.ts imports from the database's types.
func (o *APIOutput) WorkerQueues() []string {
	var out []string
	for _, w := range o.Workers {
		if !slices.Contains(out, w.Queue) {
			out = append(out, w.Queue)
		}
	}
	slices.Sort(out)
	return out
}

// The functions an implementation of an API with jobs or workers exports,
// of the generated JobsConstructor's and WorkersConstructor's types, and
// the one an implementation of an API with a @manualRouteRegistration
// operation exports, of ManualRoutesConstructor's, whose handlers a
// server's entrypoint hands buildRouter (D51).
const (
	JobsFunc         = "createJobs"
	WorkersFunc      = "createWorkers"
	ManualRoutesFunc = "createManualRoutes"
)

// exportPattern matches a declaration that exports a name, by the name's
// place in the match: `export const createJobs`, `export function
// createJobs`, or a name in an export list, `export { createJobs }`.
var exportPattern = regexp.MustCompile(`(?m)^\s*export\s+(?:declare\s+)?(?:async\s+)?(?:const|let|var|function\*?|class)\s+([A-Za-z_$][\w$]*)|export\s*(?:type\s+)?\{([^}]*)\}`)

// reexportPattern matches a re-export of a whole module.
var reexportPattern = regexp.MustCompile(`(?m)^\s*export\s*\*\s*from`)

// ExportsName reports whether the TypeScript package at dir exports name
// from a file that is not a test: a declaration that exports it, or an
// export list that names it. A file that re-exports a whole module
// (`export * from`) may export it too, so it counts as exporting it.
func ExportsName(dir, name string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("read implementation package %s: %w", dir, err)
	}
	for _, entry := range entries {
		file := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(file, ".ts") || strings.HasSuffix(file, ".d.ts") || strings.HasSuffix(file, ".test.ts") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			return false, fmt.Errorf("read implementation package %s: %w", dir, err)
		}
		text := string(data)
		if reexportPattern.MatchString(text) {
			return true, nil
		}
		for _, m := range exportPattern.FindAllStringSubmatch(text, -1) {
			if m[1] == name {
				return true, nil
			}
			for _, item := range strings.Split(m[2], ",") {
				fields := strings.Fields(item)
				// `createJobs`, or `jobs as createJobs`.
				if len(fields) > 0 && fields[len(fields)-1] == name {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// ImplementationFile is the file the scaffold writes the implementation
// in; the scaffold also writes package.json and tsconfig.json.
const ImplementationFile = "index.ts"

// WriteImplementationScaffold writes the scaffold of the API's
// TypeScript implementation into dir, the package the naming file's
// [implementation_paths] typescript template places it in, when the
// package is missing: when dir holds no .ts file. It never writes into a
// package that exists, so it never overwrites the engineer's code, and
// reports whether it wrote. The scaffold's create has the generated
// Constructor's type and each method throws the runtime's not-implemented
// problem, which answers 501.
func WriteImplementationScaffold(output *APIOutput, dir string) (bool, error) {
	exists, err := ImplementationExists(dir)
	if err != nil || exists {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("create implementation package %s: %w", dir, err)
	}
	for _, file := range []struct{ template, name string }{
		{"implementation-package.tmpl", "package.json"},
		{"implementation-tsconfig.tmpl", "tsconfig.json"},
		{"implementation.tmpl", ImplementationFile},
	} {
		path := filepath.Join(dir, file.name)
		if _, err := os.Stat(path); err == nil {
			// A package.json or tsconfig.json the engineer wrote stays.
			continue
		}
		if err := generateFile(file.template, path, output); err != nil {
			return false, fmt.Errorf("write implementation scaffold: %w", err)
		}
	}
	return true, nil
}

// ImplementationExists reports whether dir holds a TypeScript package: a
// .ts file.
func ImplementationExists(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read implementation package %s: %w", dir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".ts") {
			return true, nil
		}
	}
	return false, nil
}
