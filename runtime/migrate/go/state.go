package migrate

import (
	"context"
	"fmt"
	"time"
)

// The state tables (runtime/migrate/README.md, "State"). The names are fixed
// until a distribution needs its own.
const (
	stateTable = "superschematic_schema_state"
	logTable   = "superschematic_migrations"
)

// dialectSQL is the SQL of the state tables that differs between dialects.
type dialectSQL struct {
	// create creates the two tables when they are missing.
	create []string
	// exists counts the state tables: 1 when the state table exists.
	exists string
	// text renders a timestamp column as RFC 3339 UTC text.
	text func(column string) string
}

var stateSQL = map[Dialect]dialectSQL{
	Postgres: {
		create: []string{
			`CREATE TABLE IF NOT EXISTS ` + stateTable + ` (
  service     TEXT PRIMARY KEY,
  dialect     TEXT NOT NULL,
  model_hash  TEXT NOT NULL,
  model       TEXT,
  plan_hash   TEXT,
  plan_phase  TEXT,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
)`,
			`CREATE TABLE IF NOT EXISTS ` + logTable + ` (
  plan_hash   TEXT NOT NULL,
  step        INTEGER NOT NULL,
  service     TEXT NOT NULL,
  phase       TEXT NOT NULL,
  subject     TEXT NOT NULL,
  sql_hash    TEXT NOT NULL,
  started_at  TIMESTAMPTZ NOT NULL,
  finished_at TIMESTAMPTZ,
  PRIMARY KEY (plan_hash, step)
)`,
		},
		exists: `SELECT count(*) FROM pg_class WHERE oid = to_regclass('` + stateTable + `')`,
		text: func(column string) string {
			return `to_char(` + column + ` AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`
		},
	},
	SQLite: {
		create: []string{
			`CREATE TABLE IF NOT EXISTS ` + stateTable + ` (
  service     TEXT PRIMARY KEY,
  dialect     TEXT NOT NULL,
  model_hash  TEXT NOT NULL,
  model       TEXT,
  plan_hash   TEXT,
  plan_phase  TEXT,
  updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
)`,
			`CREATE TABLE IF NOT EXISTS ` + logTable + ` (
  plan_hash   TEXT NOT NULL,
  step        INTEGER NOT NULL,
  service     TEXT NOT NULL,
  phase       TEXT NOT NULL,
  subject     TEXT NOT NULL,
  sql_hash    TEXT NOT NULL,
  started_at  TEXT NOT NULL,
  finished_at TEXT,
  PRIMARY KEY (plan_hash, step)
)`,
		},
		exists: `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = '` + stateTable + `'`,
		text:   func(column string) string { return column },
	},
}

// now is the time a row records, as RFC 3339 UTC text with microseconds:
// Postgres reads it into TIMESTAMPTZ and SQLite stores it as written.
func now() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
}

// state is a service's row of the state table.
type state struct {
	// recorded is false when the service has no row.
	recorded  bool
	dialect   Dialect
	modelHash string
	model     string
	planHash  string
	planPhase string
	updatedAt string
}

func readState(ctx context.Context, conn Conn, sql dialectSQL, service string) (state, error) {
	st := state{}
	err := conn.Query(ctx, `SELECT dialect, model_hash, COALESCE(model, ''), COALESCE(plan_hash, ''), COALESCE(plan_phase, ''), `+sql.text("updated_at")+`
FROM `+stateTable+` WHERE service = $1`, []any{service}, func(scan func(dest ...any) error) error {
		var dialect string
		if err := scan(&dialect, &st.modelHash, &st.model, &st.planHash, &st.planPhase, &st.updatedAt); err != nil {
			return err
		}
		st.recorded = true
		st.dialect = Dialect(dialect)
		return nil
	})
	if err != nil {
		return state{}, fmt.Errorf("read the state of service %s: %w", service, err)
	}
	return st, nil
}

// startPlan records plan as in progress and deletes the log rows an earlier
// application of the same plan left: a plan is a pure function of its two
// models, so a database taken from A to B, back to A and to B again runs
// the same plan twice.
func startPlan(ctx context.Context, conn Conn, plan *Plan) error {
	if err := conn.Exec(ctx, `INSERT INTO `+stateTable+` (service, dialect, model_hash, plan_hash, updated_at)
VALUES ($1, $2, '', $3, $4)
ON CONFLICT (service) DO UPDATE SET plan_hash = excluded.plan_hash, plan_phase = NULL, updated_at = excluded.updated_at`,
		plan.Service, string(plan.Dialect), plan.Hash, now()); err != nil {
		return fmt.Errorf("record plan %s as in progress: %w", plan.Hash, err)
	}
	if err := conn.Exec(ctx, `DELETE FROM `+logTable+` WHERE plan_hash = $1`, plan.Hash); err != nil {
		return fmt.Errorf("clear the log of plan %s: %w", plan.Hash, err)
	}
	return nil
}

