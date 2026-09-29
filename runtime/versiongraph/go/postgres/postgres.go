// Package postgres is the Postgres storage adapter of the version-graph
// engine (D19). It builds its statements at run time from a graph's
// descriptor (version 2), which names the graph's tables, each kind's role
// columns and every column's value class, and it returns every row as a
// canonical row through package canonical.
//
// The graph's own tables have the columns the loader gives them (D17): a
// ref's root_id, parent_ref_id, base_commit_id, head_commit_id, name,
// sealed_at, audit and soft-delete columns and _version; a commit's root_id,
// ref_id, parent_commit_id, message, schema_epoch, content_hash, sequence,
// created_at and created_by; a patch's commit_id, entity_kind, entity_key,
// entity_id, entity_version and operation. A history table keys its images
// on the kind's id and version columns and holds each in data.
//
// The adapter reaches Postgres through Client; Pgx binds pgx.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/canonical"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// DefaultHistoryActorSetting is the transaction-local setting a versioned
// table's history trigger reads a hard delete's actor from, unless the
// schema's history_actor_setting naming key names another.
const DefaultHistoryActorSetting = "superschematic.history_actor_id"

// The audit columns the adapter writes on every row when a kind has them.
const (
	createdAtColumn = "created_at"
	createdByColumn = "created_by"
	updatedAtColumn = "updated_at"
	updatedByColumn = "updated_by"
)

// Options configure an Adapter.
type Options struct {
	// HistoryActorSetting is the setting the history triggers read a hard
	// delete's actor from: the schema's history_actor_setting naming key.
	// Empty is DefaultHistoryActorSetting.
	HistoryActorSetting string
}

// Adapter holds one graph's statements. It is safe for concurrent use; bind
// a Client with Storage.
type Adapter struct {
	actorSetting string
	sweepKey     string

	refTable, commitTable, patchTable string
	rootTable, rootKey                string

	kinds map[string]*kind
}

// kind is one member kind's tables and columns.
type kind struct {
	name         string
	table        string
	historyTable string
	key          string
	id           string
	ref          string
	root         string
	tombstone    string
	version      string
	columns      map[string]string
}

// descriptor is what the adapter reads of a graph descriptor.
type descriptor struct {
	Version int    `json:"version"`
	Graph   string `json:"graph"`
	Root    struct {
		Table string `json:"table"`
		Key   string `json:"key"`
	} `json:"root"`
	RefTable    string `json:"refTable"`
	CommitTable string `json:"commitTable"`
	PatchTable  string `json:"patchTable"`
	Kinds       []struct {
		Kind         string            `json:"kind"`
		Table        string            `json:"table"`
		HistoryTable string            `json:"historyTable"`
		Key          string            `json:"key"`
		ID           string            `json:"id"`
		Ref          string            `json:"ref"`
		Root         string            `json:"root"`
		Tombstone    string            `json:"tombstone"`
		Version      string            `json:"version"`
		Columns      map[string]string `json:"columns"`
	} `json:"kinds"`
}

// New reads a graph's descriptor. The core checks the rest of it; the
// adapter needs its tables, each kind's role columns, the root column
// among them, and each kind's columns.
func New(raw json.RawMessage, opts Options) (*Adapter, error) {
	var d descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("postgres: read the descriptor: %w", err)
	}
	if d.Version != 2 {
		return nil, fmt.Errorf("postgres: descriptor version %d; this adapter reads version 2", d.Version)
	}
	for member, value := range map[string]string{"root table": d.Root.Table, "root key": d.Root.Key, "refTable": d.RefTable, "commitTable": d.CommitTable, "patchTable": d.PatchTable} {
		if value == "" {
			return nil, fmt.Errorf("postgres: the descriptor's %s is empty", member)
		}
	}
	a := &Adapter{
		actorSetting: opts.HistoryActorSetting,
		sweepKey:     "superschematic.versiongraph.sweep:" + d.RefTable,
		refTable:     quote(d.RefTable),
		commitTable:  quote(d.CommitTable),
		patchTable:   quote(d.PatchTable),
		rootTable:    quote(d.Root.Table),
		rootKey:      quote(d.Root.Key),
		kinds:        map[string]*kind{},
	}
	if a.actorSetting == "" {
		a.actorSetting = DefaultHistoryActorSetting
	}
	for _, k := range d.Kinds {
		roles := map[string]string{"table": k.Table, "historyTable": k.HistoryTable, "key": k.Key, "id": k.ID, "ref": k.Ref, "root": k.Root, "tombstone": k.Tombstone, "version": k.Version}
		for role, column := range roles {
			if column == "" {
				return nil, fmt.Errorf("postgres: kind %q has no %s", k.Kind, role)
			}
			if role != "table" && role != "historyTable" {
				if _, ok := k.Columns[column]; !ok {
					return nil, fmt.Errorf("postgres: kind %q %s column %q is not in its columns", k.Kind, role, column)
				}
			}
		}
		a.kinds[k.Kind] = &kind{
			name: k.Kind, table: k.Table, historyTable: k.HistoryTable,
			key: k.Key, id: k.ID, ref: k.Ref, root: k.Root, tombstone: k.Tombstone, version: k.Version,
			columns: k.Columns,
		}
	}
	return a, nil
}

