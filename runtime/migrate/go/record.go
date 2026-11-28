package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// A runtime record (D117) is what a runtime environment's servers read the
// tables of a publish through: the schema documents it published, the
// layout document the compiler wrote for them, each type's version, and
// the model its plan ends at. `apply --record` and a job database's record
// write it into RecordTable in the transaction that finishes the plan
// (Runner.Finish), so a server never reads the model moved before the
// layout it serves the new tables through. runtime/migrate/README.md,
// "Runtime records", is the contract with runtime mode's store
// (runtime/engine/rust/rules-runtime).

// RecordVersion is the version of the record document.
const RecordVersion = 1

// RecordTable is runtime mode's record table, one row per DB service, beside
// the state tables.
const RecordTable = "superschematic_runtime"

// Record is a record document.
type Record struct {
	// Version is RecordVersion.
	Version int `json:"version"`
	// Service is the DB service, the plan's.
	Service string `json:"service"`
	// ModelHash is the model the plan ends at, the plan's to.
	ModelHash string `json:"modelHash"`
	// Documents are the schema documents published, a JSON array.
	Documents json.RawMessage `json:"documents"`
	// Layout is the layout document, a JSON object.
	Layout json.RawMessage `json:"layout"`
	// Versions are each type's version by name, a JSON object.
	Versions json.RawMessage `json:"versions"`
}

// recordSQL creates the record table when it is missing, as runtime mode's
// store does.
var recordSQL = map[Dialect]string{
	Postgres: `CREATE TABLE IF NOT EXISTS ` + RecordTable + ` (
  service TEXT PRIMARY KEY, model_hash TEXT NOT NULL, documents TEXT NOT NULL,
  layout TEXT NOT NULL, versions TEXT NOT NULL,
  published_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	SQLite: `CREATE TABLE IF NOT EXISTS ` + RecordTable + ` (
  service TEXT PRIMARY KEY, model_hash TEXT NOT NULL, documents TEXT NOT NULL,
  layout TEXT NOT NULL, versions TEXT NOT NULL,
  published_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')))`,
}

// ReadRecord decodes and checks a record document: its version, its
// service and model, and that its documents are a list and its layout and
// versions objects.
func ReadRecord(doc []byte) (*Record, error) {
	var r Record
	decoder := json.NewDecoder(bytes.NewReader(doc))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return nil, fmt.Errorf("record document: %w", err)
	}
	if r.Version != RecordVersion {
		return nil, fmt.Errorf("record document version %d; this runner reads version %d", r.Version, RecordVersion)
	}
	if r.Service == "" || r.ModelHash == "" {
		return nil, fmt.Errorf("record document: want its service and its modelHash")
	}
	for _, member := range []struct {
		name  string
		value *json.RawMessage
		open  byte
	}{
		{"documents", &r.Documents, '['},
		{"layout", &r.Layout, '{'},
		{"versions", &r.Versions, '{'},
	} {
		var compact bytes.Buffer
		if err := json.Compact(&compact, *member.value); err != nil || compact.Len() == 0 || compact.Bytes()[0] != member.open {
			kind := "an object"
			if member.open == '[' {
				kind = "a list"
			}
			return nil, fmt.Errorf("record document: %s is not %s", member.name, kind)
		}
		*member.value = compact.Bytes()
	}
	return &r, nil
}

// CheckRecord refuses a record of another service than the plan's, or of
// another model than the one the plan ends at.
func CheckRecord(plan *Plan, r *Record) error {
	switch {
	case r.Service != plan.Service:
		return refusef("the record is of service %s and the plan migrates %s", r.Service, plan.Service)
	case r.ModelHash != plan.To:
		return refusef("the record is of model %s and the plan ends at model %s", r.ModelHash, plan.To)
	}
	return nil
}

// recordExistsSQL counts the record table, as the state table's exists
// query counts it.
var recordExistsSQL = map[Dialect]string{
	Postgres: `SELECT count(*) FROM pg_class WHERE oid = to_regclass('` + RecordTable + `')`,
	SQLite:   `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = '` + RecordTable + `'`,
}

