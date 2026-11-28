package ormgen

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// The notifier, the parties and the agent dispatcher on SQLite in the Go
// ORM (#618, as D189): for a database that lists sqlite, notifications_sqlite.go
// makes the notifier's host of notifications.go run over the SQLite
// database, its statements on the database's connection and its dispatch
// queue's enqueue and claims on SQLite (queues_sqlite.go), and
// orchestration_sqlite.go does the same for orchestration.go's, a
// behavior's operation run through the core in a SQLite repository's
// transaction. Each part's columns carry how a SQLite column holds them
// (notifierColumnOf), which the runtime reads a cell by.

// checkNotifierQueuesOnSQLite refuses a database that lists sqlite whose
// notifier or orchestration enqueues on a queue SQLite's clients cannot
// claim (Queue.SQLiteUnsupported), which the SQLite host could not drain.
func checkNotifierQueuesOnSQLite(output *ORMOutput) error {
	if !output.SQLite || output.Notifications == nil {
		return nil
	}
	names := []string{output.Notifications.Dispatch}
	if o := output.Orchestration; o != nil {
		names = append(names, o.RunRequests, o.Requests)
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		for _, q := range output.SQLiteLeftOut() {
			if q.Name == name {
				return fmt.Errorf("ormgen: the notifier's queue %s has no claims on SQLite (its %s), so the Go notifier cannot run on SQLite; list postgres alone in the DB service's outputs.sql.dialects", name, q.SQLiteUnsupported)
			}
		}
	}
	return nil
}

// writeNotifierSQLite writes notifications_sqlite.go and
// orchestration_sqlite.go for a database that lists sqlite and holds the
// notifier and the orchestration; one without has neither, so a file an
// earlier build wrote goes.
func writeNotifierSQLite(generator *codegen.FileGenerator, outputDir string, output *ORMOutput) error {
	for _, f := range []struct {
		name, template string
		written        bool
	}{
		{"notifications_sqlite.go", "notifications_sqlite.tmpl", output.SQLite && output.Notifications != nil},
		{"orchestration_sqlite.go", "orchestration_sqlite.tmpl", output.SQLite && output.Orchestration != nil},
	} {
		file := filepath.Join(outputDir, f.name)
		if f.written {
			if err := generateFile(generator, f.template, file, output); err != nil {
				return fmt.Errorf("failed to write %s: %w", f.name, err)
			}
			continue
		}
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove %s: %w", f.name, err)
		}
	}
	return nil
}