// Storage binds the adapter to a client.
func (a *Adapter) Storage(client Client) storage.Storage {
	return &store{a: a, client: client}
}

type store struct {
	a      *Adapter
	client Client
}

func (s *store) Transact(ctx context.Context, fn func(ctx context.Context, tx storage.Tx) error) error {
	return s.client.Transact(ctx, func(ctx context.Context, conn Conn) error {
		return fn(ctx, &tx{a: s.a, conn: conn})
	})
}

type tx struct {
	a    *Adapter
	conn Conn
}

func (t *tx) kind(name string) (*kind, error) {
	k, ok := t.a.kinds[name]
	if !ok {
		return nil, fmt.Errorf("postgres: unknown kind %q", name)
	}
	return k, nil
}

// quote quotes an identifier.
func quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// literal quotes a string as a SQL literal.
func literal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// queryRow runs a statement that returns at most one row, and reports
// whether it returned one.
func (t *tx) queryRow(ctx context.Context, sql string, args []any, dest ...any) (bool, error) {
	found := false
	err := t.conn.Query(ctx, sql, args, func(scan func(dest ...any) error) error {
		found = true
		return scan(dest...)
	})
	return found, err
}

// isUniqueViolation reports whether err is Postgres's unique_violation.
func isUniqueViolation(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == "23505"
}

// refColumns reads a ref's graph columns; scanRef scans them.
const refColumns = `id::text, root_id::text, COALESCE(parent_ref_id::text, ''), COALESCE(base_commit_id::text, ''), ` +
	`COALESCE(head_commit_id::text, ''), "name", sealed_at IS NOT NULL, deleted_at IS NOT NULL, _version`

type refScan struct {
	id, root, parent, base, head, name string
	sealed, discarded                  bool
	version                            int64
}

func (r *refScan) dest() []any {
	return []any{&r.id, &r.root, &r.parent, &r.base, &r.head, &r.name, &r.sealed, &r.discarded, &r.version}
}

func (r *refScan) ref() (storage.Ref, error) {
	out := storage.Ref{Name: r.name, Sealed: r.sealed, Discarded: r.discarded, Version: r.version}
	var err error
	for _, pair := range []struct {
		dst *string
		src string
	}{{&out.ID, r.id}, {&out.Root, r.root}, {&out.Parent, r.parent}, {&out.Base, r.base}, {&out.Head, r.head}} {
		if *pair.dst, err = optionalCanonicalUUID(pair.src); err != nil {
			return storage.Ref{}, err
		}
	}
	return out, nil
}

func (t *tx) scanRef(ctx context.Context, sql string, args ...any) (storage.Ref, bool, error) {
	var r refScan
	found, err := t.queryRow(ctx, sql, args, r.dest()...)
	if err != nil || !found {
		return storage.Ref{}, found, err
	}
	ref, err := r.ref()
	return ref, true, err
}