// LoadRecord reads a runtime database's record in the caller's
// transaction (D117): its one row of RecordTable, checked as ReadRecord
// checks a record document. It is nil for a database no publish of
// runtime mode has reached, and refused for one that records more than
// one service. The table stays after a move to compiled mode, so a
// compiled database that was a runtime one still holds the record of its
// last publish.
func LoadRecord(ctx context.Context, conn Conn, dialect Dialect) (*Record, error) {
	exists, ok := recordExistsSQL[dialect]
	if !ok {
		return nil, fmt.Errorf("record: no record table for dialect %s", dialect)
	}
	var tables int64
	if err := conn.Query(ctx, exists, nil, func(scan func(dest ...any) error) error { return scan(&tables) }); err != nil {
		return nil, fmt.Errorf("look for the record table: %w", err)
	}
	if tables == 0 {
		return nil, nil
	}
	var records []*Record
	err := conn.Query(ctx, `SELECT service, model_hash, documents, layout, versions FROM `+RecordTable+` ORDER BY service`, nil,
		func(scan func(dest ...any) error) error {
			r := &Record{Version: RecordVersion}
			var documents, layout, versions string
			if err := scan(&r.Service, &r.ModelHash, &documents, &layout, &versions); err != nil {
				return err
			}
			r.Documents, r.Layout, r.Versions = json.RawMessage(documents), json.RawMessage(layout), json.RawMessage(versions)
			records = append(records, r)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("read the record: %w", err)
	}
	switch len(records) {
	case 0:
		return nil, nil
	case 1:
	default:
		return nil, fmt.Errorf("the database records %d services in %s; a runtime database holds one", len(records), RecordTable)
	}
	doc, err := json.Marshal(records[0])
	if err != nil {
		return nil, err
	}
	return ReadRecord(doc)
}

// RecordedModel is a runtime database's record and the model it is of,
// read together.
type RecordedModel struct {
	Record *Record
	// Model is the canonical JSON of the model the runner's state holds
	// for the record's service, the model the record's publish took the
	// database to.
	Model json.RawMessage
}

// LoadRecordedModel reads a runtime database's record and the model its
// service is at, in one read-only transaction: what a move to compiled
// mode exports (D117). It refuses a database with no record, one whose
// service has a plan in progress, and one whose model is not the record's.
func (r *Runner) LoadRecordedModel(ctx context.Context) (*RecordedModel, error) {
	dialect := r.Driver.Dialect()
	out := &RecordedModel{}
	err := r.Driver.Transact(ctx, TxOptions{ReadOnly: true}, func(ctx context.Context, conn Conn) error {
		record, err := LoadRecord(ctx, conn, dialect)
		if err != nil {
			return err
		}
		if record == nil {
			return refusef("the database holds no record of runtime mode (%s): no publish has reached it", RecordTable)
		}
		out.Record = record
		st, err := readState(ctx, conn, stateSQL[dialect], record.Service)
		if err != nil {
			return err
		}
		switch {
		case st.planHash != "":
			return refusef("service %s has plan %s in progress (%s); export it once the publish lands", record.Service, st.planHash, describePhase(st.planPhase))
		case st.modelHash != record.ModelHash || st.model == "":
			return refusef("the record is of model %s, and service %s is at model %q", record.ModelHash, record.Service, st.modelHash)
		}
		out.Model = json.RawMessage(st.model)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// WriteRecord writes a record in the caller's transaction, replacing the
// one before it, and creates the record table when it is missing.
func WriteRecord(ctx context.Context, conn Conn, dialect Dialect, r *Record) error {
	create, ok := recordSQL[dialect]
	if !ok {
		return fmt.Errorf("record: no record table for dialect %s", dialect)
	}
	if err := conn.Exec(ctx, create); err != nil {
		return fmt.Errorf("create the record table: %w", err)
	}
	if err := conn.Exec(ctx, `DELETE FROM `+RecordTable); err != nil {
		return fmt.Errorf("replace the record: %w", err)
	}
	if err := conn.Exec(ctx, `INSERT INTO `+RecordTable+` (service, model_hash, documents, layout, versions) VALUES ($1, $2, $3, $4, $5)`,
		r.Service, r.ModelHash, string(r.Documents), string(r.Layout), string(r.Versions)); err != nil {
		return fmt.Errorf("write the record of model %s: %w", r.ModelHash, err)
	}
	return nil
}
