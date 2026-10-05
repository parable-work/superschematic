// Package sqlite is the SQLite storage adapter of the version-graph engine
// (D32): a storage.Storage over one fixed layout of tables, the same for
// every graph, so the engine keeps a graph in a SQLite file. Package
// postgres is its Postgres counterpart, over the tables sqlgen generates per
// graph. It is the Go port of the TypeScript adapter
// (runtime/versiongraph/typescript/src/sqlite.ts), statement for statement,
// and a file either one writes reads the same through the other.
//
// The layout is nine STRICT tables: ref, ref_history, commit, patch,
// snapshot_entry, release, release_history, member and member_history, each
// named by a function the caller gives (DefaultTableName puts graph_ before
// each), as is every index. Every row carries its graph's name, so one file
// holds several graphs, and every statement is scoped to the adapter's
// graph. An id is TEXT holding a UUID in its canonical form (base62); a
// version, a sequence and a tombstone are INTEGER; the times of refs,
// commits, release pointers and history images are INTEGER microseconds
// since the Unix epoch, returned as canonical date-times. A member row holds
// its kind and its role columns as columns and every other column of the
// descriptor as one canonical JSON object, data. Foreign keys check every
// edge inside the layout; there is no root table, so nothing checks a root
// but the adapter, which refuses a write whose ref or commit is another
// graph's or another root's.
//
// SQLite has no trigger that can assign NEW, so the adapter does what
// Postgres's triggers do, in the statements of the transaction that changes
// a row: it sets _version (1 on an insert, the old version plus 1 on an
// update), writes the row's image at its new version on an insert or an
// update, and on a delete writes the row's image at the old version plus 1
// with the kind's history actor column set to the delete's actor. An image
// leaves out the kind's history-excluded columns, and reads back as it was
// stored, while a live row reads with every column its kind declares. Refs
// and release pointers keep history too. Every value it writes is
// canonicalized by its class first (package canonical), so a read returns
// what is stored and needs no rules of its own.
//
// The adapter reaches SQLite through Client. DB, DBConn and DBTx bind
// database/sql; the package imports no SQLite driver, so the caller picks
// one.
package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// MinVersion is the oldest SQLite the adapter runs on: its tables are
// STRICT (3.37.0), and its writes read back with RETURNING (3.35.0). Its
// reads take lists through json_each and json_extract, which SQLite builds
// in from 3.38.0 and 3.37 has in builds with JSON1; Storage checks that
// they are there.
const MinVersion = "3.37.0"

// jsonProbe uses the JSON functions the adapter's statements use, and
// fails on a SQLite built without them.
const jsonProbe = `SELECT json_extract(p.value, '$[0]') FROM json_each('[[1]]') AS p`

// The audit columns the adapter writes on every member row when a kind has
// them.
const (
	createdAtColumn = "created_at"
	createdByColumn = "created_by"
	updatedAtColumn = "updated_at"
	updatedByColumn = "updated_by"
)

const microsPerDay = 86_400_000_000

// Options configure an Adapter.
type Options struct {
	// Graph is the graph's name, which every row of the graph carries.
	// Required, and not empty.
	Graph string
	// TableName names each table and index of the layout from its local
	// name ("ref", "member_entity"). Nil is DefaultTableName.
	TableName func(local string) string
	// Clock is the time a transaction writes, in microseconds since the
	// Unix epoch. A transaction reads it once, once it holds the file's
	// write lock. Nil is the system clock.
	Clock func() int64
	// ResultCode reads SQLite's extended result code from an error the
	// client returned, for a driver whose errors do not carry it as Code()
	// int. Nil is ResultCode.
	ResultCode func(err error) (code int, ok bool)
}

// Adapter holds one graph's statements over the layout. It is safe for
// concurrent use; bind a Client with Storage.
type Adapter struct {
	graph      string
	tableName  func(string) string
	t          tables
	clock      func() int64
	resultCode func(error) (int, bool)
	kinds      map[string]*kind
}

// kind is one member kind's role columns, columns and history.
type kind struct {
	name      string
	key       string
	id        string
	ref       string
	root      string
	tombstone string
	version   string
	columns   map[string]string
	// data is every declared column but the role ones: what data holds,
	// sorted.
	data          []string
	exclude       map[string]bool
	actor         string
	retentionDays *int
}

// descriptor is what the adapter reads of a graph descriptor.
type descriptor struct {
	Version int `json:"version"`
	Kinds   []struct {
		Kind      string `json:"kind"`
		Key       string `json:"key"`
		ID        string `json:"id"`
		Ref       string `json:"ref"`
		Root      string `json:"root"`
		Tombstone string `json:"tombstone"`
		Version   string `json:"version"`
		History   *struct {
			RetentionDays *int      `json:"retentionDays"`
			Exclude       *[]string `json:"exclude"`
			Actor         string    `json:"actor"`
		} `json:"history"`
		Columns map[string]string `json:"columns"`
	} `json:"kinds"`
}

// New reads a graph's descriptor (version 3). The adapter reads only its
// kinds: each kind's role columns (the root among them), its columns' value
// classes and its history; the tables the descriptor names are the Postgres
// adapter's.
func New(raw json.RawMessage, opts Options) (*Adapter, error) {
	var d descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("sqlite: read the descriptor: %w", err)
	}
	if d.Version != 3 {
		return nil, fmt.Errorf("sqlite: descriptor version %d; this adapter reads version 3", d.Version)
	}
	if opts.Graph == "" {
		return nil, errors.New("sqlite: an adapter needs its graph's name (Options.Graph)")
	}
	a := &Adapter{
		graph:      opts.Graph,
		tableName:  opts.TableName,
		clock:      opts.Clock,
		resultCode: opts.ResultCode,
		kinds:      map[string]*kind{},
	}
	if a.tableName == nil {
		a.tableName = DefaultTableName
	}
	if a.clock == nil {
		a.clock = func() int64 { return time.Now().UnixMicro() }
	}
	if a.resultCode == nil {
		a.resultCode = ResultCode
	}
	var err error
	if a.t, err = tablesOf(a.tableName); err != nil {
		return nil, err
	}
	for _, k := range d.Kinds {
		roles := []struct{ role, column string }{
			{"key", k.Key}, {"id", k.ID}, {"ref", k.Ref}, {"root", k.Root}, {"tombstone", k.Tombstone}, {"version", k.Version},
		}
		roleColumns := map[string]bool{}
		for _, r := range roles {
			if r.column == "" {
				return nil, fmt.Errorf("sqlite: kind %q has no %s", k.Kind, r.role)
			}
			if _, ok := k.Columns[r.column]; !ok {
				return nil, fmt.Errorf("sqlite: kind %q %s column %q is not in its columns", k.Kind, r.role, r.column)
			}
			roleColumns[r.column] = true
		}
		if k.Columns[k.Tombstone] != "boolean" || k.Columns[k.Version] != "integer" {
			return nil, fmt.Errorf("sqlite: kind %q: a tombstone is a boolean column and a version an integer one", k.Kind)
		}
		if k.History == nil || k.History.Exclude == nil {
			return nil, fmt.Errorf("sqlite: kind %q has no history", k.Kind)
		}
		kd := &kind{
			name: k.Kind, key: k.Key, id: k.ID, ref: k.Ref, root: k.Root, tombstone: k.Tombstone, version: k.Version,
			columns: k.Columns, exclude: map[string]bool{}, actor: k.History.Actor, retentionDays: k.History.RetentionDays,
		}
		for column := range k.Columns {
			if !roleColumns[column] {
				kd.data = append(kd.data, column)
			}
		}
		sort.Strings(kd.data)
		for _, column := range *k.History.Exclude {
			kd.exclude[column] = true
		}
		a.kinds[k.Kind] = kd
	}
	return a, nil
}

