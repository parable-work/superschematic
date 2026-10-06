package sqlite

import (
	"fmt"
	"strings"
)

// DefaultTableName names each table and index of the layout: graph_ and its
// local name.
func DefaultTableName(local string) string {
	return "graph_" + local
}

// tableNames are the local names of the layout's tables, in the order the
// layout creates them.
var tableNames = []string{
	"ref",
	"ref_history",
	"commit",
	"patch",
	"snapshot_entry",
	"release",
	"release_history",
	"member",
	"member_history",
}

// Tables returns the local names of the layout's tables, in the order the
// layout creates them.
func Tables() []string {
	return append([]string(nil), tableNames...)
}

// tables are the layout's tables, each quoted, under one name function.
type tables struct {
	ref, refHistory, commit, patch, snapshot string
	release, releaseHistory                  string
	member, memberHistory                    string
}

// quote quotes an identifier.
func quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// nameOf names one table or index, refusing a name function that gives no
// name.
func nameOf(tableName func(string) string, local string) (string, error) {
	name := tableName(local)
	if name == "" || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("sqlite: the table name function named %q %q", local, name)
	}
	return quote(name), nil
}

func tablesOf(tableName func(string) string) (tables, error) {
	var t tables
	for _, table := range []struct {
		dst   *string
		local string
	}{
		{&t.ref, "ref"}, {&t.refHistory, "ref_history"}, {&t.commit, "commit"}, {&t.patch, "patch"},
		{&t.snapshot, "snapshot_entry"}, {&t.release, "release"}, {&t.releaseHistory, "release_history"},
		{&t.member, "member"}, {&t.memberHistory, "member_history"},
	} {
		name, err := nameOf(tableName, table.local)
		if err != nil {
			return tables{}, err
		}
		*table.dst = name
	}
	return t, nil
}

