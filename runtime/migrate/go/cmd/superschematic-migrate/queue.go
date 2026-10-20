package main

import (
	"context"
	"encoding/json"
	"fmt"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

// queue runs a queue document (migrate.QueueDocument) on the database
// --database-url names, which must be a Postgres one, and prints what it
// did as JSON (migrate.QueueResult).
func queue(ctx context.Context, args []string, o options) error {
	fs, urlFlag := flags("queue", o)
	ref := fs.String("queue", "", "the queue `document`: a path, or a gs:// URL")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *ref == "" {
		return usageErrorf("queue needs --queue")
	}
	url, err := databaseURL(*urlFlag, o)
	if err != nil {
		return err
	}
	doc, err := o.read(ctx, *ref)
	if err != nil {
		return err
	}
	q, err := migrate.ReadQueueDocument(doc)
	if err != nil {
		return fmt.Errorf("%s: %w", *ref, err)
	}
	// A queue's messages are claimed on Postgres alone, so no other
	// database is opened for them.
	if dialect := migrate.URLDialect(url); dialect != migrate.Postgres {
		return fmt.Errorf("queue %s of %s: a queue's messages are claimed on Postgres alone, and the database URL selects %s", q.Queue, q.Service, dialect)
	}
	driver, closeDriver, err := openDriver(ctx, url, o)
	if err != nil {
		return err
	}
	defer closeDriver()
	result, err := migrate.RunQueue(ctx, driver, q)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(o.stdout, "%s\n", out)
	return err
}