// CreateTables creates the layout's tables and indexes where they are
// missing (Layout), in one transaction of the client.
func (a *Adapter) CreateTables(ctx context.Context, client Client) error {
	statements, err := Layout(a.tableName)
	if err != nil {
		return err
	}
	return client.Transact(ctx, func(ctx context.Context, conn Conn) error {
		// The layout stores no time, but its transaction reads the clock
		// once, as every one does, so a clock that a writer of the file
		// steps reads as the TypeScript adapter's does.
		if _, _, err := a.begin(ctx, conn); err != nil {
			return err
		}
		for _, statement := range statements {
			if _, err := conn.Exec(ctx, statement); err != nil {
				return fmt.Errorf("sqlite: create the layout: %w", err)
			}
		}
		return nil
	})
}

// Storage binds the adapter to a client. It refuses, in one transaction of
// the client, a connection whose foreign keys are off, a SQLite older than
// MinVersion, and one built without the JSON functions json_each and
// json_extract.
func (a *Adapter) Storage(ctx context.Context, client Client) (storage.Storage, error) {
	err := client.Transact(ctx, func(ctx context.Context, conn Conn) error {
		var on int64
		found, err := queryRow(ctx, conn, "PRAGMA foreign_keys", nil, &on)
		if err != nil {
			return fmt.Errorf("sqlite: read foreign_keys: %w", err)
		}
		if !found || on != 1 {
			return errors.New("sqlite: the connection's foreign keys are off; turn them on outside a transaction (PRAGMA foreign_keys = ON)")
		}
		var version string
		if _, err := queryRow(ctx, conn, "SELECT sqlite_version()", nil, &version); err != nil {
			return fmt.Errorf("sqlite: read the SQLite version: %w", err)
		}
		older, err := olderThan(version, MinVersion)
		if err != nil {
			return err
		}
		if older {
			return fmt.Errorf("sqlite: SQLite %s is older than %s, which the layout's STRICT tables and its statements' RETURNING need", version, MinVersion)
		}
		var one int64
		if found, err := queryRow(ctx, conn, jsonProbe, nil, &one); err != nil || !found || one != 1 {
			return fmt.Errorf("sqlite: SQLite %s lacks the JSON functions json_each and json_extract, which the adapter's statements use (built in from 3.38.0, and in 3.37 with JSON1): %v", version, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &store{a: a, client: client}, nil
}

// olderThan reports whether SQLite version a (major.minor.patch) is older
// than b.
func olderThan(a, b string) (bool, error) {
	parse := func(v string) ([3]int, error) {
		var out [3]int
		parts := strings.Split(v, ".")
		if len(parts) != 3 {
			return out, fmt.Errorf("sqlite: SQLite reports version %q, not major.minor.patch", v)
		}
		for i, part := range parts {
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 {
				return out, fmt.Errorf("sqlite: SQLite reports version %q, not major.minor.patch", v)
			}
			out[i] = n
		}
		return out, nil
	}
	x, err := parse(a)
	if err != nil {
		return false, err
	}
	y, err := parse(b)
	if err != nil {
		return false, err
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] < y[i], nil
		}
	}
	return false, nil
}

type store struct {
	a      *Adapter
	client Client
}

// openTime keys, in a transaction's context, the time of the transaction
// open on a Conn.
type openTime struct{}

type open struct {
	conn Conn
	time int64
}

// sameConn reports whether two Conns are one, as a client hands a
// transaction begun inside another the Conn it handed the outer one. Only
// pointers are compared, since comparing another type can panic.
func sameConn(a, b Conn) bool {
	t := reflect.TypeOf(a)
	return t != nil && t == reflect.TypeOf(b) && t.Kind() == reflect.Pointer && a == b
}

// Transact runs fn in one transaction of the client. The outermost
// transaction on a Conn reads the clock once, at its start, which on a
// connection of the client's own is after BEGIN IMMEDIATE has taken the
// write lock, so times order as the writes do; a transaction begun inside
// it, on the same Conn, takes its time, as every statement of a Postgres
// transaction has the one now().
func (s *store) Transact(ctx context.Context, fn func(ctx context.Context, tx storage.Tx) error) error {
	return s.client.Transact(ctx, func(ctx context.Context, conn Conn) error {
		ctx, now, err := s.a.begin(ctx, conn)
		if err != nil {
			return err
		}
		return fn(ctx, &tx{a: s.a, conn: conn, time: now})
	})
}

// begin gives a transaction on conn its time: the outer one's when ctx
// carries a transaction open on conn, else the clock's, read once and
// carried in the context it returns. It refuses a time outside
// ±(2^53 - 1) microseconds, which the TypeScript adapter cannot read.
func (a *Adapter) begin(ctx context.Context, conn Conn) (context.Context, int64, error) {
	if o, ok := ctx.Value(openTime{}).(*open); ok && sameConn(o.conn, conn) {
		return ctx, o.time, nil
	}
	now := a.clock()
	if now > maxExactMicros || now < -maxExactMicros {
		return nil, 0, fmt.Errorf("sqlite: the clock returned %d, not a whole number of microseconds a number holds exactly", now)
	}
	return context.WithValue(ctx, openTime{}, &open{conn: conn, time: now}), now, nil
}

// tx is one transaction's view of the graph, over a Conn, at the
// transaction's time.
type tx struct {
	a    *Adapter
	conn Conn
	time int64
}

func (t *tx) kind(name string) (*kind, error) {
	k, ok := t.a.kinds[name]
	if !ok {
		return nil, fmt.Errorf("sqlite: unknown kind %q", name)
	}
	return k, nil
}

// queryRow runs a statement that returns at most one row, and reports
// whether it returned one.
func queryRow(ctx context.Context, conn Conn, sql string, args []any, dest ...any) (bool, error) {
	found := false
	err := conn.Query(ctx, sql, args, func(scan func(dest ...any) error) error {
		found = true
		return scan(dest...)
	})
	return found, err
}

func (t *tx) queryRow(ctx context.Context, sql string, args []any, dest ...any) (bool, error) {
	return queryRow(ctx, t.conn, sql, args, dest...)
}

// isUniqueViolation reports whether err is SQLITE_CONSTRAINT_UNIQUE.
func (t *tx) isUniqueViolation(err error) bool {
	code, ok := t.a.resultCode(err)
	return ok && code == ResultConstraintUnique
}

// isSet reads a nullable column's IS NOT NULL.
func isSet(set int64) bool { return set == 1 }

// refColumns reads a ref's columns, each nullable one as whether it is set
// and its value; refScan scans them.
const refColumns = `id, root_id, COALESCE(parent_ref_id, ''), COALESCE(base_commit_id, ''), COALESCE(head_commit_id, ''), name, ` +
	`sealed_at IS NOT NULL, COALESCE(sealed_at, 0), created_at, created_by, updated_at, updated_by, ` +
	`deleted_at IS NOT NULL, COALESCE(deleted_at, 0), deleted_by IS NOT NULL, COALESCE(deleted_by, ''), _version`

type refScan struct {
	id, root, parent, base, head, name string
	sealed, sealedAt                   int64
	createdAt                          int64
	createdBy                          string
	updatedAt                          int64
	updatedBy                          string
	deleted, deletedAt                 int64
	hasDeletedBy                       int64
	deletedBy                          string
	version                            int64
}

func (r *refScan) dest() []any {
	return []any{&r.id, &r.root, &r.parent, &r.base, &r.head, &r.name, &r.sealed, &r.sealedAt, &r.createdAt, &r.createdBy,
		&r.updatedAt, &r.updatedBy, &r.deleted, &r.deletedAt, &r.hasDeletedBy, &r.deletedBy, &r.version}
}

// ref is the ref the row holds. It refuses a seal or a discard time the
// TypeScript adapter could not read, as that adapter reads both.
func (r *refScan) ref() (storage.Ref, error) {
	for _, c := range []struct {
		column string
		set    int64
		micros int64
	}{{"sealed_at", r.sealed, r.sealedAt}, {"deleted_at", r.deleted, r.deletedAt}} {
		if isSet(c.set) {
			if err := exactTime(c.micros, c.column); err != nil {
				return storage.Ref{}, err
			}
		}
	}
	return storage.Ref{
		ID: r.id, Root: r.root, Parent: r.parent, Base: r.base, Head: r.head, Name: r.name,
		Sealed: isSet(r.sealed), Discarded: isSet(r.deleted), Version: r.version,
	}, nil
}

// image is a ref's history image: each of its columns, an id in its
// canonical form and a time as a canonical date-time. A ref's parent, base
// and head reference rows, whose ids are never empty, so "" is none.
func (r *refScan) image() (string, error) {
	image := map[string]string{
		"id":             jsonString(r.id),
		"root_id":        jsonString(r.root),
		"parent_ref_id":  optionalJSONString(r.parent, r.parent != ""),
		"base_commit_id": optionalJSONString(r.base, r.base != ""),
		"head_commit_id": optionalJSONString(r.head, r.head != ""),
		"name":           jsonString(r.name),
		"created_by":     jsonString(r.createdBy),
		"updated_by":     jsonString(r.updatedBy),
		"deleted_by":     optionalJSONString(r.deletedBy, isSet(r.hasDeletedBy)),
		"_version":       strconv.FormatInt(r.version, 10),
	}
	for _, column := range []struct {
		name    string
		micros  int64
		present bool
	}{
		{"sealed_at", r.sealedAt, isSet(r.sealed)},
		{"created_at", r.createdAt, true},
		{"updated_at", r.updatedAt, true},
		{"deleted_at", r.deletedAt, isSet(r.deleted)},
	} {
		text, err := jsonTime(column.micros, column.present)
		if err != nil {
			return "", err
		}
		image[column.name] = text
	}
	return writeObject(image), nil
}

// scanRef runs a statement that returns refColumns, and reads its one row.
func (t *tx) scanRef(ctx context.Context, sql string, args ...any) (*refScan, error) {
	var r refScan
	found, err := t.queryRow(ctx, sql, args, r.dest()...)
	if err != nil || !found {
		return nil, err
	}
	return &r, nil
}

// requireRef refuses a write whose ref is not one of this graph's refs of
// root.
func (t *tx) requireRef(ctx context.Context, id, root, what string) error {
	var one int64
	found, err := t.queryRow(ctx, `SELECT 1 AS found FROM `+t.a.t.ref+` WHERE graph = ?1 AND id = ?2 AND root_id = ?3`,
		[]any{t.a.graph, id, root}, &one)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s: ref %s is not a ref of root %s in graph %s", what, id, root, t.a.graph)
	}
	return nil
}

// requireCommit refuses a write whose commit is not one of this graph's
// commits of root.
func (t *tx) requireCommit(ctx context.Context, id, root, what string) error {
	var one int64
	found, err := t.queryRow(ctx, `SELECT 1 AS found FROM `+t.a.t.commit+` WHERE graph = ?1 AND id = ?2 AND root_id = ?3`,
		[]any{t.a.graph, id, root}, &one)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s: commit %s is not a commit of root %s in graph %s", what, id, root, t.a.graph)
	}
	return nil
}

