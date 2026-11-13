// Command guardmoves asks a database which moves of a @stateMachine's
// column it lets an update make (D117), and writes what it answered:
//
//	guardmoves postgres <admin-url> <create.sql> <moves.json> <answers.json>
//	guardmoves sqlite <database-file> <create.sql> <moves.json> <answers.json>
//
// On Postgres it creates a database of its own on the server the admin URL
// names, and drops it when it is done; on SQLite it creates the file. It
// runs create.sql there. Then, for every guarded column of every table
// moves.json lists and every pair of the column's states, it inserts a row
// in the first state, which no guard holds, and updates the column to the
// second; a null state is a null column. The answer is that the update
// passed, and the row holds the second state, or that the column's guard
// refused it, and the row holds the first. Anything else is an error in
// the answer, which is no answer.
//
// It judges nothing: TestStateMachineParity holds the answers to the
// machine, beside the answers of the four languages' helpers.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
)

// guardedTable is one table whose guards are asked: its key column, which
// each row is given, the other columns an insert sets with their literals,
// and the guarded columns.
type guardedTable struct {
	Table   string            `json:"table"`
	Key     string            `json:"key"`
	Insert  map[string]string `json:"insert"`
	Columns []column          `json:"columns"`
}

// column is one guarded column: the machine it is the column of, the
// states it takes, null among them when it is nullable, and the text that
// marks its guard's refusal: what the error's message starts with on
// Postgres, and what it holds on SQLite.
type column struct {
	Machine string    `json:"machine"`
	Column  string    `json:"column"`
	States  []*string `json:"states"`
	Refusal string    `json:"refusal"`
}

// answer is what the database did with one move.
type answer struct {
	Machine string  `json:"machine"`
	From    *string `json:"from"`
	To      *string `json:"to"`
	Allowed bool    `json:"allowed"`
	Error   string  `json:"error,omitempty"`
}

// database is one dialect's connection.
type database interface {
	// quote quotes an identifier, and param is the nth parameter's place.
	quote(name string) string
	param(n int) string
	script(ctx context.Context, statements string) error
	exec(ctx context.Context, statement string, args ...any) error
	// value reads one text column of one row.
	value(ctx context.Context, query string, args ...any) (*string, error)
	// refused reports whether err is the refusal of the guard that
	// refusal marks.
	refused(err error, refusal string) bool
	close() error
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "guardmoves:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) (err error) {
	if len(args) != 5 {
		return fmt.Errorf("usage: guardmoves postgres|sqlite <admin-url>|<database-file> <create.sql> <moves.json> <answers.json>")
	}
	dialect, where, createPath, movesPath, answersPath := args[0], args[1], args[2], args[3], args[4]

	create, err := os.ReadFile(createPath)
	if err != nil {
		return err
	}
	encoded, err := os.ReadFile(movesPath)
	if err != nil {
		return err
	}
	var asked []guardedTable
	if err := json.Unmarshal(encoded, &asked); err != nil {
		return fmt.Errorf("%s: %w", movesPath, err)
	}

	var db database
	switch dialect {
	case "postgres":
		db, err = openPostgres(ctx, where)
	case "sqlite":
		db, err = openSQLite(ctx, where)
	default:
		return fmt.Errorf("%q is no dialect; name postgres or sqlite", dialect)
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.close()) }()

	if err := db.script(ctx, string(create)); err != nil {
		return fmt.Errorf("apply %s: %w", createPath, err)
	}
	answers, err := ask(ctx, db, asked)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(answers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(answersPath, out, 0o644)
}

// shown is a state as an error writes it.
func shown(state *string) string {
	if state == nil {
		return "null"
	}
	return *state
}

// ask makes every move of every column of every table of tables, each on a
// row of its own, and returns what the database did with each.
func ask(ctx context.Context, db database, tables []guardedTable) ([]answer, error) {
	var answers []answer
	for _, asked := range tables {
		made, err := askTable(ctx, db, asked, len(answers))
		if err != nil {
			return nil, err
		}
		answers = append(answers, made...)
	}
	return answers, nil
}

