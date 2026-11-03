package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
)

// Describe applies doc's DDL to a database of its own on the Postgres url
// names, prepares each of doc's statements there and returns what Postgres
// described of each, or the error it refused it with (D86). The database
// is created first, so the role url connects as may create one, and
// dropped last whatever happens; nothing else on the server changes.
func Describe(ctx context.Context, url string, doc *migrate.DescribeDocument) (*migrate.DescribeResult, error) {
	admin, err := connect(ctx, url, "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = admin.Close(context.WithoutCancel(ctx)) }()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	name := "superschematic_describe_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		return nil, fmt.Errorf("postgres: create the database the queries are described in: %w", err)
	}
	defer func() {
		_, _ = admin.Exec(context.WithoutCancel(ctx), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	}()
	conn, err := connect(ctx, url, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.PgConn().Exec(ctx, doc.DDL).ReadAll(); err != nil {
		return nil, fmt.Errorf("postgres: apply create.sql: %w", err)
	}
	out := &migrate.DescribeResult{Version: migrate.DescribeVersion}
	for _, st := range doc.Statements {
		description, err := describe(ctx, conn, st)
		if err != nil {
			return nil, err
		}
		out.Statements = append(out.Statements, description)
	}
	return out, nil
}

// connect opens a connection to url, or to its server's database named
// database when one is given.
func connect(ctx context.Context, url, database string) (*pgx.Conn, error) {
	config, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	if database != "" {
		config.Database = database
	}
	if _, ok := config.RuntimeParams["application_name"]; !ok {
		config.RuntimeParams["application_name"] = "superschematic-migrate"
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	return conn, nil
}

// describe prepares one statement, unnamed, and reads what Postgres says
// of its parameters and its result columns. A statement Postgres refuses
// is a description with its error, not a failure.
func describe(ctx context.Context, conn *pgx.Conn, st migrate.DescribeStatement) (migrate.StatementDescription, error) {
	out := migrate.StatementDescription{ID: st.ID}
	sd, err := conn.PgConn().Prepare(ctx, "", st.SQL, nil)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			out.Error = pgErr.Message
			if pgErr.Position > 0 {
				out.Error += fmt.Sprintf(" (at character %d)", pgErr.Position)
			}
			if pgErr.Hint != "" {
				out.Error += "; " + pgErr.Hint
			}
			return out, nil
		}
		return out, fmt.Errorf("postgres: prepare %s: %w", st.ID, err)
	}
	for _, oid := range sd.ParamOIDs {
		t, err := describedType(ctx, conn, oid, -1)
		if err != nil {
			return out, err
		}
		out.Params = append(out.Params, t)
	}
	for _, f := range sd.Fields {
		t, err := describedType(ctx, conn, f.DataTypeOID, f.TypeModifier)
		if err != nil {
			return out, err
		}
		column := migrate.DescribedColumn{Name: f.Name, DescribedType: t}
		if f.TableOID != 0 && f.TableAttributeNumber > 0 {
			err := conn.QueryRow(ctx,
				`SELECT c.relname, a.attname, a.attnotnull FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid WHERE a.attrelid = $1 AND a.attnum = $2`,
				f.TableOID, int16(f.TableAttributeNumber)).Scan(&column.Table, &column.Column, &column.NotNull)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return out, fmt.Errorf("postgres: describe %s column %s: %w", st.ID, f.Name, err)
			}
		}
		out.Columns = append(out.Columns, column)
	}
	return out, nil
}

// describedType names the type oid as Postgres does, with typmod's
// precision when it is not -1.
func describedType(ctx context.Context, conn *pgx.Conn, oid uint32, typmod int32) (migrate.DescribedType, error) {
	var t migrate.DescribedType
	var mod *int32
	if typmod >= 0 {
		mod = &typmod
	}
	err := conn.QueryRow(ctx,
		`SELECT format_type(t.oid, $2), t.typname, COALESCE(e.typname, '') FROM pg_type t LEFT JOIN pg_type e ON e.oid = t.typelem AND t.typcategory = 'A' WHERE t.oid = $1`,
		oid, mod).Scan(&t.Type, &t.Name, &t.Element)
	if err != nil {
		return t, fmt.Errorf("postgres: name the type %d: %w", oid, err)
	}
	return t, nil
}