func (t *tx) CreateRef(ctx context.Context, ref storage.NewRef) (storage.Ref, error) {
	args, err := uuidArgs(ref.Root, ref.Parent, ref.Base, ref.Actor)
	if err != nil {
		return storage.Ref{}, err
	}
	sql := `INSERT INTO ` + t.a.refTable + ` (root_id, parent_ref_id, base_commit_id, "name", created_by, updated_by) ` +
		`VALUES ($1::uuid, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5::uuid, $5::uuid) RETURNING ` + refColumns
	out, _, err := t.scanRef(ctx, sql, args[0], args[1], args[2], ref.Name, args[3])
	if isUniqueViolation(err) {
		return storage.Ref{}, fmt.Errorf("%w: %q", storage.ErrNameTaken, ref.Name)
	}
	if err != nil {
		return storage.Ref{}, fmt.Errorf("postgres: create ref: %w", err)
	}
	return out, nil
}

func (t *tx) ReadRef(ctx context.Context, id string) (storage.Ref, error) {
	return t.readRef(ctx, id, "")
}

func (t *tx) LockRef(ctx context.Context, id string) (storage.Ref, error) {
	return t.readRef(ctx, id, " FOR UPDATE")
}

func (t *tx) readRef(ctx context.Context, id, lock string) (storage.Ref, error) {
	text, err := uuidText(id)
	if err != nil {
		return storage.Ref{}, err
	}
	ref, found, err := t.scanRef(ctx, `SELECT `+refColumns+` FROM `+t.a.refTable+` WHERE id = $1::uuid`+lock, text)
	if err != nil {
		return storage.Ref{}, fmt.Errorf("postgres: read ref: %w", err)
	}
	if !found {
		return storage.Ref{}, storage.ErrNotFound
	}
	return ref, nil
}

func (t *tx) UpdateRef(ctx context.Context, update storage.RefUpdate) (storage.Ref, error) {
	args, err := uuidArgs(update.ID, update.Head, update.Actor)
	if err != nil {
		return storage.Ref{}, err
	}
	sql := `UPDATE ` + t.a.refTable + ` SET head_commit_id = COALESCE(NULLIF($3, '')::uuid, head_commit_id), ` +
		`sealed_at = CASE WHEN $4::boolean THEN now() ELSE sealed_at END, updated_at = now(), updated_by = $5::uuid ` +
		`WHERE id = $1::uuid AND _version = $2 RETURNING ` + refColumns
	ref, found, err := t.scanRef(ctx, sql, args[0], update.Version, args[1], update.Seal, args[2])
	if err != nil {
		return storage.Ref{}, fmt.Errorf("postgres: update ref: %w", err)
	}
	if !found {
		return storage.Ref{}, storage.ErrVersionConflict
	}
	return ref, nil
}

func (t *tx) DiscardRef(ctx context.Context, id string, version int64, actor string) error {
	args, err := uuidArgs(id, actor)
	if err != nil {
		return err
	}
	n, err := t.conn.Exec(ctx, `UPDATE `+t.a.refTable+` SET deleted_at = now(), deleted_by = $3::uuid `+
		`WHERE id = $1::uuid AND _version = $2 AND deleted_at IS NULL`, args[0], version, args[1])
	if err != nil {
		return fmt.Errorf("postgres: discard ref: %w", err)
	}
	if n == 0 {
		return storage.ErrVersionConflict
	}
	return nil
}

// uuidArgs turns canonical ids into the hyphenated text a statement casts
// to uuid, with "" for "".
func uuidArgs(ids ...string) ([]any, error) {
	out := make([]any, len(ids))
	for i, id := range ids {
		text, err := optionalUUIDText(id)
		if err != nil {
			return nil, err
		}
		out[i] = text
	}
	return out, nil
}

// rows runs a statement whose one column is a row's JSON as text, and
// returns each as the canonical row of kind.
func (t *tx) rows(ctx context.Context, k *kind, sql string, args ...any) ([]json.RawMessage, error) {
	var out []json.RawMessage
	err := t.conn.Query(ctx, sql, args, func(scan func(dest ...any) error) error {
		var text string
		if err := scan(&text); err != nil {
			return err
		}
		row, err := canonical.PostgresRow(k.columns, json.RawMessage(text))
		if err != nil {
			return fmt.Errorf("%s row: %w", k.name, err)
		}
		out = append(out, row)
		return nil
	})
	return out, err
}

