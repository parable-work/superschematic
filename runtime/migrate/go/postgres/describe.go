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
	cat := newCatalog(conn)
	for _, st := range doc.Statements {
		description, err := describe(ctx, cat, st)
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
func describe(ctx context.Context, cat *catalog, st migrate.DescribeStatement) (migrate.StatementDescription, error) {
	out := migrate.StatementDescription{ID: st.ID}
	sd, err := cat.conn.PgConn().Prepare(ctx, "", st.SQL, nil)
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
		t, err := cat.describedType(ctx, oid, -1)
		if err != nil {
			return out, err
		}
		out.Params = append(out.Params, t)
	}
	for _, f := range sd.Fields {
		t, err := cat.describedType(ctx, f.DataTypeOID, f.TypeModifier)
		if err != nil {
			return out, err
		}
		column := migrate.DescribedColumn{Name: f.Name, DescribedType: t}
		if f.TableOID != 0 && f.TableAttributeNumber > 0 {
			source, err := cat.attribute(ctx, f.TableOID, int16(f.TableAttributeNumber))
			if err != nil {
				return out, fmt.Errorf("postgres: describe %s column %s: %w", st.ID, f.Name, err)
			}
			column.Table, column.Column, column.NotNull = source.table, source.column, source.notNull
		}
		out.Columns = append(out.Columns, column)
	}
	return out, nil
}

// catalog reads the types and table columns a Describe's statements name,
// each once: the catalog does not change after the DDL, and the same
// types and columns recur across a document's statements.
type catalog struct {
	conn       *pgx.Conn
	types      map[typeKey]migrate.DescribedType
	attributes map[attributeKey]attribute
}

type typeKey struct {
	oid    uint32
	typmod int32
}

type attributeKey struct {
	table uint32
	num   int16
}

// attribute is a table column a result column reads, zero when the
// catalog has none.
type attribute struct {
	table, column string
	notNull       bool
}

func newCatalog(conn *pgx.Conn) *catalog {
	return &catalog{conn: conn, types: map[typeKey]migrate.DescribedType{}, attributes: map[attributeKey]attribute{}}
}

// attribute is the table and column of attribute num of table oid.
func (c *catalog) attribute(ctx context.Context, table uint32, num int16) (attribute, error) {
	key := attributeKey{table, num}
	if a, ok := c.attributes[key]; ok {
		return a, nil
	}
	var a attribute
	err := c.conn.QueryRow(ctx,
		`SELECT c.relname, a.attname, a.attnotnull FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid WHERE a.attrelid = $1 AND a.attnum = $2`,
		table, num).Scan(&a.table, &a.column, &a.notNull)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return attribute{}, err
	}
	if err != nil {
		a = attribute{}
	}
	c.attributes[key] = a
	return a, nil
}

// describedType names the type oid as Postgres does, with typmod's
// precision when it is not -1.
func (c *catalog) describedType(ctx context.Context, oid uint32, typmod int32) (migrate.DescribedType, error) {
	key := typeKey{oid, typmod}
	if t, ok := c.types[key]; ok {
		return t, nil
	}
	var t migrate.DescribedType
	var mod *int32
	if typmod >= 0 {
		mod = &typmod
	}
	err := c.conn.QueryRow(ctx,
		`SELECT format_type(t.oid, $2), t.typname, COALESCE(e.typname, '') FROM pg_type t LEFT JOIN pg_type e ON e.oid = t.typelem AND t.typcategory = 'A' WHERE t.oid = $1`,
		oid, mod).Scan(&t.Type, &t.Name, &t.Element)
	if err != nil {
		return t, fmt.Errorf("postgres: name the type %d: %w", oid, err)
	}
	c.types[key] = t
	return t, nil
}
