// Command queuedb makes, reads and drops the database a test's local
// environment holds its queue in, on the Postgres server an admin URL
// names:
//
//	queuedb create <admin-url> <database> <sql-file>
//	queuedb query <admin-url> <database> <sql>
//	queuedb drop <admin-url> <database>
//
// create drops the database if it exists, creates it and runs the file's
// statements in it; query prints each row of a query in it, its columns
// joined by "|", NULL as nothing; drop drops it.
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "queuedb:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) < 3 {
		return fmt.Errorf("usage: queuedb create|query|drop <admin-url> <database> [<sql-file>|<sql>]")
	}
	command, admin, database := args[0], args[1], args[2]
	if database == "" || strings.ContainsAny(database, `"; `) {
		return fmt.Errorf("database %q is no plain name", database)
	}
	switch command {
	case "create":
		if len(args) != 4 {
			return fmt.Errorf("create takes a SQL file")
		}
		sql, err := os.ReadFile(args[3])
		if err != nil {
			return err
		}
		if err := exec(ctx, admin, `DROP DATABASE IF EXISTS "`+database+`" WITH (FORCE)`, `CREATE DATABASE "`+database+`"`); err != nil {
			return err
		}
		dbURL, err := in(admin, database)
		if err != nil {
			return err
		}
		// With no arguments pgx sends the file over the simple protocol,
		// so its statements run as one script.
		return exec(ctx, dbURL, string(sql))
	case "query":
		if len(args) != 4 {
			return fmt.Errorf("query takes a SQL query")
		}
		dbURL, err := in(admin, database)
		if err != nil {
			return err
		}
		return query(ctx, dbURL, args[3])
	case "drop":
		return exec(ctx, admin, `DROP DATABASE IF EXISTS "`+database+`" WITH (FORCE)`)
	}
	return fmt.Errorf("unknown command %q", command)
}

// in returns the admin URL with database in place of its own.
func in(admin, database string) (string, error) {
	u, err := url.Parse(admin)
	if err != nil {
		return "", err
	}
	u.Path = "/" + database
	return u.String(), nil
}

func exec(ctx context.Context, dbURL string, statements ...string) error {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	return nil
}

func query(ctx context.Context, dbURL, sql string) error {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return err
		}
		cells := make([]string, len(values))
		for i, v := range values {
			if v != nil {
				cells[i] = fmt.Sprint(v)
			}
		}
		fmt.Println(strings.Join(cells, "|"))
	}
	return rows.Err()
}
