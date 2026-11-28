package generator

import (
	"fmt"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
)

// checkGoQueue refuses a Go worker of a sqlite-first database whose queue
// the ORM's SQLite queues leave out (#717): a Go worker claims on its
// database's SQLite file through New<Queue>SQLiteQueue, which the ORM
// writes for every queue whose message the SQLite queue clients hold. A
// queue's columns on SQLite are one layout in the three languages (D53,
// amended), so a queue the Rust types leave out on SQLite is one the Go
// ORM leaves out, for the same field.
func (r run) checkGoQueue(stackName string, w *ir.ResolvedDeployable, services []stack.Service) error {
	if w.Worker == nil {
		return nil
	}
	db := workerDatabase(w, services)
	if db == "" || r.Options.LoadDependency == nil {
		return nil
	}
	sqliteFirst := false
	for _, svc := range services {
		if svc.Name == db && len(svc.Dialects) > 0 && svc.Dialects[0] == registry.SQLDialectSQLite {
			sqliteFirst = true
		}
	}
	if !sqliteFirst {
		return nil
	}
	schema, err := r.LoadDependency(db)
	if err != nil {
		return fmt.Errorf("stack %s: worker %s claims a queue of %s: %w", stackName, w.Name, db, err)
	}
	queues, err := rustgen.RustQueues(schema, true)
	if err != nil {
		return fmt.Errorf("stack %s: worker %s claims a queue of %s: %w", stackName, w.Name, db, err)
	}
	claimed := append([]string{w.Worker.Queue}, w.Worker.Queues...)
	for _, q := range queues {
		if slices.Contains(claimed, q.Name) && q.SQLiteUnsupported != "" {
			return fmt.Errorf("stack %s: Go worker %s claims queue %s of %s, whose DB service lists sqlite first, and the SQLite queue client cannot hold its %s; hold the value in a @jsonField type (D53, amended; #717)", stackName, w.Name, q.Name, db, q.SQLiteUnsupported)
		}
	}
	return nil
}