// history writes a ref's or a release pointer's history image.
func (t *tx) history(ctx context.Context, table, id string, version int64, operation, data string) error {
	_, err := t.conn.Exec(ctx, `INSERT INTO `+table+` (history_id, graph, id, _version, operation, data, recorded_at) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)`,
		newID(), t.a.graph, id, version, operation, data, t.time)
	return err
}

// memberHistory writes a member's history image: its canonical row's
// members less the kind's history-excluded columns.
func (t *tx) memberHistory(ctx context.Context, k *kind, id string, version int64, operation string, members map[string]string) error {
	image := make(map[string]string, len(members))
	for column, value := range members {
		if !k.exclude[column] {
			image[column] = value
		}
	}
	_, err := t.conn.Exec(ctx, `INSERT INTO `+t.a.t.memberHistory+` (history_id, graph, kind, id, _version, operation, data, recorded_at) `+
		`VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
		newID(), t.a.graph, k.name, id, version, operation, writeObject(image), t.time)
	return err
}

// nullable is an optional id as a statement's argument: nil for "".
func nullable(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func (t *tx) CreateRef(ctx context.Context, ref storage.NewRef) (storage.Ref, error) {
	out, err := t.createRef(ctx, ref)
	if err != nil && !errors.Is(err, storage.ErrNameTaken) {
		return storage.Ref{}, fmt.Errorf("sqlite: create ref: %w", err)
	}
	return out, err
}

func (t *tx) createRef(ctx context.Context, ref storage.NewRef) (storage.Ref, error) {
	if ref.Parent != "" {
		if err := t.requireRef(ctx, ref.Parent, ref.Root, "the parent"); err != nil {
			return storage.Ref{}, err
		}
	}
	if ref.Base != "" {
		if err := t.requireCommit(ctx, ref.Base, ref.Root, "the base"); err != nil {
			return storage.Ref{}, err
		}
	}
	id := newID()
	r, err := t.scanRef(ctx, `INSERT INTO `+t.a.t.ref+` (id, graph, root_id, parent_ref_id, base_commit_id, head_commit_id, name, sealed_at, `+
		`created_at, created_by, updated_at, updated_by, deleted_at, deleted_by, _version) `+
		`VALUES (?1, ?2, ?3, ?4, ?5, NULL, ?6, NULL, ?7, ?8, ?7, ?8, NULL, NULL, 1) RETURNING `+refColumns,
		id, t.a.graph, ref.Root, nullable(ref.Parent), nullable(ref.Base), ref.Name, t.time, ref.Actor)
	if t.isUniqueViolation(err) {
		return storage.Ref{}, fmt.Errorf("%w: %q", storage.ErrNameTaken, ref.Name)
	}
	if err != nil {
		return storage.Ref{}, err
	}
	if r == nil {
		return storage.Ref{}, errors.New("the insert returned no row")
	}
	image, err := r.image()
	if err != nil {
		return storage.Ref{}, err
	}
	if err := t.history(ctx, t.a.t.refHistory, id, 1, "INSERT", image); err != nil {
		return storage.Ref{}, err
	}
	return r.ref()
}

func (t *tx) ReadRef(ctx context.Context, id string) (storage.Ref, error) {
	r, err := t.scanRef(ctx, `SELECT `+refColumns+` FROM `+t.a.t.ref+` WHERE graph = ?1 AND id = ?2`, t.a.graph, id)
	if err != nil {
		return storage.Ref{}, fmt.Errorf("sqlite: read ref: %w", err)
	}
	if r == nil {
		return storage.Ref{}, storage.ErrNotFound
	}
	return r.ref()
}

// LockRef reads a ref as ReadRef does: the one writer the file's write lock
// lets in orders every write, so a ref needs no lock of its own.
func (t *tx) LockRef(ctx context.Context, id string) (storage.Ref, error) {
	return t.ReadRef(ctx, id)
}

func (t *tx) UpdateRef(ctx context.Context, update storage.RefUpdate) (storage.Ref, error) {
	out, err := t.updateRef(ctx, update)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return storage.Ref{}, fmt.Errorf("sqlite: update ref: %w", err)
	}
	return out, err
}

func (t *tx) updateRef(ctx context.Context, update storage.RefUpdate) (storage.Ref, error) {
	if update.Head != "" || update.Base != "" {
		var root string
		found, err := t.queryRow(ctx, `SELECT root_id FROM `+t.a.t.ref+` WHERE graph = ?1 AND id = ?2`, []any{t.a.graph, update.ID}, &root)
		if err != nil {
			return storage.Ref{}, err
		}
		if !found {
			return storage.Ref{}, storage.ErrVersionConflict
		}
		if update.Head != "" {
			if err := t.requireCommit(ctx, update.Head, root, "the head"); err != nil {
				return storage.Ref{}, err
			}
		}
		if update.Base != "" {
			if err := t.requireCommit(ctx, update.Base, root, "the base"); err != nil {
				return storage.Ref{}, err
			}
		}
	}
	var seal int64
	if update.Seal {
		seal = 1
	}
	r, err := t.scanRef(ctx, `UPDATE `+t.a.t.ref+` SET head_commit_id = COALESCE(?3, head_commit_id), base_commit_id = COALESCE(?4, base_commit_id), `+
		`sealed_at = CASE WHEN ?5 = 1 THEN ?6 ELSE sealed_at END, updated_at = ?6, updated_by = ?7, _version = _version + 1 `+
		`WHERE graph = ?1 AND id = ?2 AND _version = ?8 RETURNING `+refColumns,
		t.a.graph, update.ID, nullable(update.Head), nullable(update.Base), seal, t.time, update.Actor, update.Version)
	if err != nil {
		return storage.Ref{}, err
	}
	if r == nil {
		return storage.Ref{}, storage.ErrVersionConflict
	}
	image, err := r.image()
	if err != nil {
		return storage.Ref{}, err
	}
	if err := t.history(ctx, t.a.t.refHistory, update.ID, r.version, "UPDATE", image); err != nil {
		return storage.Ref{}, err
	}
	return r.ref()
}

func (t *tx) DiscardRef(ctx context.Context, id string, version int64, actor string) error {
	r, err := t.scanRef(ctx, `UPDATE `+t.a.t.ref+` SET deleted_at = ?3, deleted_by = ?4, _version = _version + 1 `+
		`WHERE graph = ?1 AND id = ?2 AND _version = ?5 AND deleted_at IS NULL RETURNING `+refColumns,
		t.a.graph, id, t.time, actor, version)
	if err != nil {
		return fmt.Errorf("sqlite: discard ref: %w", err)
	}
	if r == nil {
		return storage.ErrVersionConflict
	}
	image, err := r.image()
	if err == nil {
		err = t.history(ctx, t.a.t.refHistory, id, r.version, "UPDATE", image)
	}
	if err != nil {
		return fmt.Errorf("sqlite: discard ref: %w", err)
	}
	return nil
}

// member is a member row as the layout holds it: its role columns, and its
// other columns as JSON text.
type member struct {
	id, key, ref, root string
	tombstone          bool
	version            int64
	data               map[string]string
}

const memberColumns = "id, entity_key, ref_id, root_id, tombstone, _version, data"

// members runs a statement that returns memberColumns.
func (t *tx) members(ctx context.Context, sql string, args ...any) ([]member, error) {
	var out []member
	err := t.conn.Query(ctx, sql, args, func(scan func(dest ...any) error) error {
		var m member
		var tombstone int64
		var data string
		if err := scan(&m.id, &m.key, &m.ref, &m.root, &tombstone, &m.version, &data); err != nil {
			return err
		}
		m.tombstone = tombstone == 1
		var err error
		if m.data, err = readObject(data, "data"); err != nil {
			return err
		}
		out = append(out, m)
		return nil
	})
	return out, err
}

// canonicalMembers is a member's canonical row's members: its role columns
// under the descriptor's names, and every other column the kind declares,
// null where the stored row lacks it, as a Postgres row has a column added
// after it was written. A stored column the kind no longer declares is kept
// as stored.
func canonicalMembers(k *kind, m member) map[string]string {
	members := make(map[string]string, len(m.data)+6)
	for column, value := range m.data {
		members[column] = value
	}
	for _, column := range k.data {
		if _, ok := members[column]; !ok {
			members[column] = "null"
		}
	}
	members[k.id] = jsonString(m.id)
	members[k.key] = jsonString(m.key)
	members[k.ref] = jsonString(m.ref)
	members[k.root] = jsonString(m.root)
	members[k.tombstone] = strconv.FormatBool(m.tombstone)
	members[k.version] = strconv.FormatInt(m.version, 10)
	return members
}

func (t *tx) Rows(ctx context.Context, kindName, ref string) ([]json.RawMessage, error) {
	k, err := t.kind(kindName)
	if err != nil {
		return nil, err
	}
	found, err := t.members(ctx, `SELECT `+memberColumns+` FROM `+t.a.t.member+` WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3`, t.a.graph, k.name, ref)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read %s rows: %w", k.name, err)
	}
	out := make([]json.RawMessage, len(found))
	for i, m := range found {
		out[i] = json.RawMessage(writeObject(canonicalMembers(k, m)))
	}
	return out, nil
}

// roleValue is the canonical form of a value of a role column's class,
// given as a string: an id, a key, an actor.
func roleValue(k *kind, column, value string) (string, error) {
	text, err := canonicalText(k.columns[column], value)
	if err != nil {
		return "", err
	}
	var out string
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return "", fmt.Errorf("the %s column %s holds a string", k.name, column)
	}
	return out, nil
}

func (t *tx) UpsertRow(ctx context.Context, kindName string, write storage.RowWrite) (json.RawMessage, error) {
	k, err := t.kind(kindName)
	if err != nil {
		return nil, err
	}
	row, err := t.upsert(ctx, k, write)
	if err != nil {
		return nil, fmt.Errorf("sqlite: write %s row: %w", k.name, err)
	}
	return row, nil
}

func (t *tx) upsert(ctx context.Context, k *kind, write storage.RowWrite) (json.RawMessage, error) {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(write.Row, &parsed); err != nil || parsed == nil {
		return nil, fmt.Errorf("a %s row is a JSON object", k.name)
	}
	// The row's other columns, each canonical. The adapter writes the ref,
	// the root and the tombstone, and never the id or the version.
	given := map[string]string{}
	var err error
	for column, value := range parsed {
		class, ok := k.columns[column]
		if !ok {
			return nil, fmt.Errorf("the %s row has column %q, which its descriptor does not declare", k.name, column)
		}
		switch column {
		case k.id, k.version, k.ref, k.root, k.tombstone, k.key:
			continue
		}
		if given[column], err = canonicalOf(class, value); err != nil {
			return nil, fmt.Errorf("column %s: %w", column, err)
		}
	}
	// A row without an entity key, or with a null one, is a new entity.
	key, hasKey := "", false
	if value, ok := parsed[k.key]; ok && string(bytes.TrimSpace(value)) != "null" {
		text, err := canonicalOf(k.columns[k.key], value)
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", k.key, err)
		}
		if err := json.Unmarshal([]byte(text), &key); err != nil {
			return nil, fmt.Errorf("the %s row's entity key is a string", k.name)
		}
		hasKey = true
	}
	ref, err := roleValue(k, k.ref, write.Ref)
	if err != nil {
		return nil, err
	}
	root, err := roleValue(k, k.root, write.Root)
	if err != nil {
		return nil, err
	}
	if err := t.requireRef(ctx, ref, root, "the row's ref"); err != nil {
		return nil, err
	}
	now, err := microsToDateTime(t.time)
	if err != nil {
		return nil, err
	}
	audit := func(column, value string) error {
		if class, ok := k.columns[column]; ok {
			text, err := canonicalText(class, value)
			if err != nil {
				return fmt.Errorf("column %s: %w", column, err)
			}
			given[column] = text
		}
		return nil
	}
	var existing []member
	if hasKey {
		if existing, err = t.members(ctx, `SELECT `+memberColumns+` FROM `+t.a.t.member+` WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3 AND entity_key = ?4`,
			t.a.graph, k.name, ref, key); err != nil {
			return nil, err
		}
	}
	if len(existing) > 0 {
		old := existing[0]
		// A column the row lacks keeps its stored value; the entity key, the
		// root and the creation audit stay as the row was first written.
		delete(given, createdAtColumn)
		delete(given, createdByColumn)
		if err := audit(updatedAtColumn, now); err != nil {
			return nil, err
		}
		if err := audit(updatedByColumn, write.Actor); err != nil {
			return nil, err
		}
		data := make(map[string]string, len(old.data)+len(given))
		for column, value := range old.data {
			data[column] = value
		}
		for column, value := range given {
			data[column] = value
		}
		// A column the kind gained after the row was written is null, as a
		// Postgres row has it.
		for _, column := range k.data {
			if _, ok := data[column]; !ok {
				data[column] = "null"
			}
		}
		stored := old
		stored.tombstone, stored.version, stored.data = write.Tombstone, old.version+1, data
		changed, err := t.conn.Exec(ctx, `UPDATE `+t.a.t.member+` SET tombstone = ?3, _version = ?4, data = ?5 WHERE graph = ?1 AND id = ?2 AND _version = ?6`,
			t.a.graph, stored.id, boolInt(stored.tombstone), stored.version, writeObject(stored.data), old.version)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, fmt.Errorf("%d rows written", changed)
		}
		members := canonicalMembers(k, stored)
		if err := t.memberHistory(ctx, k, stored.id, stored.version, "UPDATE", members); err != nil {
			return nil, err
		}
		return json.RawMessage(writeObject(members)), nil
	}
	// A column the row lacks holds its default, null; the audit columns hold
	// the write's actor and time.
	for _, column := range []string{createdAtColumn, updatedAtColumn} {
		if err := audit(column, now); err != nil {
			return nil, err
		}
	}
	for _, column := range []string{createdByColumn, updatedByColumn} {
		if err := audit(column, write.Actor); err != nil {
			return nil, err
		}
	}
	data := make(map[string]string, len(k.data))
	for _, column := range k.data {
		if value, ok := given[column]; ok {
			data[column] = value
		} else {
			data[column] = "null"
		}
	}
	stored := member{ref: ref, root: root, tombstone: write.Tombstone, version: 1, data: data}
	if stored.id, err = roleValue(k, k.id, newID()); err != nil {
		return nil, err
	}
	stored.key = key
	if !hasKey {
		if stored.key, err = roleValue(k, k.key, newID()); err != nil {
			return nil, err
		}
	}
	if _, err := t.conn.Exec(ctx, `INSERT INTO `+t.a.t.member+` (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, data) `+
		`VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, 1, ?8)`,
		stored.id, t.a.graph, k.name, stored.key, stored.ref, stored.root, boolInt(stored.tombstone), writeObject(data)); err != nil {
		return nil, err
	}
	members := canonicalMembers(k, stored)
	if err := t.memberHistory(ctx, k, stored.id, 1, "INSERT", members); err != nil {
		return nil, err
	}
	return json.RawMessage(writeObject(members)), nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// remove hard-deletes a member row and writes its DELETE image: the row at
// its version plus 1, with the kind's history actor column, when it has
// one, set to actor.
func (t *tx) remove(ctx context.Context, k *kind, m member, actor string) error {
	changed, err := t.conn.Exec(ctx, `DELETE FROM `+t.a.t.member+` WHERE graph = ?1 AND id = ?2`, t.a.graph, m.id)
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%d rows deleted", changed)
	}
	members := canonicalMembers(k, m)
	members[k.version] = strconv.FormatInt(m.version+1, 10)
	if k.actor != "" {
		if members[k.actor], err = canonicalText(k.columns[k.actor], actor); err != nil {
			return fmt.Errorf("column %s: %w", k.actor, err)
		}
	}
	return t.memberHistory(ctx, k, m.id, m.version+1, "DELETE", members)
}

func (t *tx) RemoveRow(ctx context.Context, kindName, ref, entityKey, actor string) (bool, error) {
	k, err := t.kind(kindName)
	if err != nil {
		return false, err
	}
	removed, err := t.removeRow(ctx, k, ref, entityKey, actor)
	if err != nil {
		return false, fmt.Errorf("sqlite: remove the %s row: %w", k.name, err)
	}
	return removed, nil
}

func (t *tx) removeRow(ctx context.Context, k *kind, ref, entityKey, actor string) (bool, error) {
	key, err := roleValue(k, k.key, entityKey)
	if err != nil {
		return false, err
	}
	found, err := t.members(ctx, `SELECT `+memberColumns+` FROM `+t.a.t.member+` WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3 AND entity_key = ?4`,
		t.a.graph, k.name, ref, key)
	if err != nil {
		return false, err
	}
	for _, m := range found {
		if err := t.remove(ctx, k, m, actor); err != nil {
			return false, err
		}
	}
	return len(found) > 0, nil
}

// Images reads each pinned image as it was stored, as a Postgres history
// image reads: one taken before its kind gained a column lacks it, and the
// core reads a content column a row lacks as null.
func (t *tx) Images(ctx context.Context, kindName string, pins []storage.Pin) ([]json.RawMessage, error) {
	k, err := t.kind(kindName)
	if err != nil {
		return nil, err
	}
	if len(pins) == 0 {
		return nil, nil
	}
	list := make([]string, len(pins))
	for i, pin := range pins {
		list[i] = "[" + jsonString(pin.ID) + "," + strconv.FormatInt(pin.Version, 10) + "]"
	}
	var out []json.RawMessage
	err = t.conn.Query(ctx, `SELECT h.data FROM json_each(?3) AS p JOIN `+t.a.t.memberHistory+` AS h `+
		`ON h.id = json_extract(p.value, '$[0]') AND h._version = json_extract(p.value, '$[1]') `+
		`WHERE h.graph = ?1 AND h.kind = ?2`,
		[]any{t.a.graph, k.name, "[" + strings.Join(list, ",") + "]"}, func(scan func(dest ...any) error) error {
			var data string
			if err := scan(&data); err != nil {
				return err
			}
			out = append(out, json.RawMessage(data))
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("sqlite: read %s history: %w", k.name, err)
	}
	return out, nil
}

// hasSnapshot is the SQL that tells whether the commit whose id is idSQL
// has a snapshot.
func (t *tx) hasSnapshot(idSQL string) string {
	return `EXISTS (SELECT 1 FROM ` + t.a.t.snapshot + ` AS s WHERE s.commit_id = ` + idSQL + `)`
}

// commitColumns reads a commit, followed by whether it has a snapshot;
// commitScan scans them.
const commitColumns = `c.id, c.root_id, c.ref_id, COALESCE(c.parent_commit_id, ''), COALESCE(c.message, ''), c.schema_epoch, c.content_hash, ` +
	`c.sequence IS NOT NULL, COALESCE(c.sequence, 0), c.created_at, c.created_by`

type commitScan struct {
	id, root, ref, parent, message, hash string
	epoch, tagged, sequence, createdAt   int64
	createdBy                            string
	snapshot                             int64
}

func (c *commitScan) dest() []any {
	return []any{&c.id, &c.root, &c.ref, &c.parent, &c.message, &c.epoch, &c.hash, &c.tagged, &c.sequence, &c.createdAt, &c.createdBy, &c.snapshot}
}

func (c *commitScan) commit() (storage.Commit, error) {
	createdAt, err := microsToDateTime(c.createdAt)
	if err != nil {
		return storage.Commit{}, err
	}
	out := storage.Commit{
		ID: c.id, Root: c.root, Ref: c.ref, Parent: c.parent, Message: c.message, SchemaEpoch: c.epoch, ContentHash: c.hash,
		CreatedAt: createdAt, CreatedBy: c.createdBy, Snapshot: c.snapshot == 1,
	}
	if c.tagged == 1 {
		sequence := c.sequence
		out.Sequence = &sequence
	}
	return out, nil
}

// commits runs a statement that returns commitColumns and the snapshot
// flag.
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
	commits, err := t.commits(ctx, `SELECT `+commitColumns+`, `+t.hasSnapshot("c.id")+` AS snapshot FROM `+t.a.t.commit+` AS c WHERE c.graph = ?1 AND c.id = ?2`,
		t.a.graph, id)
	if err != nil {
		return storage.Commit{}, fmt.Errorf("sqlite: read commit: %w", err)
	}
	if len(commits) == 0 {
		return storage.Commit{}, storage.ErrNotFound
	}
	return commits[0], nil
}

func (t *tx) InsertCommit(ctx context.Context, commit storage.NewCommit) (storage.Commit, error) {
	out, err := t.insertCommit(ctx, commit)
	if err != nil {
		return storage.Commit{}, fmt.Errorf("sqlite: write commit: %w", err)
	}
	return out, nil
}

func (t *tx) insertCommit(ctx context.Context, commit storage.NewCommit) (storage.Commit, error) {
	if err := t.requireRef(ctx, commit.Ref, commit.Root, "the commit's ref"); err != nil {
		return storage.Commit{}, err
	}
	if commit.Parent != "" {
		if err := t.requireCommit(ctx, commit.Parent, commit.Root, "the commit's parent"); err != nil {
			return storage.Commit{}, err
		}
	}
	createdAt, err := microsToDateTime(t.time)
	if err != nil {
		return storage.Commit{}, err
	}
	var message, sequence any
	if commit.Message != "" {
		message = commit.Message
	}
	if commit.Sequence != nil {
		sequence = *commit.Sequence
	}
	id := newID()
	if _, err := t.conn.Exec(ctx, `INSERT INTO `+t.a.t.commit+` (id, graph, root_id, ref_id, parent_commit_id, message, schema_epoch, content_hash, sequence, created_at, created_by) `+
		`VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11)`,
		id, t.a.graph, commit.Root, commit.Ref, nullable(commit.Parent), message, commit.SchemaEpoch, commit.ContentHash, sequence, t.time, commit.Actor); err != nil {
		return storage.Commit{}, err
	}
	out := storage.Commit{
		ID: id, Root: commit.Root, Ref: commit.Ref, Parent: commit.Parent, Message: commit.Message, SchemaEpoch: commit.SchemaEpoch,
		ContentHash: commit.ContentHash, CreatedAt: createdAt, CreatedBy: commit.Actor,
	}
	if commit.Sequence != nil {
		s := *commit.Sequence
		out.Sequence = &s
	}
	return out, nil
}

// requireOwnCommit refuses a commit of another graph, whatever its root,
// before writing its patches or its snapshot.
func (t *tx) requireOwnCommit(ctx context.Context, id, what string) error {
	var one int64
	found, err := t.queryRow(ctx, `SELECT 1 AS found FROM `+t.a.t.commit+` WHERE graph = ?1 AND id = ?2`, []any{t.a.graph, id}, &one)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s: commit %s is not a commit of graph %s", what, id, t.a.graph)
	}
	return nil
}

// pinned is an entity a patch or a snapshot entry pins: its kind's name and
// its key and id in their canonical forms.
func (t *tx) pinned(kindName, entityKey, entityID string) (*kind, string, string, error) {
	k, err := t.kind(kindName)
	if err != nil {
		return nil, "", "", err
	}
	key, err := roleValue(k, k.key, entityKey)
	if err != nil {
		return nil, "", "", err
	}
	id, err := roleValue(k, k.id, entityID)
	if err != nil {
		return nil, "", "", err
	}
	return k, key, id, nil
}

func (t *tx) InsertPatches(ctx context.Context, commit string, patches []storage.Patch) error {
	if err := t.insertPatches(ctx, commit, patches); err != nil {
		return fmt.Errorf("sqlite: write patches: %w", err)
	}
	return nil
}

func (t *tx) insertPatches(ctx context.Context, commit string, patches []storage.Patch) error {
	if err := t.requireOwnCommit(ctx, commit, "the patches' commit"); err != nil {
		return err
	}
	for _, p := range patches {
		k, key, id, err := t.pinned(p.Kind, p.EntityKey, p.EntityID)
		if err != nil {
			return err
		}
		if _, err := t.conn.Exec(ctx, `INSERT INTO `+t.a.t.patch+` (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version, operation) `+
			`VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
			newID(), t.a.graph, commit, k.name, key, id, p.EntityVersion, p.Operation); err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) Walk(ctx context.Context, commit string, limit int) ([]storage.Commit, error) {
	// The walk carries each commit's snapshot flag, and goes no further than
	// the first commit that has one.
	sql := `WITH RECURSIVE chain (id, parent, depth, snapshotted) AS (` +
		`SELECT c.id, c.parent_commit_id, 1, ` + t.hasSnapshot("c.id") + ` FROM ` + t.a.t.commit + ` AS c WHERE c.graph = ?1 AND c.id = ?2 ` +
		`UNION ALL SELECT c.id, c.parent_commit_id, chain.depth + 1, ` + t.hasSnapshot("c.id") + ` FROM ` + t.a.t.commit + ` AS c ` +
		`JOIN chain ON c.id = chain.parent WHERE c.graph = ?1 AND chain.depth < ?3 AND NOT chain.snapshotted` +
		`) SELECT ` + commitColumns + `, chain.snapshotted AS snapshot FROM chain JOIN ` + t.a.t.commit + ` AS c ON c.id = chain.id ORDER BY chain.depth`
	commits, err := t.commits(ctx, sql, t.a.graph, commit, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("sqlite: walk commits: %w", err)
	}
	return commits, nil
}

