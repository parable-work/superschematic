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