// setModel records model as the service's applied model with no plan in
// progress.
func setModel(ctx context.Context, conn Conn, model *Model) error {
	if err := conn.Exec(ctx, `INSERT INTO `+stateTable+` (service, dialect, model_hash, model, plan_hash, plan_phase, updated_at)
VALUES ($1, $2, $3, $4, NULL, NULL, $5)
ON CONFLICT (service) DO UPDATE SET dialect = excluded.dialect, model_hash = excluded.model_hash, model = excluded.model,
  plan_hash = NULL, plan_phase = NULL, updated_at = excluded.updated_at`,
		model.Service, string(model.Dialect), model.Hash, string(model.Canonical), now()); err != nil {
		return fmt.Errorf("record model %s for service %s: %w", model.Hash, model.Service, err)
	}
	return nil
}

// setExpandDone records that the plan in progress has finished its expand
// steps.
func setExpandDone(ctx context.Context, conn Conn, plan *Plan) error {
	if err := conn.Exec(ctx, `UPDATE `+stateTable+` SET plan_phase = 'expand', updated_at = $1 WHERE service = $2 AND plan_hash = $3`,
		now(), plan.Service, plan.Hash); err != nil {
		return fmt.Errorf("record the end of plan %s's expand phase: %w", plan.Hash, err)
	}
	return nil
}

// finishedSteps returns the indexes of the plan's steps whose log row has
// finished_at.
func finishedSteps(ctx context.Context, conn Conn, planHash string) (map[int]bool, error) {
	finished := map[int]bool{}
	err := conn.Query(ctx, `SELECT step FROM `+logTable+` WHERE plan_hash = $1 AND finished_at IS NOT NULL`, []any{planHash},
		func(scan func(dest ...any) error) error {
			var step int64
			if err := scan(&step); err != nil {
				return err
			}
			finished[int(step)] = true
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("read the log of plan %s: %w", planHash, err)
	}
	return finished, nil
}

// LoggedStep is one log row of a plan.
type LoggedStep struct {
	Step    int
	Phase   Phase
	Subject string
	SQLHash string
	// StartedAt and FinishedAt are RFC 3339 UTC times; FinishedAt is empty
	// while the step has not finished.
	StartedAt  string
	FinishedAt string
}

func readLog(ctx context.Context, conn Conn, sql dialectSQL, planHash string) ([]LoggedStep, error) {
	var steps []LoggedStep
	err := conn.Query(ctx, `SELECT step, phase, subject, sql_hash, `+sql.text("started_at")+`, COALESCE(`+sql.text("finished_at")+`, '')
FROM `+logTable+` WHERE plan_hash = $1 ORDER BY step`, []any{planHash}, func(scan func(dest ...any) error) error {
		var step int64
		var phase string
		var row LoggedStep
		if err := scan(&step, &phase, &row.Subject, &row.SQLHash, &row.StartedAt, &row.FinishedAt); err != nil {
			return err
		}
		row.Step = int(step)
		row.Phase = Phase(phase)
		steps = append(steps, row)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the log of plan %s: %w", planHash, err)
	}
	return steps, nil
}

// logStep reads one step's log row: whether it exists and whether it has
// finished_at.
func logStep(ctx context.Context, conn Conn, planHash string, index int) (exists, finished bool, err error) {
	err = conn.Query(ctx, `SELECT CASE WHEN finished_at IS NULL THEN 0 ELSE 1 END FROM `+logTable+` WHERE plan_hash = $1 AND step = $2`,
		[]any{planHash, int64(index)}, func(scan func(dest ...any) error) error {
			var done int64
			if err := scan(&done); err != nil {
				return err
			}
			exists, finished = true, done == 1
			return nil
		})
	if err != nil {
		return false, false, fmt.Errorf("read the log row of step %d: %w", index, err)
	}
	return exists, finished, nil
}

// insertLog writes a step's log row. finishedAt is empty for a step that is
// not transactional, which sets it when it finishes.
func insertLog(ctx context.Context, conn Conn, plan *Plan, step *Step, startedAt, finishedAt string) error {
	var err error
	if finishedAt == "" {
		err = conn.Exec(ctx, `INSERT INTO `+logTable+` (plan_hash, step, service, phase, subject, sql_hash, started_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			plan.Hash, int64(step.Index), plan.Service, string(step.Phase), step.Subject, sqlHash(step.Statements), startedAt)
	} else {
		err = conn.Exec(ctx, `INSERT INTO `+logTable+` (plan_hash, step, service, phase, subject, sql_hash, started_at, finished_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			plan.Hash, int64(step.Index), plan.Service, string(step.Phase), step.Subject, sqlHash(step.Statements), startedAt, finishedAt)
	}
	if err != nil {
		return fmt.Errorf("write the log row of step %d: %w", step.Index, err)
	}
	return nil
}

func finishLog(ctx context.Context, conn Conn, plan *Plan, step *Step) error {
	if err := conn.Exec(ctx, `UPDATE `+logTable+` SET finished_at = $1 WHERE plan_hash = $2 AND step = $3`,
		now(), plan.Hash, int64(step.Index)); err != nil {
		return fmt.Errorf("finish the log row of step %d: %w", step.Index, err)
	}
	return nil
}