func (t *tx) RefCommits(ctx context.Context, ref, head string, limit int) ([]storage.Commit, error) {
	sql := `WITH RECURSIVE chain (id, parent, depth) AS (` +
		`SELECT c.id, c.parent_commit_id, 1 FROM ` + t.a.t.commit + ` AS c WHERE c.graph = ?1 AND c.id = ?2 AND c.ref_id = ?3 ` +
		`UNION ALL SELECT c.id, c.parent_commit_id, chain.depth + 1 FROM ` + t.a.t.commit + ` AS c ` +
		`JOIN chain ON c.id = chain.parent WHERE c.graph = ?1 AND c.ref_id = ?3 AND chain.depth < ?4` +
		`) SELECT ` + commitColumns + `, ` + t.hasSnapshot("c.id") + ` AS snapshot FROM chain JOIN ` + t.a.t.commit + ` AS c ON c.id = chain.id ORDER BY chain.depth`
	commits, err := t.commits(ctx, sql, t.a.graph, head, ref, int64(limit))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list commits: %w", err)
	}
	return commits, nil
}

func (t *tx) Patches(ctx context.Context, commits []string) ([]storage.Patch, error) {
	if len(commits) == 0 {
		return nil, nil
	}
	list := make([]string, len(commits))
	for i, commit := range commits {
		list[i] = jsonString(commit)
	}
	var out []storage.Patch
	err := t.conn.Query(ctx, `SELECT commit_id, entity_kind, entity_key, entity_id, entity_version, operation FROM `+t.a.t.patch+` `+
		`WHERE graph = ?1 AND commit_id IN (SELECT value FROM json_each(?2))`,
		[]any{t.a.graph, "[" + strings.Join(list, ",") + "]"}, func(scan func(dest ...any) error) error {
			var p storage.Patch
			if err := scan(&p.Commit, &p.Kind, &p.EntityKey, &p.EntityID, &p.EntityVersion, &p.Operation); err != nil {
				return err
			}
			out = append(out, p)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("sqlite: read patches: %w", err)
	}
	return out, nil
}

// NextSequence is the root's highest sequence plus one: the file's one
// writer orders every tagger, so the root needs no lock of its own.
func (t *tx) NextSequence(ctx context.Context, root string) (int64, error) {
	var next int64
	if _, err := t.queryRow(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 AS next FROM `+t.a.t.commit+` WHERE graph = ?1 AND root_id = ?2`,
		[]any{t.a.graph, root}, &next); err != nil {
		return 0, fmt.Errorf("sqlite: read the next sequence: %w", err)
	}
	return next, nil
}

// Prune deletes a kind's images older than its declared retention, or
// retentionDays when it is not 0, keeping each row's newest image and every
// image a patch or a snapshot pins, at most batchSize of them (0 for no
// limit), oldest first. A kind declared without retentionDays keeps its
// history.
func (t *tx) Prune(ctx context.Context, kindName string, retentionDays, batchSize int) (int64, error) {
	k, err := t.kind(kindName)
	if err != nil {
		return 0, err
	}
	if k.retentionDays == nil {
		return 0, nil
	}
	if batchSize < 0 {
		return 0, fmt.Errorf("sqlite: prune %s history: a batch is a whole number of images, 0 for no limit, not %d", k.name, batchSize)
	}
	days := retentionDays
	if days == 0 {
		days = *k.retentionDays
	}
	limit := int64(batchSize)
	if batchSize == 0 {
		limit = -1
	}
	// Older than the retention, not the newest image of its row, and pinned
	// by no patch and no snapshot; the oldest first.
	sql := `DELETE FROM ` + t.a.t.memberHistory + ` WHERE history_id IN (` +
		`SELECT h.history_id FROM ` + t.a.t.memberHistory + ` AS h WHERE h.graph = ?1 AND h.kind = ?2 AND h.recorded_at < ?3 ` +
		`AND EXISTS (SELECT 1 FROM ` + t.a.t.memberHistory + ` AS newer WHERE newer.id = h.id AND newer._version > h._version) ` +
		`AND NOT EXISTS (SELECT 1 FROM ` + t.a.t.patch + ` AS pin WHERE pin.graph = h.graph AND pin.entity_id = h.id AND pin.entity_version = h._version) ` +
		`AND NOT EXISTS (SELECT 1 FROM ` + t.a.t.snapshot + ` AS pin WHERE pin.graph = h.graph AND pin.entity_id = h.id AND pin.entity_version = h._version) ` +
		`ORDER BY h.recorded_at, h.id, h._version LIMIT ?4)`
	deleted, err := t.conn.Exec(ctx, sql, t.a.graph, k.name, t.time-int64(days)*microsPerDay, limit)
	if err != nil {
		return 0, fmt.Errorf("sqlite: prune %s history: %w", k.name, err)
	}
	return deleted, nil
}

// SweepLock reports true: the one writer the file's write lock lets in is
// the only sweeper there can be.
func (t *tx) SweepLock(context.Context) (bool, error) {
	return true, nil
}

func (t *tx) Snapshot(ctx context.Context, commit string) ([]storage.SnapshotEntry, error) {
	var out []storage.SnapshotEntry
	err := t.conn.Query(ctx, `SELECT entity_kind, entity_key, entity_id, entity_version FROM `+t.a.t.snapshot+` WHERE graph = ?1 AND commit_id = ?2`,
		[]any{t.a.graph, commit}, func(scan func(dest ...any) error) error {
			var e storage.SnapshotEntry
			if err := scan(&e.Kind, &e.EntityKey, &e.EntityID, &e.EntityVersion); err != nil {
				return err
			}
			out = append(out, e)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("sqlite: read the snapshot: %w", err)
	}
	return out, nil
}

func (t *tx) InsertSnapshot(ctx context.Context, commit string, entries []storage.SnapshotEntry) error {
	if len(entries) == 0 {
		return nil
	}
	if err := t.insertSnapshot(ctx, commit, entries); err != nil {
		return fmt.Errorf("sqlite: write the snapshot: %w", err)
	}
	return nil
}

func (t *tx) insertSnapshot(ctx context.Context, commit string, entries []storage.SnapshotEntry) error {
	if err := t.requireOwnCommit(ctx, commit, "the snapshot's commit"); err != nil {
		return err
	}
	for _, e := range entries {
		k, key, id, err := t.pinned(e.Kind, e.EntityKey, e.EntityID)
		if err != nil {
			return err
		}
		if _, err := t.conn.Exec(ctx, `INSERT INTO `+t.a.t.snapshot+` (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version) `+
			`VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)`,
			newID(), t.a.graph, commit, k.name, key, id, e.EntityVersion); err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) Commits(ctx context.Context) ([]storage.CommitNode, error) {
	var out []storage.CommitNode
	err := t.conn.Query(ctx, `SELECT c.id, COALESCE(c.parent_commit_id, ''), c.sequence IS NOT NULL AS tagged, `+t.hasSnapshot("c.id")+` AS snapshot `+
		`FROM `+t.a.t.commit+` AS c WHERE c.graph = ?1`,
		[]any{t.a.graph}, func(scan func(dest ...any) error) error {
			var n storage.CommitNode
			var tagged, snapshot int64
			if err := scan(&n.ID, &n.Parent, &tagged, &snapshot); err != nil {
				return err
			}
			n.Tagged, n.Snapshot = tagged == 1, snapshot == 1
			out = append(out, n)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("sqlite: read the commits: %w", err)
	}
	return out, nil
}

// releaseColumns reads a release pointer; releaseScan scans them.
const releaseColumns = "id, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version"

type releaseScan struct {
	id, root, commit string
	createdAt        int64
	createdBy        string
	updatedAt        int64
	updatedBy        string
	version          int64
}

func (r *releaseScan) dest() []any {
	return []any{&r.id, &r.root, &r.commit, &r.createdAt, &r.createdBy, &r.updatedAt, &r.updatedBy, &r.version}
}

func (r *releaseScan) release() storage.Release {
	return storage.Release{ID: r.id, Root: r.root, Commit: r.commit, Version: r.version}
}

// image is a release pointer's history image, as a ref's is written.
func (r *releaseScan) image() (string, error) {
	image := map[string]string{
		"id":         jsonString(r.id),
		"root_id":    jsonString(r.root),
		"commit_id":  jsonString(r.commit),
		"created_by": jsonString(r.createdBy),
		"updated_by": jsonString(r.updatedBy),
		"_version":   strconv.FormatInt(r.version, 10),
	}
	for name, micros := range map[string]int64{"created_at": r.createdAt, "updated_at": r.updatedAt} {
		text, err := jsonTime(micros, true)
		if err != nil {
			return "", err
		}
		image[name] = text
	}
	return writeObject(image), nil
}

func (t *tx) ReadRelease(ctx context.Context, root string) (storage.Release, error) {
	var r releaseScan
	found, err := t.queryRow(ctx, `SELECT `+releaseColumns+` FROM `+t.a.t.release+` WHERE graph = ?1 AND root_id = ?2`, []any{t.a.graph, root}, r.dest()...)
	if err != nil {
		return storage.Release{}, fmt.Errorf("sqlite: read the release: %w", err)
	}
	if !found {
		return storage.Release{}, storage.ErrNotFound
	}
	return r.release(), nil
}

func (t *tx) WriteRelease(ctx context.Context, write storage.ReleaseWrite) (storage.Release, error) {
	out, err := t.writeRelease(ctx, write)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return storage.Release{}, fmt.Errorf("sqlite: write the release: %w", err)
	}
	return out, err
}

func (t *tx) writeRelease(ctx context.Context, write storage.ReleaseWrite) (storage.Release, error) {
	if err := t.requireCommit(ctx, write.Commit, write.Root, "the release's commit"); err != nil {
		return storage.Release{}, err
	}
	var r releaseScan
	var found bool
	var err error
	operation := "UPDATE"
	if write.Version == 0 {
		// A root's first pointer. Another first pointer of the root holds
		// its slot, as a move at a stale version would.
		operation = "INSERT"
		found, err = t.queryRow(ctx, `INSERT INTO `+t.a.t.release+` (id, graph, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version) `+
			`VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?5, ?6, 1) ON CONFLICT (graph, root_id) DO NOTHING RETURNING `+releaseColumns,
			[]any{newID(), t.a.graph, write.Root, write.Commit, t.time, write.Actor}, r.dest()...)
	} else {
		found, err = t.queryRow(ctx, `UPDATE `+t.a.t.release+` SET commit_id = ?3, updated_at = ?4, updated_by = ?5, _version = _version + 1 `+
			`WHERE graph = ?1 AND root_id = ?2 AND _version = ?6 RETURNING `+releaseColumns,
			[]any{t.a.graph, write.Root, write.Commit, t.time, write.Actor, write.Version}, r.dest()...)
	}
	if err != nil {
		return storage.Release{}, err
	}
	if !found {
		return storage.Release{}, storage.ErrVersionConflict
	}
	image, err := r.image()
	if err != nil {
		return storage.Release{}, err
	}
	if err := t.history(ctx, t.a.t.releaseHistory, r.id, r.version, operation, image); err != nil {
		return storage.Release{}, err
	}
	return r.release(), nil
}

func (t *tx) DiscardedRefs(ctx context.Context, grace time.Duration) ([]storage.Ref, error) {
	return t.refs(ctx, `SELECT `+refColumns+` FROM `+t.a.t.ref+` WHERE graph = ?1 AND deleted_at IS NOT NULL AND deleted_at < ?2 ORDER BY deleted_at, id`,
		t.a.graph, t.time-grace.Microseconds())
}

func (t *tx) IdleDrafts(ctx context.Context, idle time.Duration) ([]storage.Ref, error) {
	return t.refs(ctx, `SELECT `+refColumns+` FROM `+t.a.t.ref+` `+
		`WHERE graph = ?1 AND deleted_at IS NULL AND parent_ref_id IS NOT NULL AND updated_at < ?2 ORDER BY updated_at, id`,
		t.a.graph, t.time-idle.Microseconds())
}

// refs runs a statement that returns refColumns.
func (t *tx) refs(ctx context.Context, sql string, args ...any) ([]storage.Ref, error) {
	var out []storage.Ref
	err := t.conn.Query(ctx, sql, args, func(scan func(dest ...any) error) error {
		var r refScan
		if err := scan(r.dest()...); err != nil {
			return err
		}
		ref, err := r.ref()
		if err != nil {
			return err
		}
		out = append(out, ref)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sqlite: read refs: %w", err)
	}
	return out, nil
}

func (t *tx) RemoveRefRows(ctx context.Context, kindName, ref, actor string) (int64, error) {
	k, err := t.kind(kindName)
	if err != nil {
		return 0, err
	}
	found, err := t.members(ctx, `SELECT `+memberColumns+` FROM `+t.a.t.member+` WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3`, t.a.graph, k.name, ref)
	if err == nil {
		for _, m := range found {
			if err = t.remove(ctx, k, m, actor); err != nil {
				break
			}
		}
	}
	if err != nil {
		return 0, fmt.Errorf("sqlite: remove the %s rows of a ref: %w", k.name, err)
	}
	return int64(len(found)), nil
}
