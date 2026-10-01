package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	versiongraph "github.com/parable-work/superschematic/runtime/versiongraph/go"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/engine"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/postgres"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// databaseVariable names the Postgres the scenarios run against.
const databaseVariable = "SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL"

// fixtureSchemaEpoch is the schema epoch the fixture's Recipe graph
// declares, which the engine of every step writes and reads at unless the
// step names another.
const fixtureSchemaEpoch = 1

// fixtureSnapshotEvery is the snapshot interval the fixture's Recipe graph
// declares, which the engine of every step takes snapshots at unless the
// step names another.
const fixtureSnapshotEvery = 3

// defaultActor is the actor of a step that names none: "Cook", a UUID in
// its canonical form.
const defaultActor = "Cook"

var fixtureDir = filepath.Join("..", "..", "testdata", "fixture")

type scenario struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Steps       []step `json:"steps"`
}

type step struct {
	Op            string                    `json:"op"`
	As            string                    `json:"as"`
	Actor         *string                   `json:"actor"`
	Root          string                    `json:"root"`
	Name          string                    `json:"name"`
	Ref           string                    `json:"ref"`
	From          string                    `json:"from"`
	To            string                    `json:"to"`
	Source        string                    `json:"source"`
	Target        string                    `json:"target"`
	Commit        string                    `json:"commit"`
	ToCommit      string                    `json:"toCommit"`
	Version       *int64                    `json:"version"`
	Edits         map[string]kindEdits      `json:"edits"`
	Message       string                    `json:"message"`
	Tag           bool                      `json:"tag"`
	Resolutions   []versiongraph.Resolution `json:"resolutions"`
	WalkCeiling   int                       `json:"walkCeiling"`
	SchemaEpoch   *int64                    `json:"schemaEpoch"`
	SnapshotEvery int                       `json:"snapshotEvery"`
	Sweep         *sweepOptions             `json:"sweep"`
	Kind          string                    `json:"kind"`
	Statement     string                    `json:"statement"`
	Args          []sqlArg                  `json:"args"`
	Expect        expect                    `json:"expect"`
}

type kindEdits struct {
	Upsert []json.RawMessage `json:"upsert"`
	Delete []string          `json:"delete"`
	Unset  []string          `json:"unset"`
}

// sweepOptions are a sweep step's options; durations are in seconds.
type sweepOptions struct {
	DiscardGraceSeconds int64 `json:"discardGraceSeconds"`
	AbandonAfterSeconds int64 `json:"abandonAfterSeconds"`
	PruneBatch          int   `json:"pruneBatch"`
}

type sqlArg struct {
	UUID   string `json:"uuid"`
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
}

type partialRow = map[string]json.RawMessage

type expect struct {
	Error         string                  `json:"error"`
	Ref           *refExpect              `json:"ref"`
	Commit        json.RawMessage         `json:"commit"`
	Tree          map[string][]partialRow `json:"tree"`
	Saved         map[string][]partialRow `json:"saved"`
	ContentHash   string                  `json:"contentHash"`
	ContentHashOf string                  `json:"contentHashOf"`
	Findings      *[]partialRow           `json:"findings"`
	Conflicts     *[]partialRow           `json:"conflicts"`
	Changes       *[]partialRow           `json:"changes"`
	Commits       *[]string               `json:"commits"`
	Rows          *[]partialRow           `json:"rows"`
	Patches       *[]partialRow           `json:"patches"`
	Snapshot      *[]partialRow           `json:"snapshot"`
	Release       *releaseExpect          `json:"release"`
	Report        partialRow              `json:"report"`
}

type releaseExpect struct {
	Commit  string `json:"commit"`
	Version *int64 `json:"version"`
}

type refExpect struct {
	Version *int64          `json:"version"`
	Sealed  *bool           `json:"sealed"`
	Name    *string         `json:"name"`
	Parent  json.RawMessage `json:"parent"`
	Base    json.RawMessage `json:"base"`
	Head    json.RawMessage `json:"head"`
}

type commitExpect struct {
	Ref           *string         `json:"ref"`
	Parent        json.RawMessage `json:"parent"`
	Message       *string         `json:"message"`
	Sequence      json.RawMessage `json:"sequence"`
	SchemaEpoch   *int64          `json:"schemaEpoch"`
	ContentHash   string          `json:"contentHash"`
	ContentHashOf string          `json:"contentHashOf"`
}

