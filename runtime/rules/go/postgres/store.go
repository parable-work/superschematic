// Package postgres is the behaviors' adapter's store over a Postgres
// transaction in pgx (D103): what rules.Execute reads and writes through
// in the caller's transaction, a generated repository's pgx.Tx or a
// hand-written server's.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

// Querier is the transaction a store reads and writes through: a pgx.Tx,
// which every generated repository holds in a transaction. pgx.Tx and
// *pgx.Conn satisfy it; never pass a pool, whose statements each take a
// connection of their own.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Savepoint is the savepoint a store applies a change set in, named apart
// from the Go ORM's nested transactions' sp_<depth>.
const Savepoint = "rules_apply"

// LockStatement is the statement Lock runs: the transaction's advisory
// lock on the key's hash, which it holds until it ends.
const LockStatement = "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))"

// NewStore is a Store over tx: DialectPostgres, LocksHeld. Apply runs in a
// savepoint, rules_apply, which a moved row, a false check or a failed
// statement rolls back to and releases, leaving tx going on; it releases
// it on success. A check runs as SELECT (<text>) AS held. Lock runs
// SELECT pg_advisory_xact_lock(hashtextextended($1, 0)).
func NewStore(tx Querier) rules.Store {
	return &store{tx: tx}
}

type store struct {
	tx Querier
}

func (*store) Dialect() rules.Dialect { return rules.DialectPostgres }

func (*store) Locks() rules.LockMode { return rules.LocksHeld }

func (s *store) Query(ctx context.Context, text string, values ...any) ([][]any, error) {
	rows, err := s.tx.Query(ctx, text, values...)
	if err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	defer rows.Close()
	fields := rows.FieldDescriptions()
	var out [][]any
	for rows.Next() {
		row, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("rules: %w", err)
		}
		for i, value := range row {
			// A json or jsonb column's value as the JSON it holds, which pgx
			// decodes: a string it holds stays a string to the adapter.
			if value == nil || i >= len(fields) || (fields[i].DataTypeOID != pgtype.JSONOID && fields[i].DataTypeOID != pgtype.JSONBOID) {
				continue
			}
			text, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("rules: encode column %s's JSON: %w", fields[i].Name, err)
			}
			row[i] = json.RawMessage(text)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	return out, nil
}

func (s *store) Apply(ctx context.Context, statements []rules.Statement) (bool, error) {
	if len(statements) == 0 {
		return true, nil
	}
	// A savepoint, so a moved row or a failed statement undoes the unit and
	// leaves the caller's transaction going on.
	if _, err := s.tx.Exec(ctx, "SAVEPOINT "+Savepoint); err != nil {
		return false, fmt.Errorf("rules: %w", err)
	}
	for _, statement := range statements {
		held, err := s.run(ctx, statement)
		if err != nil {
			s.undo(ctx)
			return false, fmt.Errorf("rules: %w", err)
		}
		if !held {
			s.undo(ctx)
			return false, nil
		}
	}
	if _, err := s.tx.Exec(ctx, "RELEASE SAVEPOINT "+Savepoint); err != nil {
		return false, fmt.Errorf("rules: %w", err)
	}
	return true, nil
}

// run runs one statement of a unit: whether its check held, or whether its
// write changed the rows it expects.
func (s *store) run(ctx context.Context, statement rules.Statement) (bool, error) {
	if statement.Check {
		rows, err := s.tx.Query(ctx, "SELECT ("+statement.Text+") AS held", statement.Values...)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		held := false
		if rows.Next() {
			var value *bool
			if err := rows.Scan(&value); err != nil {
				return false, err
			}
			held = value != nil && *value
		}
		rows.Close()
		return held, rows.Err()
	}
	tag, err := s.tx.Exec(ctx, statement.Text, statement.Values...)
	if err != nil {
		return false, err
	}
	return statement.Expect == rules.AnyRows || tag.RowsAffected() == statement.Expect, nil
}

// undo rolls a unit back to its savepoint and releases it. A rollback that
// fails (a broken connection) is let go: the error that undid the unit is
// the one returned, and the caller's transaction is lost either way.
func (s *store) undo(ctx context.Context) {
	if _, err := s.tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+Savepoint); err != nil {
		return
	}
	_, _ = s.tx.Exec(ctx, "RELEASE SAVEPOINT "+Savepoint)
}

func (s *store) Lock(ctx context.Context, key string) error {
	if _, err := s.tx.Exec(ctx, LockStatement, key); err != nil {
		return fmt.Errorf("rules: lock %s: %w", key, err)
	}
	return nil
}
