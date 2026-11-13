package main

import (
	"context"
	"encoding/json"
	"fmt"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/d1"
	"github.com/parable-work/superschematic/runtime/migrate/go/sqlite"
)

// inspect prints what the catalog of the SQLite file --database-url names
// holds as JSON (migrate.Inspection). It opens the file to read it alone,
// so it creates no file, no state table and no row. `superschematic migrate
// plan --from-database` runs it and turns the result into a model (D116).
func inspect(ctx context.Context, args []string, o options) error {
	fs, urlFlag := flags("inspect", o)
	if err := parse(fs, args); err != nil {
		return err
	}
	url, err := databaseURL(*urlFlag, o)
	if err != nil {
		return err
	}
	if d1.IsURL(url) || migrate.URLDialect(url) != migrate.SQLite {
		return usageErrorf("inspect reads a SQLite file; %q names none", url)
	}
	driver, err := sqlite.OpenReadOnly(ctx, url, sqlite.Options{})
	if err != nil {
		return err
	}
	defer func() { _ = driver.Close(context.WithoutCancel(ctx)) }()
	inspection, err := o.runner(driver).Inspect(ctx)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(inspection, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(o.stdout, "%s\n", out)
	return err
}