// TestScenarios runs every scenario in runtime/versiongraph/testdata/scenarios
// through the engine and the Postgres adapter, each in a schema of its own
// that holds the fixture's DDL. It needs the Postgres named by
// SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL.
func TestScenarios(t *testing.T) {
	dsn := os.Getenv(databaseVariable)
	if dsn == "" {
		t.Skip("set " + databaseVariable + " to run the version-graph scenarios against Postgres")
	}
	descriptor, err := os.ReadFile(filepath.Join(fixtureDir, "recipe.json"))
	if err != nil {
		t.Fatal(err)
	}
	createSQL, err := os.ReadFile(filepath.Join(fixtureDir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "scenarios", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no scenarios found")
	}
	for _, file := range files {
		s := readScenario(t, file)
		if want := strings.TrimSuffix(filepath.Base(file), ".json"); s.Name != want {
			t.Fatalf("%s: scenario name %q, want the file's name %q", file, s.Name, want)
		}
		t.Run(s.Name, func(t *testing.T) {
			r := newRunner(t, dsn, descriptor, createSQL)
			for i, st := range s.Steps {
				r.step = fmt.Sprintf("step %d (%s)", i, st.Op)
				r.run(t, st)
			}
		})
	}
}

func readScenario(t *testing.T, file string) scenario {
	t.Helper()
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.DisallowUnknownFields()
	var s scenario
	if err := decoder.Decode(&s); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	if len(s.Steps) == 0 {
		t.Fatalf("%s: a scenario has steps", file)
	}
	return s
}

// runner holds one scenario's database, engine and named results.
type runner struct {
	ctx        context.Context
	descriptor json.RawMessage
	config     *pgx.ConnConfig
	conn       *pgx.Conn
	store      storage.Storage
	adapter    *postgres.Adapter
	engine     *engine.Engine
	refs       map[string]storage.Ref
	commits    map[string]storage.Commit
	releases   map[string]storage.Release
	step       string
	// holder is the transaction of another connection that holds the
	// graph's sweep lock, between holdSweepLock and releaseSweepLock.
	holder pgx.Tx
}

func newRunner(t *testing.T, dsn string, descriptor, createSQL []byte) *runner {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	// The fixture's DDL creates pgcrypto if it is missing. An extension's
	// name is unique in the database, so create it once in public, where
	// every schema's search path finds it, before test packages running in
	// parallel each try to create it in their own schema.
	if _, err := admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public"); err != nil && !isUniqueViolation(err) {
		t.Fatalf("create pgcrypto: %v", err)
	}
	schema := fmt.Sprintf("vg_scenario_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect to the scenario's schema: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, string(createSQL)); err != nil {
		t.Fatalf("apply the fixture's DDL: %v", err)
	}
	adapter, err := postgres.New(descriptor, postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	store := adapter.Storage(postgres.Pgx(conn))
	eng, err := engine.New(descriptor, store, engine.Options{SchemaEpoch: fixtureSchemaEpoch, SnapshotEvery: fixtureSnapshotEvery})
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{
		ctx: ctx, descriptor: descriptor, config: config, conn: conn, store: store, adapter: adapter, engine: eng,
		refs: map[string]storage.Ref{}, commits: map[string]storage.Commit{}, releases: map[string]storage.Release{},
	}
	t.Cleanup(func() {
		if r.holder != nil {
			_ = r.holder.Rollback(context.Background())
		}
	})
	return r
}

func (r *runner) fatalf(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Fatalf(r.step+": "+format, args...)
}

// refID resolves a ref named by an earlier step's "as", or a literal id
// written "id:<uuid>".
func (r *runner) refID(t *testing.T, name string) string {
	t.Helper()
	if literal, ok := strings.CutPrefix(name, "id:"); ok {
		return literal
	}
	ref, ok := r.refs[name]
	if !ok {
		r.fatalf(t, "no ref is named %q", name)
	}
	return ref.ID
}

// commitID resolves a commit named by an earlier step's "as", or a
// literal id written "id:<uuid>".
func (r *runner) commitID(t *testing.T, name string) string {
	t.Helper()
	if literal, ok := strings.CutPrefix(name, "id:"); ok {
		return literal
	}
	commit, ok := r.commits[name]
	if !ok {
		r.fatalf(t, "no commit is named %q", name)
	}
	return commit.ID
}

// version is the step's version, else the named ref's current one.
func (r *runner) version(t *testing.T, st step, name string) int64 {
	t.Helper()
	if st.Version != nil {
		return *st.Version
	}
	ref, ok := r.refs[name]
	if !ok {
		r.fatalf(t, "no ref is named %q", name)
	}
	return ref.Version
}

// trackRef records a ref's new state under every name bound to it.
func (r *runner) trackRef(ref storage.Ref) {
	for name, known := range r.refs {
		if known.ID == ref.ID {
			r.refs[name] = ref
		}
	}
}

// engineFor is the scenario's engine, at the step's walk ceiling, schema
// epoch and snapshot interval when it names them.
func (r *runner) engineFor(t *testing.T, st step) *engine.Engine {
	t.Helper()
	eng := r.engine
	if st.SchemaEpoch != nil || st.SnapshotEvery != 0 {
		opts := engine.Options{SchemaEpoch: fixtureSchemaEpoch, SnapshotEvery: fixtureSnapshotEvery}
		if st.SchemaEpoch != nil {
			opts.SchemaEpoch = *st.SchemaEpoch
		}
		if st.SnapshotEvery != 0 {
			opts.SnapshotEvery = st.SnapshotEvery
		}
		var err error
		if eng, err = engine.New(r.descriptor, r.store, opts); err != nil {
			t.Fatal(err)
		}
	}
	if st.WalkCeiling != 0 {
		eng = eng.WithWalkCeiling(st.WalkCeiling)
	}
	return eng
}

func (r *runner) run(t *testing.T, st step) {
	t.Helper()
	ctx := r.ctx
	eng := r.engineFor(t, st)
	actor := defaultActor
	if st.Actor != nil {
		actor = *st.Actor
	}
	var (
		err         error
		ref         *storage.Ref
		commit      *storage.Commit
		tree        *engine.TreeResult
		saved       engine.Tree
		conflicts   []versiongraph.Conflict
		changes     []versiongraph.Change
		history     []storage.Commit
		rows        []json.RawMessage
		patches     []storage.Patch
		entries     []storage.SnapshotEntry
		release     *storage.Release
		report      *engine.SweepReport
		commitAware bool
	)
	switch st.Op {
	case "createPrimary", "branch":
		var created storage.Ref
		if st.Op == "createPrimary" {
			created, err = eng.CreatePrimary(ctx, actor, st.Root, st.Name)
		} else {
			created, err = eng.Branch(ctx, actor, r.refID(t, st.From), st.Name)
		}
		if err == nil {
			ref = &created
			if st.As != "" {
				r.refs[st.As] = created
			}
		}
	case "save":
		edits := engine.Edits{}
		for kind, e := range st.Edits {
			edits[kind] = engine.KindEdits{Upsert: e.Upsert, Delete: e.Delete, Unset: e.Unset}
		}
		var result *engine.SaveResult
		if result, err = eng.Save(ctx, actor, r.refID(t, st.Ref), r.version(t, st, st.Ref), edits); err == nil {
			ref, saved = &result.Ref, result.Saved
		}
	case "commit", "seal", "revert":
		var result *engine.CommitResult
		switch st.Op {
		case "commit":
			result, err = eng.Commit(ctx, actor, r.refID(t, st.Ref), r.version(t, st, st.Ref), engine.CommitOptions{Message: st.Message, Tag: st.Tag})
		case "seal":
			result, err = eng.Seal(ctx, actor, r.refID(t, st.Ref), r.version(t, st, st.Ref))
		default:
			result, err = eng.Revert(ctx, actor, r.refID(t, st.Ref), r.version(t, st, st.Ref), r.commitID(t, st.ToCommit))
		}
		if err == nil {
			ref, commit, commitAware = &result.Ref, result.Commit, true
		}
	case "merge", "rebase":
		var result *engine.MergeResult
		if st.Op == "merge" {
			opts := engine.CommitOptions{Message: st.Message, Tag: st.Tag}
			result, err = eng.Merge(ctx, actor, r.refID(t, st.Source), r.refID(t, st.Target), r.version(t, st, st.Target), st.Resolutions, opts)
		} else {
			result, err = eng.Rebase(ctx, actor, r.refID(t, st.Ref), r.version(t, st, st.Ref), st.Resolutions)
		}
		if err == nil {
			ref, commit, conflicts, commitAware = &result.Ref, result.Commit, result.Conflicts, true
		}
	case "release":
		version := r.releases[st.Root].Version
		if st.Version != nil {
			version = *st.Version
		}
		var released storage.Release
		if released, err = eng.Release(ctx, actor, st.Root, r.commitID(t, st.Commit), version); err == nil {
			release = &released
			r.releases[st.Root] = released
		}
	case "released":
		var result *engine.ReleasedResult
		if result, err = eng.Released(ctx, st.Root); err == nil {
			release, tree = &result.Release, &result.TreeResult
		}
	case "sweep":
		opts := engine.SweepOptions{Actor: actor}
		if o := st.Sweep; o != nil {
			opts.DiscardGrace = time.Duration(o.DiscardGraceSeconds) * time.Second
			opts.AbandonAfter = time.Duration(o.AbandonAfterSeconds) * time.Second
			opts.PruneBatch = o.PruneBatch
		}
		report, err = eng.Sweep(ctx, opts)
	case "holdSweepLock":
		r.holdSweepLock(t)
	case "releaseSweepLock":
		if r.holder == nil {
			r.fatalf(t, "no sweep lock is held")
		}
		err = r.holder.Rollback(ctx)
		r.holder = nil
	case "snapshot":
		err = r.store.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
			var err error
			entries, err = tx.Snapshot(ctx, r.commitID(t, st.Commit))
			return err
		})
	case "materialize":
		tree, err = eng.Materialize(ctx, r.commitID(t, st.Commit))
	case "compose":
		tree, err = eng.Compose(ctx, r.refID(t, st.Ref))
	case "diff":
		changes, err = eng.Diff(ctx, r.commitID(t, st.From), r.commitID(t, st.To))
	case "history":
		history, err = eng.History(ctx, r.refID(t, st.Ref))
	case "discard":
		err = eng.Discard(ctx, actor, r.refID(t, st.Ref), r.version(t, st, st.Ref))
	case "rows":
		err = r.store.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
			var err error
			rows, err = tx.Rows(ctx, st.Kind, r.refID(t, st.Ref))
			return err
		})
	case "patches":
		err = r.store.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
			var err error
			patches, err = tx.Patches(ctx, []string{r.commitID(t, st.Commit)})
			return err
		})
	case "sql":
		args := make([]any, len(st.Args))
		for i, arg := range st.Args {
			args[i] = r.sqlArg(t, arg)
		}
		if st.Expect.Rows != nil {
			rows, err = r.queryRows(ctx, st.Statement, args)
		} else {
			_, err = r.conn.Exec(ctx, st.Statement, args...)
		}
	default:
		r.fatalf(t, "unknown op %q", st.Op)
	}

	if st.Expect.Error != "" {
		if err == nil {
			r.fatalf(t, "succeeded, want error %s", st.Expect.Error)
		}
		if code := engine.ErrorCode(err); code != st.Expect.Error {
			r.fatalf(t, "error %q (%v), want %s", code, err, st.Expect.Error)
		}
		return
	}
	if err != nil {
		r.fatalf(t, "%v", err)
	}
	if ref != nil {
		r.trackRef(*ref)
	}
	if commit != nil && st.As != "" && st.Op != "createPrimary" && st.Op != "branch" {
		r.commits[st.As] = *commit
	}

	x := st.Expect
	if x.Ref != nil {
		if ref == nil {
			r.fatalf(t, "expects a ref, and %s returns none", st.Op)
		}
		r.checkRef(t, *ref, *x.Ref)
	}
	if len(x.Commit) > 0 {
		if !commitAware {
			r.fatalf(t, "expects a commit, and %s returns none", st.Op)
		}
		r.checkCommit(t, commit, x.Commit)
	}
	if x.Saved != nil {
		r.checkTree(t, "saved", saved, x.Saved)
	}
	if x.Tree != nil || x.ContentHash != "" || x.ContentHashOf != "" || x.Findings != nil {
		if tree == nil {
			r.fatalf(t, "expects a tree, and %s returns none", st.Op)
		}
		if x.Tree != nil {
			r.checkTree(t, "tree", tree.Tree, x.Tree)
		}
		r.checkHash(t, tree.ContentHash, x.ContentHash, x.ContentHashOf)
		if x.Findings != nil {
			r.checkList(t, "findings", toRows(t, tree.Findings), *x.Findings)
		}
	}
	if x.Conflicts != nil {
		r.checkList(t, "conflicts", toRows(t, conflicts), *x.Conflicts)
	} else if len(conflicts) > 0 {
		r.fatalf(t, "merge left conflicts %s, and the step expects none", mustJSON(t, conflicts))
	}
	if x.Changes != nil {
		r.checkList(t, "changes", toRows(t, changes), *x.Changes)
	}
	if x.Commits != nil {
		var got []string
		for _, c := range history {
			got = append(got, r.commitName(c.ID))
		}
		if !reflect.DeepEqual(got, *x.Commits) && !(len(got) == 0 && len(*x.Commits) == 0) {
			r.fatalf(t, "history %q, want %q", got, *x.Commits)
		}
	}
	if x.Rows != nil {
		if st.Op != "sql" {
			sortRows(t, rows, "entity_key")
		}
		r.checkList(t, "rows", rows, *x.Rows)
	}
	if x.Snapshot != nil {
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Kind != entries[j].Kind {
				return entries[i].Kind < entries[j].Kind
			}
			return entries[i].EntityKey < entries[j].EntityKey
		})
		var got []json.RawMessage
		for _, e := range entries {
			got = append(got, mustJSON(t, map[string]any{"kind": e.Kind, "entityKey": e.EntityKey, "entityVersion": e.EntityVersion}))
		}
		r.checkList(t, "snapshot", got, *x.Snapshot)
	}
	if x.Release != nil {
		if release == nil {
			r.fatalf(t, "expects a release, and %s returns none", st.Op)
		}
		if got := r.commitName(release.Commit); got != x.Release.Commit {
			r.fatalf(t, "the release names commit %q, want %q", got, x.Release.Commit)
		}
		if x.Release.Version != nil && release.Version != *x.Release.Version {
			r.fatalf(t, "release version %d, want %d", release.Version, *x.Release.Version)
		}
	}
	if x.Report != nil {
		if report == nil {
			r.fatalf(t, "expects a report, and %s returns none", st.Op)
		}
		r.checkList(t, "report", []json.RawMessage{mustJSON(t, report)}, []partialRow{x.Report})
	}
	if x.Patches != nil {
		sort.Slice(patches, func(i, j int) bool {
			if patches[i].Kind != patches[j].Kind {
				return patches[i].Kind < patches[j].Kind
			}
			return patches[i].EntityKey < patches[j].EntityKey
		})
		var got []json.RawMessage
		for _, p := range patches {
			got = append(got, mustJSON(t, map[string]any{"kind": p.Kind, "entityKey": p.EntityKey, "operation": p.Operation, "entityVersion": p.EntityVersion}))
		}
		r.checkList(t, "patches", got, *x.Patches)
	}
}