func (t *tx) Rows(ctx context.Context, kindName, ref string) ([]json.RawMessage, error) {
	k, err := t.kind(kindName)
	if err != nil {
		return nil, err
	}
	text, err := uuidText(ref)
	if err != nil {
		return nil, err
	}
	rows, err := t.rows(ctx, k, `SELECT to_jsonb(t)::text FROM `+quote(k.table)+` AS t WHERE t.`+quote(k.ref)+` = $1::uuid`, text)
	if err != nil {
		return nil, fmt.Errorf("postgres: read %s rows: %w", k.name, err)
	}
	return rows, nil
}

func (t *tx) UpsertRow(ctx context.Context, kindName string, write storage.RowWrite) (json.RawMessage, error) {
	k, err := t.kind(kindName)
	if err != nil {
		return nil, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(write.Row, &members); err != nil {
		return nil, fmt.Errorf("postgres: a %s row is a JSON object: %w", k.name, err)
	}
	if members == nil {
		return nil, fmt.Errorf("postgres: a %s row is a JSON object", k.name)
	}
	delete(members, k.id)
	delete(members, k.version)
	if key, ok := members[k.key]; ok && string(key) == "null" {
		delete(members, k.key)
	}
	tombstone, err := json.Marshal(write.Tombstone)
	if err != nil {
		return nil, err
	}
	forced := map[string]json.RawMessage{k.tombstone: tombstone}
	for column, id := range map[string]string{k.ref: write.Ref, k.root: write.Root} {
		if forced[column], err = json.Marshal(id); err != nil {
			return nil, err
		}
	}
	for _, column := range []string{createdByColumn, updatedByColumn} {
		if _, ok := k.columns[column]; ok {
			if forced[column], err = json.Marshal(write.Actor); err != nil {
				return nil, err
			}
		}
	}
	for column, value := range forced {
		members[column] = value
	}
	input := make(map[string]json.RawMessage, len(members))
	columns := make([]string, 0, len(members)+2)
	for column, value := range members {
		class, ok := k.columns[column]
		if !ok {
			return nil, fmt.Errorf("postgres: the %s row has column %q, which its descriptor does not declare", k.name, column)
		}
		if input[column], err = inputValue(class, value); err != nil {
			return nil, fmt.Errorf("postgres: %s column %s: %w", k.name, column, err)
		}
		columns = append(columns, column)
	}
	for _, column := range []string{createdAtColumn, updatedAtColumn} {
		if _, ok := k.columns[column]; ok {
			if _, given := input[column]; !given {
				columns = append(columns, column)
			}
		}
	}
	sort.Strings(columns)
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}

	var names, values, updates []string
	for _, column := range columns {
		q := quote(column)
		names = append(names, q)
		class := k.columns[column]
		switch {
		case column == createdAtColumn || column == updatedAtColumn:
			values = append(values, "now()")
		case class == canonical.JSON || strings.HasSuffix(class, "[][]"):
			// A JSONB column takes the member itself, so a JSON null is
			// the value null, which a required column holds, rather than
			// SQL NULL, which jsonb_populate_record reads it as.
			values = append(values, "($1::jsonb -> "+literal(column)+")")
		default:
			values = append(values, "r."+q)
		}
		switch column {
		case k.key, k.ref, k.root, createdAtColumn, createdByColumn:
			// The conflict key, the root and the creation audit stay as
			// the row was first written.
		default:
			updates = append(updates, q+" = EXCLUDED."+q)
		}
	}
	table := quote(k.table)
	sql := `INSERT INTO ` + table + ` AS t (` + strings.Join(names, ", ") + `) ` +
		`SELECT ` + strings.Join(values, ", ") + ` FROM jsonb_populate_record(NULL::` + table + `, $1::jsonb) AS r ` +
		`ON CONFLICT (` + quote(k.key) + `, ` + quote(k.ref) + `) DO UPDATE SET ` + strings.Join(updates, ", ") +
		` RETURNING to_jsonb(t)::text`
	rows, err := t.rows(ctx, k, sql, string(inputJSON))
	if err != nil {
		return nil, fmt.Errorf("postgres: write %s row: %w", k.name, err)
	}
	if len(rows) != 1 {
		return nil, fmt.Errorf("postgres: write %s row: %d rows written", k.name, len(rows))
	}
	return rows[0], nil
}

