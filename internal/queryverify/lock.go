// Package queryverify checks a DB service's declared queries (@query, D86)
// against Postgres, and keeps what Postgres described of them in the
// service's lock file.
//
// Verification needs a Postgres: the migration runner, which holds the
// driver (D27, Apply), applies the service's create.sql to a database of
// its own and prepares each statement (`superschematic-migrate describe`),
// and Judge compares what it described with the types the schema
// declares. The lock records, per query, the hash of what was verified and
// what Postgres described; CheckLock tells, with no database, whether each
// query is the one the lock verified, which `check` and `build` require.
package queryverify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// LockFile is the file beside a DB service's schema config that holds what
// Postgres described of its queries.
const LockFile = "queries.lock"

// LockVersion is the version of the lock's format.
const LockVersion = 1

// Lock is a service's lock file: each query that verified, by name.
type Lock struct {
	Version int           `json:"version"`
	Queries []LockedQuery `json:"queries"`
}

// LockedQuery is a query as it verified: the hash of what was verified
// (Hash), and the types of its parameters and its columns as Postgres
// described them.
type LockedQuery struct {
	Name    string         `json:"name"`
	Hash    string         `json:"hash"`
	Params  []string       `json:"params"`
	Columns []LockedColumn `json:"columns"`
}

// LockedColumn is a result column as Postgres described it: its name, its
// type, and whether it comes straight from a NOT NULL column that no outer
// join may leave out.
type LockedColumn struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	NotNull bool   `json:"notNull,omitempty"`
}

// Hash is the hash of what verification checks of q: its statements as
// Postgres runs them, the SQL types of its parameters and of its row's
// fields, which fields may be null and which it vouches for, and the shape
// of each table of the schema its SQL names. Any change to one of them,
// such as a renamed column of a table it reads, is a query the lock has
// not verified.
func Hash(q sqlutil.QueryModel) string {
	type field struct {
		Name     string `json:"name"`
		Column   string `json:"column,omitempty"`
		Type     string `json:"type"`
		Nullable bool   `json:"nullable,omitempty"`
	}
	var in struct {
		Name       string   `json:"name"`
		Statements []string `json:"statements"`
		Args       []field  `json:"args"`
		Row        []field  `json:"row"`
		NotNull    []string `json:"notNull"`
		Outer      []string `json:"outer"`
		AllOuter   bool     `json:"allOuter"`
		Tables     []string `json:"tables"`
	}
	in.Name = q.Name
	for _, st := range q.Statements() {
		in.Statements = append(in.Statements, st.SQL)
	}
	for _, a := range q.Args {
		in.Args = append(in.Args, field{Name: a.Name, Type: a.SQLType, Nullable: a.Nullable})
	}
	for _, f := range q.RowFields {
		in.Row = append(in.Row, field{Name: f.Name, Column: f.Column, Type: f.SQLType, Nullable: f.Nullable})
	}
	for name := range q.NotNull {
		in.NotNull = append(in.NotNull, name)
	}
	sort.Strings(in.NotNull)
	in.Outer = append([]string(nil), q.OuterTables...)
	sort.Strings(in.Outer)
	in.AllOuter = q.AllOuter
	in.Tables = q.Tables
	data, _ := json.Marshal(in)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ReadLock reads the lock at path; a missing file is a nil lock.
func ReadLock(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var lock Lock
	if err := decoder.Decode(&lock); err != nil {
		return nil, fmt.Errorf("%s: not a queries lock: %w", path, err)
	}
	if lock.Version != LockVersion {
		return nil, fmt.Errorf("%s: lock version %d; this compiler reads version %d", path, lock.Version, LockVersion)
	}
	return &lock, nil
}

// WriteLock writes lock to path, its queries sorted by name, as indented
// JSON with a final newline.
func WriteLock(path string, lock *Lock) error {
	sort.Slice(lock.Queries, func(i, j int) bool { return lock.Queries[i].Name < lock.Queries[j].Name })
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// CheckLock reports, with no database, whether the lock beside the service
// at serviceDir verified each query schema declares as it is now, and
// names no other: an error names each query that is not, and the command
// that verifies it. A service without queries needs no lock.
func CheckLock(serviceDir string, schema *ir.Schema) error {
	models, err := sqlutil.QueriesOf(schema, nil)
	if err != nil {
		return err
	}
	path := filepath.Join(serviceDir, LockFile)
	lock, err := ReadLock(path)
	if err != nil {
		return err
	}
	if len(models) == 0 && lock == nil {
		return nil
	}
	locked := map[string]LockedQuery{}
	if lock != nil {
		for _, q := range lock.Queries {
			locked[q.Name] = q
		}
	}
	var stale []string
	declared := map[string]bool{}
	for _, q := range models {
		declared[q.Name] = true
		entry, ok := locked[q.Name]
		switch {
		case !ok:
			stale = append(stale, fmt.Sprintf("query %s has not been verified: %s holds no entry for it", q.Name, LockFile))
		case entry.Hash != Hash(q):
			stale = append(stale, fmt.Sprintf("query %s changed since it was verified: its SQL, its params' or row's types, or a table it names are not what %s verified", q.Name, LockFile))
		}
	}
	if lock != nil {
		for _, q := range lock.Queries {
			if !declared[q.Name] {
				stale = append(stale, fmt.Sprintf("%s names query %s, which the schema no longer declares", LockFile, q.Name))
			}
		}
	}
	if len(stale) == 0 {
		return nil
	}
	return fmt.Errorf("%s; run `superschematic queries verify %s`, which prepares each query on Postgres and writes %s",
		strings.Join(stale, "; "), serviceDir, path)
}

// NeedsVectors reports whether a table of schema has a vector column, whose
// create.sql needs pgvector's image (D84).
func NeedsVectors(schema *ir.Schema) bool {
	scalarTypes := sqlutil.ScalarSQLTypes(schema)
	for _, td := range sqlutil.TableTypes(schema) {
		for _, fd := range td.Fields {
			if sqlutil.IsVectorType(sqlutil.ColumnType(schema, fd, scalarTypes)) {
				return true
			}
		}
	}
	return false
}