// queryRows runs an sql step's statement and returns its rows in the order
// the statement returns them, each a JSON object of its columns. Every
// column is read as text, so the statement casts what it selects.
func (r *runner) queryRows(ctx context.Context, statement string, args []any) ([]json.RawMessage, error) {
	result, err := r.conn.Query(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	var out []json.RawMessage
	for result.Next() {
		fields := result.FieldDescriptions()
		texts := make([]*string, len(fields))
		dest := make([]any, len(fields))
		for i := range texts {
			dest[i] = &texts[i]
		}
		if err := result.Scan(dest...); err != nil {
			return nil, fmt.Errorf("read a row as text (cast every column in the statement): %w", err)
		}
		row := map[string]*string{}
		for i, field := range fields {
			row[field.Name] = texts[i]
		}
		text, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	return out, result.Err()
}

// holdSweepLock takes the graph's sweep lock through the adapter in a
// transaction of another connection, and keeps it open until
// releaseSweepLock.
func (r *runner) holdSweepLock(t *testing.T) {
	t.Helper()
	if r.holder != nil {
		r.fatalf(t, "the sweep lock is already held")
	}
	other, err := pgx.ConnectConfig(r.ctx, r.config)
	if err != nil {
		r.fatalf(t, "connect: %v", err)
	}
	t.Cleanup(func() { _ = other.Close(context.Background()) })
	holder, err := other.Begin(r.ctx)
	if err != nil {
		r.fatalf(t, "begin: %v", err)
	}
	r.holder = holder
	locked := false
	err = r.adapter.Storage(postgres.Pgx(holder)).Transact(r.ctx, func(ctx context.Context, tx storage.Tx) error {
		var err error
		locked, err = tx.SweepLock(ctx)
		return err
	})
	if err != nil || !locked {
		r.fatalf(t, "take the sweep lock: %v (locked %t)", err, locked)
	}
}

func (r *runner) sqlArg(t *testing.T, arg sqlArg) string {
	t.Helper()
	var id string
	switch {
	case arg.UUID != "":
		id = arg.UUID
	case arg.Ref != "":
		id = r.refID(t, arg.Ref)
	case arg.Commit != "":
		id = r.commitID(t, arg.Commit)
	default:
		r.fatalf(t, "an sql argument names a uuid, a ref or a commit")
	}
	return hyphenated(t, id)
}

// commitName is the name an earlier step bound a commit to, or its id.
func (r *runner) commitName(id string) string {
	for name, c := range r.commits {
		if c.ID == id {
			return name
		}
	}
	return "id:" + id
}

func (r *runner) refName(id string) string {
	for name, ref := range r.refs {
		if ref.ID == id {
			return name
		}
	}
	return "id:" + id
}

// checkName compares an id with an expectation that names a ref or a
// commit, or is null for none.
func (r *runner) checkName(t *testing.T, what, id string, want json.RawMessage, name func(string) string) {
	t.Helper()
	if len(want) == 0 {
		return
	}
	if string(want) == "null" {
		if id != "" {
			r.fatalf(t, "%s is %s, want none", what, name(id))
		}
		return
	}
	var wantName string
	if err := json.Unmarshal(want, &wantName); err != nil {
		r.fatalf(t, "%s expectation %s: %v", what, want, err)
	}
	if id == "" || name(id) != wantName {
		r.fatalf(t, "%s is %q, want %q", what, name(id), wantName)
	}
}

func (r *runner) checkRef(t *testing.T, got storage.Ref, want refExpect) {
	t.Helper()
	if want.Version != nil && got.Version != *want.Version {
		r.fatalf(t, "ref version %d, want %d", got.Version, *want.Version)
	}
	if want.Sealed != nil && got.Sealed != *want.Sealed {
		r.fatalf(t, "ref sealed %t, want %t", got.Sealed, *want.Sealed)
	}
	if want.Name != nil && got.Name != *want.Name {
		r.fatalf(t, "ref name %q, want %q", got.Name, *want.Name)
	}
	r.checkName(t, "the ref's parent", got.Parent, want.Parent, r.refName)
	r.checkName(t, "the ref's base", got.Base, want.Base, r.commitName)
	r.checkName(t, "the ref's head", got.Head, want.Head, r.commitName)
}

func (r *runner) checkCommit(t *testing.T, got *storage.Commit, raw json.RawMessage) {
	t.Helper()
	if string(raw) == "null" {
		if got != nil {
			r.fatalf(t, "wrote commit %s, want none", got.ID)
		}
		return
	}
	if got == nil {
		r.fatalf(t, "wrote no commit, want one")
	}
	var want commitExpect
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&want); err != nil {
		r.fatalf(t, "commit expectation: %v", err)
	}
	if want.Ref != nil && r.refName(got.Ref) != *want.Ref {
		r.fatalf(t, "commit ref %q, want %q", r.refName(got.Ref), *want.Ref)
	}
	r.checkName(t, "the commit's parent", got.Parent, want.Parent, r.commitName)
	if want.Message != nil && got.Message != *want.Message {
		r.fatalf(t, "commit message %q, want %q", got.Message, *want.Message)
	}
	if len(want.Sequence) > 0 {
		gotSequence := "null"
		if got.Sequence != nil {
			gotSequence = fmt.Sprint(*got.Sequence)
		}
		if gotSequence != string(want.Sequence) {
			r.fatalf(t, "commit sequence %s, want %s", gotSequence, want.Sequence)
		}
	}
	if want.SchemaEpoch != nil && got.SchemaEpoch != *want.SchemaEpoch {
		r.fatalf(t, "commit schema epoch %d, want %d", got.SchemaEpoch, *want.SchemaEpoch)
	}
	r.checkHash(t, got.ContentHash, want.ContentHash, want.ContentHashOf)
}

func (r *runner) checkHash(t *testing.T, got, want, wantOf string) {
	t.Helper()
	if want != "" && got != want {
		r.fatalf(t, "content hash %s, want %s", got, want)
	}
	if wantOf != "" {
		commit, ok := r.commits[wantOf]
		if !ok {
			r.fatalf(t, "no commit is named %q", wantOf)
		}
		if got != commit.ContentHash {
			r.fatalf(t, "content hash %s, want %s's, %s", got, wantOf, commit.ContentHash)
		}
	}
}

// checkTree compares every kind of a tree with the expected rows: the same
// kinds, and per kind the same number of rows in the same order, each with
// the listed columns' values.
func (r *runner) checkTree(t *testing.T, what string, got engine.Tree, want map[string][]partialRow) {
	t.Helper()
	for kind := range got {
		if _, ok := want[kind]; !ok {
			r.fatalf(t, "%s has %s rows %s, and the step expects none", what, kind, mustJSON(t, got[kind]))
		}
	}
	for kind, rows := range want {
		r.checkList(t, what+" "+kind, got[kind], rows)
	}
}

// checkList compares a list of JSON objects with expected ones: the same
// length, and each object with the listed members' values.
func (r *runner) checkList(t *testing.T, what string, got []json.RawMessage, want []partialRow) {
	t.Helper()
	if len(got) != len(want) {
		r.fatalf(t, "%s: %d, want %d: %s", what, len(got), len(want), mustJSON(t, got))
	}
	for i := range want {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(got[i], &members); err != nil {
			r.fatalf(t, "%s[%d]: %v", what, i, err)
		}
		for column, value := range want[i] {
			have, ok := members[column]
			if !ok && string(value) == "null" {
				continue
			}
			if !ok || !sameJSON(t, have, value) {
				r.fatalf(t, "%s[%d].%s is %s, want %s (%s)", what, i, column, have, value, got[i])
			}
		}
	}
}

// sameJSON compares two JSON values: numbers by their text, objects by
// their members.
func sameJSON(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	decode := func(raw json.RawMessage) any {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return v
	}
	return reflect.DeepEqual(decode(a), decode(b))
}

func toRows[T any](t *testing.T, values []T) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(values))
	for i, v := range values {
		out[i] = mustJSON(t, v)
	}
	return out
}

func sortRows(t *testing.T, rows []json.RawMessage, column string) {
	t.Helper()
	key := func(raw json.RawMessage) string {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(raw, &members); err != nil {
			t.Fatal(err)
		}
		return string(members[column])
	}
	sort.SliceStable(rows, func(i, j int) bool { return key(rows[i]) < key(rows[j]) })
}

// hyphenated writes a UUID given in its canonical form (base62) as the
// hyphenated text Postgres reads.
func hyphenated(t *testing.T, id string) string {
	t.Helper()
	n := new(big.Int)
	for _, c := range []byte(id) {
		digit := strings.IndexByte(base62Alphabet, c)
		if digit < 0 {
			t.Fatalf("%q is not a UUID in its canonical form", id)
		}
		n.Mul(n, big.NewInt(62))
		n.Add(n, big.NewInt(int64(digit)))
	}
	hex := fmt.Sprintf("%032x", n)
	return hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:32]
}

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// isUniqueViolation reports whether err is Postgres's unique_violation: a
// concurrent CREATE EXTENSION that another session won.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