func (t *tx) RemoveRow(ctx context.Context, kindName, ref, entityKey, actor string) (bool, error) {
	k, err := t.kind(kindName)
	if err != nil {
		return false, err
	}
	args, err := uuidArgs(entityKey, ref, actor)
	if err != nil {
		return false, err
	}
	sql := `WITH history_actor AS MATERIALIZED (SELECT set_config($1, $2, true) AS _history_actor) ` +
		`DELETE FROM ` + quote(k.table) + ` USING history_actor WHERE ` + quote(k.key) + ` = $3::uuid AND ` + quote(k.ref) + ` = $4::uuid`
	n, err := t.conn.Exec(ctx, sql, t.a.actorSetting, args[2], args[0], args[1])
	if err != nil {
		return false, fmt.Errorf("postgres: remove the %s row: %w", k.name, err)
	}
	if _, err := t.conn.Exec(ctx, `SELECT set_config($1, '', true)`, t.a.actorSetting); err != nil {
		return false, fmt.Errorf("postgres: clear the history actor: %w", err)
	}
	return n > 0, nil
}

func (t *tx) Images(ctx context.Context, kindName string, pins []storage.Pin) ([]json.RawMessage, error) {
	k, err := t.kind(kindName)
	if err != nil {
		return nil, err
	}
	if len(pins) == 0 {
		return nil, nil
	}
	ids := make([]string, len(pins))
	versions := make([]int64, len(pins))
	for i, pin := range pins {
		if ids[i], err = uuidText(pin.ID); err != nil {
			return nil, err
		}
		versions[i] = pin.Version
	}
	sql := `SELECT h.data::text FROM ` + quote(k.historyTable) + ` AS h ` +
		`JOIN unnest($1::text[]::uuid[], $2::bigint[]) AS p(id, version) ON h.` + quote(k.id) + ` = p.id AND h.` + quote(k.version) + ` = p.version`
	rows, err := t.rows(ctx, k, sql, ids, versions)
	if err != nil {
		return nil, fmt.Errorf("postgres: read %s history: %w", k.name, err)
	}
	return rows, nil
}

// commitColumns reads a commit; commitScan scans them.
const commitColumns = `id::text, root_id::text, ref_id::text, COALESCE(parent_commit_id::text, ''), COALESCE(message, ''), ` +
	`schema_epoch, content_hash, COALESCE("sequence"::text, ''), to_jsonb(created_at)::text, created_by::text`

type commitScan struct {
	id, root, ref, parent, message, hash, sequence, createdAt, createdBy string
	epoch                                                                int64
}

func (c *commitScan) dest() []any {
	return []any{&c.id, &c.root, &c.ref, &c.parent, &c.message, &c.epoch, &c.hash, &c.sequence, &c.createdAt, &c.createdBy}
}

func (c *commitScan) commit() (storage.Commit, error) {
	out := storage.Commit{Message: c.message, SchemaEpoch: c.epoch, ContentHash: c.hash}
	var err error
	for _, pair := range []struct {
		dst *string
		src string
	}{{&out.ID, c.id}, {&out.Root, c.root}, {&out.Ref, c.ref}, {&out.Parent, c.parent}, {&out.CreatedBy, c.createdBy}} {
		if *pair.dst, err = optionalCanonicalUUID(pair.src); err != nil {
			return storage.Commit{}, err
		}
	}
	if c.sequence != "" {
		sequence, err := strconv.ParseInt(c.sequence, 10, 64)
		if err != nil {
			return storage.Commit{}, err
		}
		out.Sequence = &sequence
	}
	createdAt, err := canonical.Postgres(canonical.DateTime, json.RawMessage(c.createdAt))
	if err != nil {
		return storage.Commit{}, err
	}
	if err := json.Unmarshal(createdAt, &out.CreatedAt); err != nil {
		return storage.Commit{}, err
	}
	return out, nil
}

