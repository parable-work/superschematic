package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/postgres"
)

// describe runs a describe document (migrate.DescribeDocument) on the
// Postgres --database-url names, in a database it creates there and drops
// when it is done, and prints what Postgres described of each statement as
// JSON (migrate.DescribeResult). `superschematic queries verify` writes the
// document and judges the result (D86).
func describe(ctx context.Context, args []string, o options) error {
	fs, urlFlag := flags("describe", o)
	path := fs.String("statements", "", "the describe `document` superschematic queries verify wrote")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *path == "" {
		return usageErrorf("describe needs --statements")
	}
	url, err := databaseURL(*urlFlag, o)
	if err != nil {
		return err
	}
	if migrate.URLDialect(url) != migrate.Postgres {
		return usageErrorf("describe prepares statements on Postgres; %q is no postgres:// URL", url)
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	doc, err := migrate.ReadDescribeDocument(data)
	if err != nil {
		return fmt.Errorf("%s: %w", *path, err)
	}
	result, err := postgres.Describe(ctx, url, doc)
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