// askTable makes every move of every column of asked. rows is the number
// of rows the run has inserted, which a row's key is written from.
func askTable(ctx context.Context, db database, asked guardedTable, rows int) ([]answer, error) {
	table, key := db.quote(asked.Table), db.quote(asked.Key)
	others := make([]string, 0, len(asked.Insert))
	for name := range asked.Insert {
		others = append(others, name)
	}
	sort.Strings(others)

	var answers []answer
	for _, col := range asked.Columns {
		column := db.quote(col.Column)
		names, values := []string{key, column}, []string{db.param(1), db.param(2)}
		for _, name := range others {
			names, values = append(names, db.quote(name)), append(values, asked.Insert[name])
		}
		insert := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(names, ", "), strings.Join(values, ", "))
		// The update sets the guarded column alone: a guard hears an update
		// that names its column.
		update := fmt.Sprintf("UPDATE %s SET %s = %s WHERE %s = %s", table, column, db.param(1), key, db.param(2))
		query := fmt.Sprintf("SELECT %s FROM %s WHERE %s = %s", column, table, key, db.param(1))

		for _, from := range col.States {
			for _, to := range col.States {
				id := fmt.Sprintf("00000000-0000-4000-8000-%012d", rows+len(answers)+1)
				if err := db.exec(ctx, insert, id, from); err != nil {
					return nil, fmt.Errorf("insert a row of %s whose %s is %s: %w", asked.Table, col.Column, shown(from), err)
				}
				a := answer{Machine: col.Machine, From: from, To: to}
				holds := to
				switch err := db.exec(ctx, update, to, id); {
				case err == nil:
					a.Allowed = true
				case db.refused(err, col.Refusal):
					holds = from
				default:
					a.Error = "the update failed, and not by the column's guard: " + err.Error()
				}
				if a.Error == "" {
					got, err := db.value(ctx, query, id)
					if err != nil {
						return nil, fmt.Errorf("read the row of %s from %s to %s: %w", col.Machine, shown(from), shown(to), err)
					}
					if (got == nil) != (holds == nil) || got != nil && *got != *holds {
						a.Error = fmt.Sprintf("allowed is %v, and the row holds %s after the update", a.Allowed, shown(got))
					}
				}
				answers = append(answers, a)
			}
		}
	}
	return answers, nil
}

// postgres is a database created for one run on the server admin is
// connected to.
type postgres struct {
	admin, conn *pgx.Conn
	name        string
}

func openPostgres(ctx context.Context, adminURL string) (database, error) {
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	db := &postgres{admin: admin, name: fmt.Sprintf("parity_guardmoves_%d", time.Now().UnixNano())}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+db.name); err != nil {
		return nil, errors.Join(fmt.Errorf("create database: %w", err), admin.Close(ctx))
	}
	dbURL, err := url.Parse(adminURL)
	if err != nil {
		return nil, errors.Join(err, db.close())
	}
	dbURL.Path = "/" + db.name
	if db.conn, err = pgx.Connect(ctx, dbURL.String()); err != nil {
		return nil, errors.Join(fmt.Errorf("connect %s: %w", db.name, err), db.close())
	}
	return db, nil
}

func (p *postgres) quote(name string) string { return pgx.Identifier{name}.Sanitize() }

func (p *postgres) param(n int) string { return fmt.Sprintf("$%d", n) }

// script runs the statements in one simple-protocol call.
func (p *postgres) script(ctx context.Context, statements string) error {
	_, err := p.conn.PgConn().Exec(ctx, statements).ReadAll()
	return err
}

// exec runs one statement in a transaction of its own, so a refusal ends
// no other statement's.
func (p *postgres) exec(ctx context.Context, statement string, args ...any) error {
	_, err := p.conn.Exec(ctx, statement, args...)
	return err
}

func (p *postgres) value(ctx context.Context, query string, args ...any) (*string, error) {
	var got *string
	err := p.conn.QueryRow(ctx, query, args...).Scan(&got)
	return got, err
}

// checkViolation is the SQLSTATE a guard's function raises.
const checkViolation = "23514"

// refused: a guard raises check_violation with a message that starts with
// its table and column. A CHECK constraint's own violation has the code
// and another message.
func (p *postgres) refused(err error, refusal string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == checkViolation && strings.HasPrefix(pgErr.Message, refusal)
}

func (p *postgres) close() error {
	ctx := context.Background()
	var errs []error
	if p.conn != nil {
		errs = append(errs, p.conn.Close(ctx))
	}
	if _, err := p.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+p.name+" WITH (FORCE)"); err != nil {
		errs = append(errs, fmt.Errorf("drop database %s: %w", p.name, err))
	}
	return errors.Join(append(errs, p.admin.Close(ctx))...)
}

// sqliteFile is a new database file on one connection.
type sqliteFile struct {
	db   *sql.DB
	conn *sql.Conn
}

func openSQLite(ctx context.Context, path string) (database, error) {
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%s exists; name a file to create", path)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &sqliteFile{db: db, conn: conn}, nil
}

func (s *sqliteFile) quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (s *sqliteFile) param(int) string { return "?" }

func (s *sqliteFile) script(ctx context.Context, statements string) error {
	_, err := s.conn.ExecContext(ctx, statements)
	return err
}

func (s *sqliteFile) exec(ctx context.Context, statement string, args ...any) error {
	_, err := s.conn.ExecContext(ctx, statement, args...)
	return err
}

func (s *sqliteFile) value(ctx context.Context, query string, args ...any) (*string, error) {
	var got sql.NullString
	if err := s.conn.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
		return nil, err
	}
	if !got.Valid {
		return nil, nil
	}
	return &got.String, nil
}

// constraintTrigger is SQLITE_CONSTRAINT_TRIGGER, the error of a trigger's
// RAISE(ABORT).
const constraintTrigger = 1811

// refused: a guard's trigger fails the statement with its fixed message.
func (s *sqliteFile) refused(err error, refusal string) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == constraintTrigger && strings.Contains(err.Error(), refusal)
}

func (s *sqliteFile) close() error {
	return errors.Join(s.conn.Close(), s.db.Close())
}