// commits runs a statement that returns commitColumns.
func (t *tx) commits(ctx context.Context, sql string, args ...any) ([]storage.Commit, error) {
	var out []storage.Commit
	err := t.conn.Query(ctx, sql, args, func(scan func(dest ...any) error) error {
		var c commitScan
		if err := scan(c.dest()...); err != nil {
			return err
		}
		commit, err := c.commit()
		if err != nil {
			return err
		}
		out = append(out, commit)
		return nil
	})
	return out, err
}

func (t *tx) ReadCommit(ctx context.Context, id string) (storage.Commit, error) {
	text, err := uuidText(id)
	if err != nil {
		return storage.Commit{}, err
	}
	commits, err := t.commits(ctx, `SELECT `+commitColumns+` FROM `+t.a.commitTable+` WHERE id = $1::uuid`, text)
	if err != nil {
		return storage.Commit{}, fmt.Errorf("postgres: read commit: %w", err)
	}
	if len(commits) == 0 {
		return storage.Commit{}, storage.ErrNotFound
	}
	return commits[0], nil
}

func (t *tx) InsertCommit(ctx context.Context, commit storage.NewCommit) (storage.Commit, error) {
	args, err := uuidArgs(commit.Root, commit.Ref, commit.Parent, commit.Actor)
	if err != nil {
		return storage.Commit{}, err
	}
	var sequence any
	if commit.Sequence != nil {
		sequence = *commit.Sequence
	}
	sql := `INSERT INTO ` + t.a.commitTable + ` (root_id, ref_id, parent_commit_id, message, schema_epoch, content_hash, "sequence", created_by) ` +
		`VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, NULLIF($4, ''), $5, $6, $7::bigint, $8::uuid) RETURNING ` + commitColumns
	commits, err := t.commits(ctx, sql, args[0], args[1], args[2], commit.Message, commit.SchemaEpoch, commit.ContentHash, sequence, args[3])
	if err != nil {
		return storage.Commit{}, fmt.Errorf("postgres: write commit: %w", err)
	}
	return commits[0], nil
}

