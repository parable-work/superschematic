package main

import (
	"context"
	"encoding/json"
	"fmt"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/d1"
	"github.com/parable-work/superschematic/runtime/migrate/go/sqlite"
)

// record prints a runtime database's record (D117), the document apply
// --record writes, or with --model the model the record is of, each read
// with the other in one read-only transaction, so the model is the
// record's. superschematic migrate export reads a runtime database
// through it, since the compiler holds no database driver.
func record(ctx context.Context, args []string, o options) error {
	fs, urlFlag := flags("record", o)
	printModel := fs.Bool("model", false, "print the model the record is of, as status --model prints it")
	if err := parse(fs, args); err != nil {
		return err
	}
	url, err := databaseURL(*urlFlag, o)
	if err != nil {
		return err
	}
	var driver migrate.Driver
	var closeDriver func()
	if !d1.IsURL(url) && migrate.URLDialect(url) == migrate.SQLite {
		// A file is read alone, and one that does not exist is refused
		// rather than created.
		readOnly, err := sqlite.OpenReadOnly(ctx, url, sqlite.Options{})
		if err != nil {
			return err
		}
		driver, closeDriver = readOnly, func() { _ = readOnly.Close(context.WithoutCancel(ctx)) }
	} else {
		if driver, closeDriver, err = openDriver(ctx, url, o); err != nil {
			return err
		}
	}
	defer closeDriver()
	recorded, err := o.runner(driver).LoadRecordedModel(ctx)
	if err != nil {
		return err
	}
	if *printModel {
		_, err := fmt.Fprintf(o.stdout, "%s\n", recorded.Model)
		return err
	}
	doc, err := json.Marshal(recorded.Record)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(o.stdout, "%s\n", doc)
	return err
}