// Layout returns the statements that create the layout, each table and
// index under the name tableName gives it (DefaultTableName when nil): one
// statement each, CREATE TABLE IF NOT EXISTS or CREATE [UNIQUE] INDEX IF
// NOT EXISTS, with no trigger and no transaction control, for a caller that
// runs its own migrations. Adapter.CreateTables runs them.
func Layout(tableName func(local string) string) ([]string, error) {
	if tableName == nil {
		tableName = DefaultTableName
	}
	t, err := tablesOf(tableName)
	if err != nil {
		return nil, err
	}
	var indexErr error
	index := func(local string) string {
		name, err := nameOf(tableName, local)
		if err != nil && indexErr == nil {
			indexErr = err
		}
		return name
	}
	history := func(table string) string {
		return `CREATE TABLE IF NOT EXISTS ` + table + ` (` +
			`history_id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, id TEXT NOT NULL, _version INTEGER NOT NULL, ` +
			`operation TEXT NOT NULL CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE')), data TEXT NOT NULL, recorded_at INTEGER NOT NULL` +
			`) STRICT`
	}
	statements := []string{
		// A ref's head and a commit's ref point at each other. A ref is
		// written before any commit of it, and its head moves to a commit
		// only once the commit is written, so both keys are checked at once,
		// not deferred.
		`CREATE TABLE IF NOT EXISTS ` + t.ref + ` (` +
			`id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, root_id TEXT NOT NULL, ` +
			`parent_ref_id TEXT REFERENCES ` + t.ref + ` (id), base_commit_id TEXT REFERENCES ` + t.commit + ` (id), ` +
			`head_commit_id TEXT REFERENCES ` + t.commit + ` (id), name TEXT NOT NULL, sealed_at INTEGER, ` +
			`created_at INTEGER NOT NULL, created_by TEXT NOT NULL, updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL, ` +
			`deleted_at INTEGER, deleted_by TEXT, _version INTEGER NOT NULL` +
			`) STRICT`,
		// A root's live refs have distinct names: a name already taken is
		// this index's SQLITE_CONSTRAINT_UNIQUE.
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + index("ref_live_name") + ` ON ` + t.ref + ` (graph, root_id, name) WHERE deleted_at IS NULL`,
		history(t.refHistory),
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + index("ref_history_version") + ` ON ` + t.refHistory + ` (id, _version)`,
		`CREATE TABLE IF NOT EXISTS ` + t.commit + ` (` +
			`id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, root_id TEXT NOT NULL, ` +
			`ref_id TEXT NOT NULL REFERENCES ` + t.ref + ` (id), parent_commit_id TEXT REFERENCES ` + t.commit + ` (id), ` +
			`message TEXT, schema_epoch INTEGER NOT NULL, content_hash TEXT NOT NULL, sequence INTEGER, ` +
			`created_at INTEGER NOT NULL, created_by TEXT NOT NULL` +
			`) STRICT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + index("commit_sequence") + ` ON ` + t.commit + ` (graph, root_id, sequence)`,
		`CREATE TABLE IF NOT EXISTS ` + t.patch + ` (` +
			`id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, commit_id TEXT NOT NULL REFERENCES ` + t.commit + ` (id), ` +
			`entity_kind TEXT NOT NULL, entity_key TEXT NOT NULL, entity_id TEXT NOT NULL, entity_version INTEGER NOT NULL, ` +
			`operation TEXT NOT NULL CHECK (operation IN ('ADD', 'UPDATE', 'DELETE'))` +
			`) STRICT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + index("patch_entity") + ` ON ` + t.patch + ` (commit_id, entity_kind, entity_key)`,
		`CREATE INDEX IF NOT EXISTS ` + index("patch_pin") + ` ON ` + t.patch + ` (entity_id, entity_version)`,
		`CREATE TABLE IF NOT EXISTS ` + t.snapshot + ` (` +
			`id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, commit_id TEXT NOT NULL REFERENCES ` + t.commit + ` (id), ` +
			`entity_kind TEXT NOT NULL, entity_key TEXT NOT NULL, entity_id TEXT NOT NULL, entity_version INTEGER NOT NULL` +
			`) STRICT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + index("snapshot_entry_entity") + ` ON ` + t.snapshot + ` (commit_id, entity_kind, entity_key)`,
		`CREATE INDEX IF NOT EXISTS ` + index("snapshot_entry_pin") + ` ON ` + t.snapshot + ` (entity_id, entity_version)`,
		`CREATE TABLE IF NOT EXISTS ` + t.release + ` (` +
			`id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, root_id TEXT NOT NULL, ` +
			`commit_id TEXT NOT NULL REFERENCES ` + t.commit + ` (id), created_at INTEGER NOT NULL, created_by TEXT NOT NULL, ` +
			`updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL, _version INTEGER NOT NULL` +
			`) STRICT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + index("release_root") + ` ON ` + t.release + ` (graph, root_id)`,
		history(t.releaseHistory),
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + index("release_history_version") + ` ON ` + t.releaseHistory + ` (id, _version)`,
		`CREATE TABLE IF NOT EXISTS ` + t.member + ` (` +
			`id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, kind TEXT NOT NULL, entity_key TEXT NOT NULL, ` +
			`ref_id TEXT NOT NULL REFERENCES ` + t.ref + ` (id), root_id TEXT NOT NULL, ` +
			`tombstone INTEGER NOT NULL CHECK (tombstone IN (0, 1)), _version INTEGER NOT NULL, data TEXT NOT NULL` +
			`) STRICT`,
		// Unique on the entity key of a kind on a ref, declared as (graph,
		// kind, ref_id, entity_key) so it also serves a read of a ref's rows
		// of a kind.
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + index("member_entity") + ` ON ` + t.member + ` (graph, kind, ref_id, entity_key)`,
		`CREATE TABLE IF NOT EXISTS ` + t.memberHistory + ` (` +
			`history_id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, kind TEXT NOT NULL, id TEXT NOT NULL, ` +
			`_version INTEGER NOT NULL, operation TEXT NOT NULL CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE')), ` +
			`data TEXT NOT NULL, recorded_at INTEGER NOT NULL` +
			`) STRICT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + index("member_history_version") + ` ON ` + t.memberHistory + ` (id, _version)`,
		`CREATE INDEX IF NOT EXISTS ` + index("member_history_recorded") + ` ON ` + t.memberHistory + ` (graph, kind, recorded_at)`,
	}
	if indexErr != nil {
		return nil, indexErr
	}
	return statements, nil
}