func (t *tx) InsertPatches(ctx context.Context, commit string, patches []storage.Patch) error {
	type row struct {
		Kind      string `json:"kind"`
		Key       string `json:"key"`
		ID        string `json:"id"`
		Version   int64  `json:"version"`
		Operation string `json:"op"`
	}
	rows := make([]row, len(patches))
	for i, p := range patches {
		args, err := uuidArgs(p.EntityKey, p.EntityID)
		if err != nil {
			return err
		}
		rows[i] = row{Kind: p.Kind, Key: args[0].(string), ID: args[1].(string), Version: p.EntityVersion, Operation: p.Operation}
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	commitText, err := uuidText(commit)
	if err != nil {
		return err
	}
	sql := `INSERT INTO ` + t.a.patchTable + ` (commit_id, entity_kind, entity_key, entity_id, entity_version, operation) ` +
		`SELECT $1::uuid, p.kind, p.key::uuid, p.id::uuid, p.version, p.op ` +
		`FROM jsonb_to_recordset($2::jsonb) AS p(kind text, key text, id text, version bigint, op text)`
	if _, err := t.conn.Exec(ctx, sql, commitText, string(payload)); err != nil {
		return fmt.Errorf("postgres: write patches: %w", err)
	}
	return nil
}

func (t *tx) Walk(ctx context.Context, commit string, limit int) ([]storage.Commit, error) {
	text, err := uuidText(commit)
	if err != nil {
		return nil, err
	}
	sql := `WITH RECURSIVE chain AS (` +
		`SELECT c.*, 1 AS depth FROM ` + t.a.commitTable + ` AS c WHERE c.id = $1::uuid ` +
		`UNION ALL SELECT c.*, chain.depth + 1 FROM ` + t.a.commitTable + ` AS c ` +
		`JOIN chain ON c.id = chain.parent_commit_id WHERE chain.depth < $2` +
		`) SELECT ` + commitColumns + ` FROM chain ORDER BY depth`
	commits, err := t.commits(ctx, sql, text, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("postgres: walk commits: %w", err)
	}
	return commits, nil
}

func (t *tx) RefCommits(ctx context.Context, ref, head string, limit int) ([]storage.Commit, error) {
	args, err := uuidArgs(head, ref)
	if err != nil {
		return nil, err
	}
	sql := `WITH RECURSIVE chain AS (` +
		`SELECT c.*, 1 AS depth FROM ` + t.a.commitTable + ` AS c WHERE c.id = $1::uuid AND c.ref_id = $2::uuid ` +
		`UNION ALL SELECT c.*, chain.depth + 1 FROM ` + t.a.commitTable + ` AS c ` +
		`JOIN chain ON c.id = chain.parent_commit_id WHERE c.ref_id = $2::uuid AND chain.depth < $3` +
		`) SELECT ` + commitColumns + ` FROM chain ORDER BY depth`
	commits, err := t.commits(ctx, sql, args[0], args[1], int64(limit))
	if err != nil {
		return nil, fmt.Errorf("postgres: list commits: %w", err)
	}
	return commits, nil
}

func (t *tx) Patches(ctx context.Context, commits []string) ([]storage.Patch, error) {
	if len(commits) == 0 {
		return nil, nil
	}
	ids, err := uuidTexts(commits)
	if err != nil {
		return nil, err
	}
	var out []storage.Patch
	sql := `SELECT commit_id::text, entity_kind, entity_key::text, entity_id::text, entity_version, operation ` +
		`FROM ` + t.a.patchTable + ` WHERE commit_id = ANY($1::text[]::uuid[])`
	err = t.conn.Query(ctx, sql, []any{ids}, func(scan func(dest ...any) error) error {
		var p storage.Patch
		if err := scan(&p.Commit, &p.Kind, &p.EntityKey, &p.EntityID, &p.EntityVersion, &p.Operation); err != nil {
			return err
		}
		var err error
		for _, id := range []*string{&p.Commit, &p.EntityKey, &p.EntityID} {
			if *id, err = canonicalUUID(*id); err != nil {
				return err
			}
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: read patches: %w", err)
	}
	return out, nil
}

func (t *tx) NextSequence(ctx context.Context, root string) (int64, error) {
	text, err := uuidText(root)
	if err != nil {
		return 0, err
	}
	// FOR NO KEY UPDATE conflicts with itself and not with the key-share
	// locks that inserting a row that references the root takes.
	if _, err := t.conn.Exec(ctx, `SELECT 1 FROM `+t.a.rootTable+` WHERE `+t.a.rootKey+` = $1::uuid FOR NO KEY UPDATE`, text); err != nil {
		return 0, fmt.Errorf("postgres: lock the root: %w", err)
	}
	var next int64
	if _, err := t.queryRow(ctx, `SELECT COALESCE(MAX("sequence"), 0) + 1 FROM `+t.a.commitTable+` WHERE root_id = $1::uuid`, []any{text}, &next); err != nil {
		return 0, fmt.Errorf("postgres: read the next sequence: %w", err)
	}
	return next, nil
}

func (t *tx) Prune(ctx context.Context, kindName string, retentionDays, batchSize int) (int64, error) {
	k, err := t.kind(kindName)
	if err != nil {
		return 0, err
	}
	// A kind declared without retentionDays has no prune function, and
	// keeps its history.
	function := quote(k.table + "_prune_history")
	var exists bool
	if _, err := t.queryRow(ctx, `SELECT to_regproc($1) IS NOT NULL`, []any{function}, &exists); err != nil {
		return 0, fmt.Errorf("postgres: find the %s prune function: %w", k.name, err)
	}
	if !exists {
		return 0, nil
	}
	var deleted int64
	if _, err := t.queryRow(ctx, `SELECT `+function+`($1::integer, NULLIF($2, 0)::integer)`, []any{int64(retentionDays), int64(batchSize)}, &deleted); err != nil {
		return 0, fmt.Errorf("postgres: prune %s history: %w", k.name, err)
	}
	return deleted, nil
}

func (t *tx) SweepLock(ctx context.Context) (bool, error) {
	var locked bool
	if _, err := t.queryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))`, []any{t.a.sweepKey}, &locked); err != nil {
		return false, fmt.Errorf("postgres: take the sweep lock: %w", err)
	}
	return locked, nil
}
