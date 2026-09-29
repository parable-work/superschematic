package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVersionGraphShellOnPostgres generates fixture-version-graph-db, whose
// ORM carries the Recipe graph's facade over the version-graph engine
// (versiongraph_recipe.go), then builds, vets and tests the ORM module with
// versionGraphShellTest and versionGraphFacadeTest. The first runs the
// facade against the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL through a whole lifecycle: a
// primary line written only through merges, a tagged merge, two change sets
// merged back (one cleanly, one with a conflict settled by a resolution), a
// parent deleted with its children, a revert, pruned history, an unset
// override, a merge that carries a delete, a member with a plain UUID key, a
// revert that deletes, two taggers made to wait on the root's lock
// together, and the fence, the seal, a missing history row, the walk
// ceiling and the schema epoch refusing what they refuse. The second saves
// every value class through the facade; the third releases, rolls back,
// rebases and sweeps through it.
func TestVersionGraphShellOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the version graph shell against Postgres")
	}
	ormDir := generateVersionGraphModule(t)
	if err := os.WriteFile(filepath.Join(ormDir, "graph_shell_test.go"), []byte(versionGraphShellTest), 0o644); err != nil {
		t.Fatalf("write graph shell test: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "graph_facade_test.go"), []byte(versionGraphFacadeTest), 0o644); err != nil {
		t.Fatalf("write graph facade test: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "graph_release_test.go"), []byte(versionGraphReleaseTest), 0o644); err != nil {
		t.Fatalf("write graph release test: %v", err)
	}
	out := runVersionGraphModule(t, ormDir)
	for _, test := range []string{"TestVersionGraphShellOnPostgres", "TestVersionGraphFacadeKeepsEveryClass", "TestVersionGraphFacadeReleasesRebasesAndSweeps"} {
		if !strings.Contains(out, "--- PASS: "+test) {
			t.Fatalf("the generated ORM module did not run %s", test)
		}
	}
}

// versionGraphFacadeTest saves a typed Tasting, whose columns hold a value
// of every class a descriptor names, through the facade, commits it and
// reads it back from the save and from the commit. Each field comes back
// as the typed value of its canonical form: the same value, and a time of
// day and an instant in the forms the canonical row gives them.
const versionGraphFacadeTest = `package orm

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	types "example.com/schemas/types/go/fixture-version-graph-db"
)

func TestVersionGraphFacadeKeepsEveryClass(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the version graph facade against Postgres")
	}
	db, _ := openShellDatabase(t, dsn)
	ctx := WithUserID(context.Background(), mustShellUUID(t, "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90"))
	g := db.RecipeGraph()
	recipe, err := db.Recipe.CreateOne(ctx, &types.Recipe{Title: "Bread"})
	if err != nil {
		t.Fatalf("create recipe: %v", err)
	}
	main, err := g.CreatePrimary(ctx, *recipe.Id, "main")
	if err != nil {
		t.Fatalf("CreatePrimary: %v", err)
	}
	draft, err := g.Branch(ctx, *main.Id, "tastings")
	if err != nil {
		t.Fatalf("Branch: %v", err)
	}
	parse := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	taster, err := types.ParseIdentityUserID("0e7d4b1a-3c2f-4a6e-8b9d-1f2e3d4c5b6a")
	parse(err)
	tastedOn, err := types.ParseTemporalDate("2026-09-01")
	parse(err)
	tastedAt, err := types.ParseTemporalDateTime("2026-09-01T12:30:00.25+02:00")
	parse(err)
	utc, err := types.ParseTemporalDateTime("2026-09-01T10:30:00.25Z")
	parse(err)
	servedAt, err := types.ParseTemporalTime("18:30")
	parse(err)
	canonicalTime, err := types.ParseTemporalTime("18:30:00")
	parse(err)
	rested, err := types.ParseTemporalDuration("1.5ms")
	parse(err)
	input := &types.Tasting{
		Taster: taster, Salty: true, Score: 4.5, Servings: 9007199254740993,
		TastedOn: tastedOn, TastedAt: tastedAt, ServedAt: servedAt, Rested: rested,
		Verdict: types.Verdict_Tweak, Remarks: types.GenericJSON(` + "`" + `{"crumb": "open", "crust": [1, 2.5]}` + "`" + `),
		Tags: []string{"sour", "a \"quoted\" tag"}, Helpers: []types.IdentityUUID{mustShellUUID(t, "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90")},
		Bites: [][]types.GenericInt64{{1, 2}, {3}},
	}
	want := *input
	want.TastedAt, want.ServedAt = utc, canonicalTime

	saved, err := g.Save(ctx, *draft.Id, draft.Version, RecipeEdits{Tasting: GraphEdits[types.Tasting]{Upsert: []*types.Tasting{input}}})
	if err != nil {
		t.Fatalf("save a tasting: %v", err)
	}
	committed, err := g.Commit(ctx, *draft.Id, saved.Ref.Version, RecipeCommitOptions{})
	if err != nil {
		t.Fatalf("commit the tasting: %v", err)
	}
	tree, err := g.Materialize(ctx, *committed.Commit.Id)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(saved.Saved.Tasting) != 1 || len(tree.Tasting) != 1 {
		t.Fatalf("saved %d and materialized %d tastings, want one each", len(saved.Saved.Tasting), len(tree.Tasting))
	}
	fields := []string{"taster", "salty", "score", "servings", "tastedOn", "tastedAt", "servedAt", "rested", "verdict", "remarks", "tags", "helpers", "bites"}
	for what, got := range map[string]*types.Tasting{"saved": saved.Saved.Tasting[0], "materialized": tree.Tasting[0]} {
		gotJSON, wantJSON := facadeFields(t, got), facadeFields(t, &want)
		for _, field := range fields {
			if !reflect.DeepEqual(facadeValue(t, gotJSON[field]), facadeValue(t, wantJSON[field])) {
				t.Errorf("%s tasting %s = %s, want %s", what, field, gotJSON[field], wantJSON[field])
			}
		}
		if got.Recipe.Id == nil || *got.Recipe.Id != *recipe.Id || got.EntityKey == nil {
			t.Errorf("%s tasting has recipe %v and entity key %v, want the root and a generated key", what, got.Recipe.Id, got.EntityKey)
		}
	}
}

// facadeValue decodes a JSON value, keeping each number's text.
func facadeValue(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return value
}

func facadeFields(t *testing.T, tasting *types.Tasting) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(tasting)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}
`

// versionGraphReleaseTest drives the operations D19 adds through the typed
// facade: a merge's message and tag, the release pointer and a rollback that
// writes no member rows, Released, a clean and a conflicting Rebase, and a
// Sweep and a RunSweeper pass that write as their configured actor.
const versionGraphReleaseTest = `package orm

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	versiongraph "github.com/parable-work/superschematic/runtime/versiongraph/go"
	types "example.com/schemas/types/go/fixture-version-graph-db"
)

func TestVersionGraphFacadeReleasesRebasesAndSweeps(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the version graph facade against Postgres")
	}
	db, pool := openShellDatabase(t, dsn)
	cook := mustShellUUID(t, "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90")
	janitor := mustShellUUID(t, "3c9a7e21-6b4d-4f8a-9e2c-5d1b7a3f6e08")
	ctx := WithUserID(context.Background(), cook)
	g := db.RecipeGraph()
	recipe, err := db.Recipe.CreateOne(ctx, &types.Recipe{Title: "Bread"})
	if err != nil {
		t.Fatalf("create recipe: %v", err)
	}
	main, err := g.CreatePrimary(ctx, *recipe.Id, "main")
	if err != nil {
		t.Fatalf("CreatePrimary: %v", err)
	}
	mainID := *main.Id
	step := func(position int64, instruction string) *types.Step {
		return &types.Step{Position: types.GenericInt64(position), Instruction: instruction, Timings: types.GenericJSON("{}")}
	}

	// A merge commits with its message and tag.
	one := landShell(t, ctx, g, mainID, main.Version, "one", RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{step(1, "Mix")}}}, RecipeCommitOptions{Message: "v1", Tag: true})
	v1 := one.merged.Commit
	if v1.Sequence == nil || *v1.Sequence != 1 || v1.Message != "v1" {
		t.Fatalf("the tagged merge = %+v, want sequence 1 and message v1", v1)
	}
	mix := one.saved.Saved.Step[0]
	two := landShell(t, ctx, g, mainID, one.merged.Ref.Version, "two", RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{stepEdit(mix, "Mix well", "{}")}}}, RecipeCommitOptions{Tag: true})
	v2 := two.merged.Commit

	// The release pointer names a tagged commit, fenced by its version.
	if _, err := g.Released(ctx, *recipe.Id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Released before any release = %v, want ErrNotFound", err)
	}
	release, err := g.Release(ctx, *recipe.Id, *v2.Id, 0)
	if err != nil {
		t.Fatalf("Release v2: %v", err)
	}
	if *release.Commit.Id != *v2.Id || *release.Root.Id != *recipe.Id || release.Version != 1 {
		t.Fatalf("release = %+v, want v2 of the recipe at version 1", release)
	}
	released, err := g.Released(ctx, *recipe.Id)
	if err != nil {
		t.Fatalf("Released: %v", err)
	}
	if released.Release.Version != 1 || released.Tree.ContentHash != v2.ContentHash || len(released.Tree.Step) != 1 || released.Tree.Step[0].Instruction != "Mix well" {
		t.Fatalf("released = %+v with steps %+v, want v2's tree", released.Release, released.Tree.Step)
	}

	// A rollback moves the pointer and writes no member rows.
	rowState := func() (count, versions int64) {
		t.Helper()
		if err := pool.QueryRow(ctx, "SELECT count(*), COALESCE(sum(_version), 0) FROM step").Scan(&count, &versions); err != nil {
			t.Fatalf("read the step rows: %v", err)
		}
		return count, versions
	}
	beforeCount, beforeVersions := rowState()
	rollback, err := g.Release(ctx, *recipe.Id, *v1.Id, release.Version)
	if err != nil {
		t.Fatalf("roll back to v1: %v", err)
	}
	if afterCount, afterVersions := rowState(); rollback.Version != 2 || afterCount != beforeCount || afterVersions != beforeVersions {
		t.Fatalf("rollback = %+v; step rows went from (%d, %d) to (%d, %d), want no member writes", rollback, beforeCount, beforeVersions, afterCount, afterVersions)
	}
	released, err = g.Released(ctx, *recipe.Id)
	if err != nil {
		t.Fatalf("Released: %v", err)
	}
	if released.Tree.ContentHash != v1.ContentHash {
		t.Fatal("after the rollback Released reads another tree than v1's")
	}
	if _, err := g.Release(ctx, *recipe.Id, *v2.Id, release.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("Release at a stale version = %v, want ErrVersionConflict", err)
	}
	three := landShell(t, ctx, g, mainID, two.merged.Ref.Version, "three", RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{step(2, "Rest")}}}, RecipeCommitOptions{})
	if _, err := g.Release(ctx, *recipe.Id, *three.merged.Commit.Id, rollback.Version); !errors.Is(err, ErrNotTagged) {
		t.Fatalf("Release of an untagged commit = %v, want ErrNotTagged", err)
	}

	// A rebase moves a change set onto its parent's head and commits after
	// its previous head.
	a, err := g.Branch(ctx, mainID, "knead")
	if err != nil {
		t.Fatalf("Branch a: %v", err)
	}
	aSaved, err := g.Save(ctx, *a.Id, a.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{stepEdit(mix, "Mix and knead", "{}")}}})
	if err != nil {
		t.Fatalf("save a: %v", err)
	}
	aCommit, err := g.Commit(ctx, *a.Id, aSaved.Ref.Version, RecipeCommitOptions{})
	if err != nil {
		t.Fatalf("commit a: %v", err)
	}
	four := landShell(t, ctx, g, mainID, three.merged.Ref.Version, "four", RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{step(3, "Bake")}}}, RecipeCommitOptions{})
	rebased, err := g.Rebase(ctx, *a.Id, aCommit.Ref.Version, nil)
	if err != nil {
		t.Fatalf("Rebase a: %v", err)
	}
	if len(rebased.Conflicts) != 0 || rebased.Commit == nil || *rebased.Commit.ParentCommit.Id != *aCommit.Commit.Id ||
		rebased.Ref.BaseCommit == nil || *rebased.Ref.BaseCommit.Id != *four.merged.Commit.Id || *rebased.Ref.HeadCommit.Id != *rebased.Commit.Id {
		t.Fatalf("rebase = %+v, want a commit after a's head and four's merge as a's base", rebased)
	}
	aTree, err := g.Compose(ctx, *a.Id)
	if err != nil {
		t.Fatalf("Compose a: %v", err)
	}
	assertShellTree(t, "a rebased", aTree, []string{"Mix and knead", "Rest", "Bake"}, nil)
	if _, err := g.Rebase(ctx, mainID, four.merged.Ref.Version, nil); !errors.Is(err, ErrNoParent) {
		t.Fatalf("Rebase of the primary line = %v, want ErrNoParent", err)
	}

	// A conflicting rebase returns typed conflicts and writes nothing. A
	// resolution that keeps the change set's side settles it: the base moves,
	// and with the tree its head already holds there is nothing to commit.
	c, err := g.Branch(ctx, mainID, "by hand")
	if err != nil {
		t.Fatalf("Branch c: %v", err)
	}
	cSaved, err := g.Save(ctx, *c.Id, c.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{stepEdit(mix, "Mix by hand", "{}")}}})
	if err != nil {
		t.Fatalf("save c: %v", err)
	}
	cCommit, err := g.Commit(ctx, *c.Id, cSaved.Ref.Version, RecipeCommitOptions{})
	if err != nil {
		t.Fatalf("commit c: %v", err)
	}
	five := landShell(t, ctx, g, mainID, four.merged.Ref.Version, "five", RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{stepEdit(mix, "Mix fast", "{}")}}}, RecipeCommitOptions{})
	// Main's commits are snapshotted at the graph's interval: three is one
	// past the tagged two, and five, three past it, is snapshotted.
	snapshotEntries := func(commit types.IdentityUUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM recipe_snapshot_entry WHERE commit_id = $1", commit.ToUUID()).Scan(&n); err != nil {
			t.Fatalf("count snapshot entries: %v", err)
		}
		return n
	}
	if RecipeGraphSnapshotEvery != 3 || snapshotEntries(*three.merged.Commit.Id) != 0 || snapshotEntries(*five.merged.Commit.Id) == 0 {
		t.Fatalf("snapshot entries of three %d and five %d at interval %d, want none and some at 3",
			snapshotEntries(*three.merged.Commit.Id), snapshotEntries(*five.merged.Commit.Id), RecipeGraphSnapshotEvery)
	}
	conflicted, err := g.Rebase(ctx, *c.Id, cCommit.Ref.Version, nil)
	if err != nil {
		t.Fatalf("Rebase c: %v", err)
	}
	if len(conflicted.Conflicts) != 1 || conflicted.Commit != nil || conflicted.Ref.Version != cCommit.Ref.Version ||
		conflicted.Conflicts[0].Kind != types.RecipeEntityKind_Step || conflicted.Conflicts[0].EntityKey != *mix.EntityKey || conflicted.Conflicts[0].Path != "/instruction" {
		t.Fatalf("conflicting rebase = %+v, want the mixing step's instruction and no write", conflicted)
	}
	settled, err := g.Rebase(ctx, *c.Id, cCommit.Ref.Version, []RecipeResolution{{
		Kind: types.RecipeEntityKind_Step, EntityKey: *mix.EntityKey, Path: "/instruction", Take: versiongraph.TakeOurs,
	}})
	if err != nil {
		t.Fatalf("Rebase c with a resolution: %v", err)
	}
	cTree, err := g.Compose(ctx, *c.Id)
	if err != nil {
		t.Fatalf("Compose c: %v", err)
	}
	if len(settled.Conflicts) != 0 || settled.Commit != nil || *settled.Ref.BaseCommit.Id != *five.merged.Commit.Id || *settled.Ref.HeadCommit.Id != *cCommit.Commit.Id {
		t.Fatalf("settled rebase = %+v, want five's merge as c's base and c's head kept", settled)
	}
	assertShellTree(t, "c rebased", cTree, []string{"Mix by hand", "Rest", "Bake"}, nil)

	// A sweep writes as its configured actor: the rows it collects from a
	// discarded change set record it.
	if _, err := g.Sweep(ctx, GraphSweepOptions{}); !errors.Is(err, ErrNoActor) {
		t.Fatalf("Sweep with no actor = %v, want ErrNoActor", err)
	}
	x, err := g.Branch(ctx, mainID, "dropped")
	if err != nil {
		t.Fatalf("Branch x: %v", err)
	}
	xSaved, err := g.Save(ctx, *x.Id, x.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{step(9, "Garnish")}}})
	if err != nil {
		t.Fatalf("save x: %v", err)
	}
	if err := g.Discard(ctx, *x.Id, xSaved.Ref.Version); err != nil {
		t.Fatalf("Discard x: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE recipe_ref SET deleted_at = now() - interval '8 days' WHERE id = $1", x.Id.ToUUID()); err != nil {
		t.Fatalf("age the discard: %v", err)
	}
	report, err := g.Sweep(context.Background(), GraphSweepOptions{Actor: janitor})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if report.Skipped || report.CollectedRefs != 1 || report.CollectedRows["step"] != 1 {
		t.Fatalf("sweep report = %+v, want x's one step row collected", report)
	}
	var operation, actor string
	if err := pool.QueryRow(ctx, "SELECT operation, data->>'updated_by' FROM step_history WHERE id = $1 ORDER BY _version DESC LIMIT 1", xSaved.Saved.Step[0].Id.ToUUID()).Scan(&operation, &actor); err != nil {
		t.Fatalf("read the collected row's history: %v", err)
	}
	if operation != "DELETE" || actor != janitor.ToUUID().String() {
		t.Fatalf("the collected row's last history row = (%s, %s), want (DELETE, %s)", operation, actor, janitor.ToUUID())
	}

	// A change set the sweep abandons records the sweep's actor as its
	// discarder.
	idle, err := g.Branch(ctx, mainID, "idle")
	if err != nil {
		t.Fatalf("Branch idle: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE recipe_ref SET updated_at = now() - interval '3 days' WHERE id = $1", idle.Id.ToUUID()); err != nil {
		t.Fatalf("age the idle change set: %v", err)
	}
	report, err = g.Sweep(context.Background(), GraphSweepOptions{Actor: janitor, AbandonAfter: 48 * time.Hour})
	if err != nil {
		t.Fatalf("Sweep with AbandonAfter: %v", err)
	}
	var discarder string
	if err := pool.QueryRow(ctx, "SELECT deleted_by::text FROM recipe_ref WHERE id = $1", idle.Id.ToUUID()).Scan(&discarder); err != nil {
		t.Fatalf("read the abandoned change set: %v", err)
	}
	if report.Abandoned != 1 || discarder != janitor.ToUUID().String() {
		t.Fatalf("abandon pass = %+v with discarder %s, want one change set discarded by %s", report, discarder, janitor.ToUUID())
	}

	// The sweeper runs a pass at once and stops with its context.
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	passes := 0
	err = g.RunSweeper(runCtx, time.Hour, GraphSweepOptions{Actor: janitor}, func(report *GraphSweepReport, err error) {
		passes++
		if err != nil || report == nil || report.Skipped {
			t.Errorf("sweeper pass = %+v, %v; want a pass that swept", report, err)
		}
		cancel()
	})
	if !errors.Is(err, context.Canceled) || passes != 1 {
		t.Fatalf("RunSweeper = %v after %d passes, want context.Canceled after one", err, passes)
	}
}
`

const versionGraphShellTest = `package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	versiongraph "github.com/parable-work/superschematic/runtime/versiongraph/go"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/canonical"
	types "example.com/schemas/types/go/fixture-version-graph-db"
)

func TestVersionGraphShellOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the version graph shell against Postgres")
	}
	db, pool := openShellDatabase(t, dsn)
	cook := mustShellUUID(t, "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90")
	editor := mustShellUUID(t, "0e7d4b1a-3c2f-4a6e-8b9d-1f2e3d4c5b6a")
	ctx := WithUserID(context.Background(), cook)
	editorCtx := WithUserID(context.Background(), editor)
	g := db.RecipeGraph()

	recipe, err := db.Recipe.CreateOne(ctx, &types.Recipe{Title: "Bread"})
	if err != nil {
		t.Fatalf("create recipe: %v", err)
	}
	if _, err := g.CreatePrimary(context.Background(), *recipe.Id, "main"); !errors.Is(err, ErrNoUserInContext) {
		t.Fatalf("CreatePrimary without a user = %v, want ErrNoUserInContext", err)
	}

	// A primary line takes writes only from a merge: a first change set's
	// steps and ingredients land on it as a tagged merge.
	main, err := g.CreatePrimary(ctx, *recipe.Id, "main")
	if err != nil {
		t.Fatalf("CreatePrimary: %v", err)
	}
	mainID := *main.Id
	if main.ParentRef != nil || main.HeadCommit != nil {
		t.Fatalf("a primary line has no parent and no head: %+v", main)
	}
	if _, err := g.Save(ctx, mainID, main.Version, RecipeEdits{}); !errors.Is(err, ErrPrimaryMergeOnly) {
		t.Fatalf("Save on the primary line = %v, want ErrPrimaryMergeOnly", err)
	}
	draft, err := g.Branch(ctx, mainID, "first draft")
	if err != nil {
		t.Fatalf("Branch the first draft: %v", err)
	}
	saved, err := g.Save(ctx, *draft.Id, draft.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{
		{Position: 1, Instruction: "Mix", Timings: types.GenericJSON(` + "`" + `{"knead": 10, "rest": 30}` + "`" + `)},
		{Position: 2, Instruction: "Bake", Timings: types.GenericJSON(` + "`" + `{"oven": 40}` + "`" + `)},
	}}})
	if err != nil {
		t.Fatalf("save steps: %v", err)
	}
	if saved.Ref.Version != draft.Version+1 {
		t.Fatalf("Save moved the ref to version %d, want %d", saved.Ref.Version, draft.Version+1)
	}
	mix, bake := saved.Saved.Step[0], saved.Saved.Step[1]
	if mix.EntityKey == nil || bake.EntityKey == nil || *mix.EntityKey == *bake.EntityKey {
		t.Fatalf("new steps need generated entity keys of their own: %v, %v", mix.EntityKey, bake.EntityKey)
	}
	saved, err = g.Save(ctx, *draft.Id, saved.Ref.Version, RecipeEdits{Ingredient: GraphEdits[types.Ingredient]{Upsert: []*types.Ingredient{
		{StepKey: *mix.EntityKey, Quantity: "200g flour", Substitutes: types.GenericJSON(` + "`" + `{"type": "object"}` + "`" + `)},
		{StepKey: *bake.EntityKey, Quantity: "1 egg wash", Substitutes: types.GenericJSON(` + "`" + `{"type": "object"}` + "`" + `)},
	}}})
	if err != nil {
		t.Fatalf("save ingredients: %v", err)
	}
	flour, wash := saved.Saved.Ingredient[0], saved.Saved.Ingredient[1]
	draftCommit, err := g.Commit(ctx, *draft.Id, saved.Ref.Version, RecipeCommitOptions{Message: "first draft"})
	if err != nil {
		t.Fatalf("Commit the first draft: %v", err)
	}
	if _, err := g.Commit(ctx, *draft.Id, draftCommit.Ref.Version, RecipeCommitOptions{}); !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("a second commit with no change = %v, want ErrNothingToCommit", err)
	}

	// A tagged merge is published version 1.
	first, err := g.Merge(ctx, *draft.Id, mainID, main.Version, nil, RecipeCommitOptions{Message: "first", Tag: true})
	if err != nil {
		t.Fatalf("Merge the first draft: %v", err)
	}
	c1 := first.Commit
	if c1 == nil || c1.Sequence == nil || *c1.Sequence != 1 || c1.SchemaEpoch != RecipeGraphSchemaEpoch || c1.Message != "first" ||
		c1.ParentCommit != nil || c1.ContentHash != draftCommit.Commit.ContentHash {
		t.Fatalf("first merge commit = %+v, want sequence 1 at epoch %d with no parent and the draft's content", c1, RecipeGraphSchemaEpoch)
	}
	if first.Ref.HeadCommit == nil || *first.Ref.HeadCommit.Id != *c1.Id {
		t.Fatalf("the ref's head is %+v, want the commit", first.Ref.HeadCommit)
	}
	if _, err := g.Commit(ctx, mainID, first.Ref.Version, RecipeCommitOptions{}); !errors.Is(err, ErrPrimaryMergeOnly) {
		t.Fatalf("Commit on the primary line = %v, want ErrPrimaryMergeOnly", err)
	}
	tree1, err := g.Materialize(ctx, *c1.Id)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	assertShellTree(t, "commit 1", tree1, []string{"Mix", "Bake"}, []string{"200g flour", "1 egg wash"})
	if tree1.ContentHash != c1.ContentHash {
		t.Fatalf("commit 1 materializes to hash %s, recorded %s", tree1.ContentHash, c1.ContentHash)
	}

	// Two change sets branch from the published head.
	a, err := g.Branch(ctx, mainID, "knead more")
	if err != nil {
		t.Fatalf("Branch a: %v", err)
	}
	b, err := g.Branch(ctx, mainID, "rest longer")
	if err != nil {
		t.Fatalf("Branch b: %v", err)
	}
	if a.BaseCommit == nil || *a.BaseCommit.Id != *c1.Id || a.ParentRef == nil || *a.ParentRef.Id != mainID {
		t.Fatalf("change set a = %+v, want main's head as its base", a)
	}
	// a changes one key of the keyed timings and the flour; b another key
	// of the timings and the flour again.
	aSaved, err := g.Save(ctx, *a.Id, a.Version, RecipeEdits{
		Step:       GraphEdits[types.Step]{Upsert: []*types.Step{stepEdit(mix, "Mix", ` + "`" + `{"knead": 12, "rest": 30}` + "`" + `)}},
		Ingredient: GraphEdits[types.Ingredient]{Upsert: []*types.Ingredient{ingredientEdit(flour, "250g flour")}},
	})
	if err != nil {
		t.Fatalf("save a: %v", err)
	}
	if *aSaved.Saved.Step[0].Id == *mix.Id || *aSaved.Saved.Step[0].EntityKey != *mix.EntityKey {
		t.Fatal("a change set's edit must be a row of its own for the same entity")
	}
	aCommit, err := g.Commit(ctx, *a.Id, aSaved.Ref.Version, RecipeCommitOptions{Message: "knead more"})
	if err != nil {
		t.Fatalf("commit a: %v", err)
	}
	if aCommit.Commit.Sequence != nil {
		t.Fatalf("an untagged commit has sequence %d, want none", *aCommit.Commit.Sequence)
	}
	if aCommit.Commit.ParentCommit == nil || *aCommit.Commit.ParentCommit.Id != *c1.Id {
		t.Fatal("a change set's first commit must have its base as its parent")
	}
	bSaved, err := g.Save(ctx, *b.Id, b.Version, RecipeEdits{
		Step:       GraphEdits[types.Step]{Upsert: []*types.Step{stepEdit(mix, "Mix", ` + "`" + `{"knead": 10, "rest": 45}` + "`" + `)}},
		Ingredient: GraphEdits[types.Ingredient]{Upsert: []*types.Ingredient{ingredientEdit(flour, "300g flour")}},
	})
	if err != nil {
		t.Fatalf("save b: %v", err)
	}
	bCommit, err := g.Commit(ctx, *b.Id, bSaved.Ref.Version, RecipeCommitOptions{Message: "rest longer"})
	if err != nil {
		t.Fatalf("commit b: %v", err)
	}
	mainTree, err := g.Compose(ctx, mainID)
	if err != nil {
		t.Fatalf("Compose main: %v", err)
	}
	if mainTree.ContentHash != c1.ContentHash {
		t.Fatal("edits on change sets must not reach main before a merge")
	}

	// The first merge is clean.
	mergedA, err := g.Merge(ctx, *a.Id, mainID, first.Ref.Version, nil, RecipeCommitOptions{})
	if err != nil {
		t.Fatalf("Merge a: %v", err)
	}
	if len(mergedA.Conflicts) != 0 || mergedA.Commit == nil || *mergedA.Commit.ParentCommit.Id != *c1.Id {
		t.Fatalf("merge a = %+v, want a clean merge commit on top of commit 1", mergedA)
	}
	mainTree, err = g.Compose(ctx, mainID)
	if err != nil {
		t.Fatalf("Compose main: %v", err)
	}
	assertShellTree(t, "main after a", mainTree, []string{"Mix", "Bake"}, []string{"250g flour", "1 egg wash"})
	assertShellJSON(t, "timings after a", mainTree.Step[0].Timings, ` + "`" + `{"knead": 12, "rest": 30}` + "`" + `)

	// The second conflicts on the flour and writes nothing.
	conflicted, err := g.Merge(ctx, *b.Id, mainID, mergedA.Ref.Version, nil, RecipeCommitOptions{})
	if err != nil {
		t.Fatalf("Merge b: %v", err)
	}
	if len(conflicted.Conflicts) != 1 || conflicted.Commit != nil {
		t.Fatalf("merge b = %+v, want one conflict and no commit", conflicted)
	}
	conflict := conflicted.Conflicts[0]
	if conflict.Kind != types.RecipeEntityKind_Ingredient || conflict.EntityKey != *flour.EntityKey || conflict.Path != "/quantity" {
		t.Fatalf("conflict = %+v, want the flour's quantity", conflict)
	}
	assertShellJSON(t, "conflict base", conflict.Base, ` + "`" + `"200g flour"` + "`" + `)
	assertShellJSON(t, "conflict ours", conflict.Ours, ` + "`" + `"250g flour"` + "`" + `)
	assertShellJSON(t, "conflict theirs", conflict.Theirs, ` + "`" + `"300g flour"` + "`" + `)
	if conflicted.Ref.Version != mergedA.Ref.Version {
		t.Fatalf("a conflicted merge moved main to version %d", conflicted.Ref.Version)
	}
	unchanged, err := g.Compose(ctx, mainID)
	if err != nil {
		t.Fatalf("Compose main: %v", err)
	}
	if unchanged.ContentHash != mainTree.ContentHash {
		t.Fatal("a conflicted merge changed main")
	}

	// A resolution settles it, and the timings keep both keys' edits.
	mergedB, err := g.Merge(ctx, *b.Id, mainID, mergedA.Ref.Version, []RecipeResolution{{
		Kind: types.RecipeEntityKind_Ingredient, EntityKey: *flour.EntityKey, Path: "/quantity", Take: versiongraph.TakeTheirs,
	}}, RecipeCommitOptions{})
	if err != nil {
		t.Fatalf("Merge b with a resolution: %v", err)
	}
	if len(mergedB.Conflicts) != 0 || mergedB.Commit == nil {
		t.Fatalf("merge b with a resolution = %+v, want a merge commit", mergedB)
	}
	mainTree, err = g.Compose(ctx, mainID)
	if err != nil {
		t.Fatalf("Compose main: %v", err)
	}
	assertShellTree(t, "main after b", mainTree, []string{"Mix", "Bake"}, []string{"300g flour", "1 egg wash"})
	assertShellJSON(t, "timings after b", mainTree.Step[0].Timings, ` + "`" + `{"knead": 12, "rest": 45}` + "`" + `)
	changes, err := g.Diff(ctx, *c1.Id, *mergedB.Commit.Id)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(changes) != 2 || changes[0].Kind != types.RecipeEntityKind_Ingredient || changes[1].Kind != types.RecipeEntityKind_Step ||
		changes[0].Operation != types.RecipePatchOperation_Update || changes[1].Operation != types.RecipePatchOperation_Update {
		t.Fatalf("diff from commit 1 to merge b = %+v, want an UPDATE of the flour and of the mixing step", changes)
	}

	// Deleting a parent removes its children from compose and materialize.
	nb, err := g.Branch(ctx, mainID, "no baking")
	if err != nil {
		t.Fatalf("Branch no baking: %v", err)
	}
	deleted, err := g.Save(ctx, *nb.Id, nb.Version, RecipeEdits{Step: GraphEdits[types.Step]{Delete: []types.IdentityUUID{*bake.EntityKey}}})
	if err != nil {
		t.Fatalf("delete bake: %v", err)
	}
	nbTree, err := g.Compose(ctx, *nb.Id)
	if err != nil {
		t.Fatalf("Compose no baking: %v", err)
	}
	assertShellTree(t, "change set without bake", nbTree, []string{"Mix"}, []string{"300g flour"})
	nbCommit, err := g.Commit(ctx, *nb.Id, deleted.Ref.Version, RecipeCommitOptions{Message: "no baking"})
	if err != nil {
		t.Fatalf("commit the delete: %v", err)
	}
	// The step's DELETE pins the change set's tombstone; the egg wash's,
	// removed with its step, pins its last committed row, main's.
	pins := shellPatches(t, pool, *nbCommit.Commit.Id)
	bakeID, bakeVersion := shellRow(t, pool, "step", *bake.EntityKey, *nb.Id)
	washID, washVersion := shellRow(t, pool, "ingredient", *wash.EntityKey, mainID)
	if got := pins["step"]; len(pins) != 2 || got != [3]any{"DELETE", bakeID, bakeVersion} {
		t.Fatalf("step patch = %v, want a DELETE pinned to bake's tombstone on the change set", got)
	}
	if got := pins["ingredient"]; got != [3]any{"DELETE", washID, washVersion} {
		t.Fatalf("ingredient patch = %v, want a DELETE pinned to main's egg wash row", got)
	}
	noBake, err := g.Merge(ctx, *nb.Id, mainID, mergedB.Ref.Version, nil, RecipeCommitOptions{Message: "no baking"})
	if err != nil {
		t.Fatalf("Merge no baking: %v", err)
	}
	if noBake.Commit == nil || noBake.Commit.Message != "no baking" || noBake.Commit.ContentHash != nbCommit.Commit.ContentHash {
		t.Fatalf("merge of no baking = %+v, want a commit with its message and the change set's content", noBake.Commit)
	}
	mainTree, err = g.Compose(ctx, mainID)
	if err != nil {
		t.Fatalf("Compose main: %v", err)
	}
	assertShellTree(t, "main without bake", mainTree, []string{"Mix"}, []string{"300g flour"})
	noBakeTree, err := g.Materialize(ctx, *noBake.Commit.Id)
	if err != nil {
		t.Fatalf("Materialize the delete: %v", err)
	}
	assertShellTree(t, "commit without bake", noBakeTree, []string{"Mix"}, []string{"300g flour"})

	// Reverting to published version 1, on a change set merged back,
	// composes and hashes as it did.
	if _, err := g.Revert(ctx, mainID, noBake.Ref.Version, *c1.Id); !errors.Is(err, ErrPrimaryMergeOnly) {
		t.Fatalf("Revert on the primary line = %v, want ErrPrimaryMergeOnly", err)
	}
	back, err := g.Branch(ctx, mainID, "back to the first")
	if err != nil {
		t.Fatalf("Branch back: %v", err)
	}
	if _, err := g.Revert(ctx, *back.Id, back.Version, *c1.Id); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	reverted, err := g.Merge(ctx, *back.Id, mainID, noBake.Ref.Version, nil, RecipeCommitOptions{})
	if err != nil {
		t.Fatalf("Merge the revert: %v", err)
	}
	if reverted.Commit == nil || reverted.Commit.ContentHash != c1.ContentHash {
		t.Fatalf("revert commit = %+v, want the content hash of commit 1", reverted.Commit)
	}
	mainTree, err = g.Compose(ctx, mainID)
	if err != nil {
		t.Fatalf("Compose main: %v", err)
	}
	assertShellTree(t, "main reverted", mainTree, []string{"Mix", "Bake"}, []string{"200g flour", "1 egg wash"})
	history, err := g.History(ctx, mainID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	wantHistory := []types.IdentityUUID{*reverted.Commit.Id, *noBake.Commit.Id, *mergedB.Commit.Id, *mergedA.Commit.Id, *c1.Id}
	if len(history) != len(wantHistory) {
		t.Fatalf("main has %d commits, want %d", len(history), len(wantHistory))
	}
	for i, commit := range history {
		if *commit.Id != wantHistory[i] {
			t.Fatalf("history[%d] = %v, want %v", i, *commit.Id, wantHistory[i])
		}
	}

	// Pruning with a retention every row is past keeps the rows commits
	// pin, so every commit still materializes to the hash it recorded. An
	// edit that no commit took is pruned.
	churnRef, err := g.Branch(ctx, mainID, "churn")
	if err != nil {
		t.Fatalf("Branch churn: %v", err)
	}
	churn, err := g.Save(ctx, *churnRef.Id, churnRef.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{stepEdit(mix, "Mix well", ` + "`" + `{"knead": 10, "rest": 30}` + "`" + `)}}})
	if err != nil {
		t.Fatalf("save churn: %v", err)
	}
	if _, err := g.Save(ctx, *churnRef.Id, churn.Ref.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{stepEdit(mix, "Mix gently", ` + "`" + `{"knead": 10, "rest": 30}` + "`" + `)}}}); err != nil {
		t.Fatalf("save churn: %v", err)
	}
	for _, table := range []string{"step_history", "ingredient_history"} {
		if _, err := pool.Exec(ctx, "UPDATE "+table+" SET recorded_at = now() - interval '400 days'"); err != nil {
			t.Fatalf("age %s: %v", table, err)
		}
	}
	prunedSteps, err := db.Step.PruneHistory(ctx, 365, 0)
	if err != nil {
		t.Fatalf("prune steps: %v", err)
	}
	if _, err := db.Ingredient.PruneHistory(ctx, 365, 0); err != nil {
		t.Fatalf("prune ingredients: %v", err)
	}
	if prunedSteps == 0 {
		t.Fatal("the prune kept every step version, want the uncommitted edit gone")
	}
	for _, ref := range []types.IdentityUUID{mainID, *draft.Id, *a.Id, *b.Id, *nb.Id, *back.Id} {
		commits, err := g.History(ctx, ref)
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		for _, commit := range commits {
			tree, err := g.Materialize(ctx, *commit.Id)
			if err != nil {
				t.Fatalf("Materialize %v after the prune: %v", *commit.Id, err)
			}
			if tree.ContentHash != commit.ContentHash {
				t.Fatalf("commit %v materializes to hash %s after the prune, recorded %s", *commit.Id, tree.ContentHash, commit.ContentHash)
			}
		}
	}
	if _, err := g.Materialize(ctx, *bCommit.Commit.Id); err != nil {
		t.Fatalf("Materialize b's commit after the prune: %v", err)
	}

	// Unsetting an override hard-deletes the ref's row, records the actor on
	// its tombstone, and reads the entity through the base again.
	c, err := g.Branch(ctx, mainID, "by hand")
	if err != nil {
		t.Fatalf("Branch c: %v", err)
	}
	cSaved, err := g.Save(ctx, *c.Id, c.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{stepEdit(mix, "Mix by hand", ` + "`" + `{"knead": 10, "rest": 30}` + "`" + `)}}})
	if err != nil {
		t.Fatalf("save c: %v", err)
	}
	override := cSaved.Saved.Step[0]
	cTree, err := g.Compose(ctx, *c.Id)
	if err != nil {
		t.Fatalf("Compose c: %v", err)
	}
	assertShellTree(t, "c with its override", cTree, []string{"Mix by hand", "Bake"}, []string{"200g flour", "1 egg wash"})
	unset, err := g.Save(editorCtx, *c.Id, cSaved.Ref.Version, RecipeEdits{Step: GraphEdits[types.Step]{Unset: []types.IdentityUUID{*mix.EntityKey}}})
	if err != nil {
		t.Fatalf("unset: %v", err)
	}
	cTree, err = g.Compose(ctx, *c.Id)
	if err != nil {
		t.Fatalf("Compose c: %v", err)
	}
	// c reads its base commit, not the churn change set's uncommitted edits.
	assertShellTree(t, "c read through", cTree, []string{"Mix", "Bake"}, []string{"200g flour", "1 egg wash"})
	var operation, actor string
	if err := pool.QueryRow(ctx, "SELECT operation, data->>'updated_by' FROM step_history WHERE id = $1 ORDER BY _version DESC LIMIT 1", override.Id.ToUUID()).Scan(&operation, &actor); err != nil {
		t.Fatalf("read the override's tombstone: %v", err)
	}
	if operation != "DELETE" || actor != editor.ToUUID().String() {
		t.Fatalf("the override's last history row = (%s, %s), want (DELETE, %s)", operation, actor, editor.ToUUID())
	}
	if _, err := g.Save(ctx, *c.Id, unset.Ref.Version, RecipeEdits{Step: GraphEdits[types.Step]{Unset: []types.IdentityUUID{*mix.EntityKey}}}); !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("unsetting a missing override = %v, want ErrEntityNotFound", err)
	}

	// A stale ref version is fenced out; a sealed ref refuses writes.
	if _, err := g.Save(ctx, *c.Id, cSaved.Ref.Version, RecipeEdits{}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("Save at a stale version = %v, want ErrVersionConflict", err)
	}
	sealed, err := g.Seal(ctx, *c.Id, unset.Ref.Version)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed.Ref.SealedAt == nil || sealed.Commit != nil {
		t.Fatalf("seal = %+v, want a sealed ref and no commit for a ref with no changes", sealed)
	}
	if _, err := g.Save(ctx, *c.Id, sealed.Ref.Version, RecipeEdits{}); !errors.Is(err, ErrRefSealed) {
		t.Fatalf("Save on a sealed ref = %v, want ErrRefSealed", err)
	}
	if _, err := g.Commit(ctx, *c.Id, sealed.Ref.Version, RecipeCommitOptions{}); !errors.Is(err, ErrRefSealed) {
		t.Fatalf("Commit on a sealed ref = %v, want ErrRefSealed", err)
	}

	// A merge carries a delete: the source's tombstone reaches the target,
	// and the entity is gone from the target's compose and from its commits.
	d, err := g.Branch(ctx, mainID, "no egg wash")
	if err != nil {
		t.Fatalf("Branch d: %v", err)
	}
	dSaved, err := g.Save(ctx, *d.Id, d.Version, RecipeEdits{Ingredient: GraphEdits[types.Ingredient]{Delete: []types.IdentityUUID{*wash.EntityKey}}})
	if err != nil {
		t.Fatalf("save d: %v", err)
	}
	if _, err := g.Commit(ctx, *d.Id, dSaved.Ref.Version, RecipeCommitOptions{Message: "no egg wash"}); err != nil {
		t.Fatalf("commit d: %v", err)
	}
	mergedD, err := g.Merge(ctx, *d.Id, mainID, reverted.Ref.Version, nil, RecipeCommitOptions{})
	if err != nil {
		t.Fatalf("Merge d: %v", err)
	}
	if len(mergedD.Conflicts) != 0 || mergedD.Commit == nil {
		t.Fatalf("merge d = %+v, want a clean merge commit", mergedD)
	}
	mainTree, err = g.Compose(ctx, mainID)
	if err != nil {
		t.Fatalf("Compose main: %v", err)
	}
	assertShellTree(t, "main after d", mainTree, []string{"Mix", "Bake"}, []string{"200g flour"})
	if !shellTombstone(t, pool, "ingredient", *wash.EntityKey, mainID) {
		t.Fatal("the merge left main's egg wash row live, want the source's delete on it")
	}
	if got := shellPatchOperation(t, pool, *mergedD.Commit.Id, "ingredient"); got != "DELETE" {
		t.Fatalf("merge d's ingredient patch = %q, want DELETE", got)
	}
	mergedDTree, err := g.Materialize(ctx, *mergedD.Commit.Id)
	if err != nil {
		t.Fatalf("Materialize merge d: %v", err)
	}
	assertShellTree(t, "merge d", mergedDTree, []string{"Mix", "Bake"}, []string{"200g flour"})
	if mergedDTree.ContentHash != mergedD.Commit.ContentHash {
		t.Fatalf("merge d materializes to hash %s, recorded %s", mergedDTree.ContentHash, mergedD.Commit.ContentHash)
	}

	// A member whose key is a plain UUID: the shell ignores the caller's id
	// and mints a row id of its own for each ref's row of the entity.
	callerID := mustShellUUID(t, "7a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d")
	w, err := g.Branch(ctx, mainID, "a whisk")
	if err != nil {
		t.Fatalf("Branch w: %v", err)
	}
	uSaved, err := g.Save(ctx, *w.Id, w.Version, RecipeEdits{Utensil: GraphEdits[types.Utensil]{Upsert: []*types.Utensil{{Id: callerID, Name: "whisk"}}}})
	if err != nil {
		t.Fatalf("save a utensil: %v", err)
	}
	whisk := uSaved.Saved.Utensil[0]
	if whisk.Id == callerID || whisk.Id.IsZero() || whisk.EntityKey == nil {
		t.Fatalf("saved utensil = %+v, want a minted id and entity key", whisk)
	}
	wCommit, err := g.Commit(ctx, *w.Id, uSaved.Ref.Version, RecipeCommitOptions{Message: "a whisk"})
	if err != nil {
		t.Fatalf("commit the utensil: %v", err)
	}
	withWhisk, err := g.Merge(ctx, *w.Id, mainID, mergedD.Ref.Version, nil, RecipeCommitOptions{})
	if err != nil {
		t.Fatalf("Merge w: %v", err)
	}
	whiskTree, err := g.Materialize(ctx, *withWhisk.Commit.Id)
	if err != nil {
		t.Fatalf("Materialize the utensil: %v", err)
	}
	// The egg wash stays gone from main's next commit.
	assertShellTree(t, "commit with a whisk", whiskTree, []string{"Mix", "Bake"}, []string{"200g flour"})
	assertShellUtensils(t, "commit with a whisk", whiskTree, "whisk")
	e, err := g.Branch(ctx, mainID, "balloon whisk")
	if err != nil {
		t.Fatalf("Branch e: %v", err)
	}
	// The override names w's row id; the change set still gets a row of its
	// own.
	eSaved, err := g.Save(ctx, *e.Id, e.Version, RecipeEdits{Utensil: GraphEdits[types.Utensil]{Upsert: []*types.Utensil{{Id: whisk.Id, EntityKey: whisk.EntityKey, Name: "balloon whisk"}}}})
	if err != nil {
		t.Fatalf("save e: %v", err)
	}
	if got := eSaved.Saved.Utensil[0]; got.Id == whisk.Id || got.Id.IsZero() || *got.EntityKey != *whisk.EntityKey {
		t.Fatalf("e's utensil override = %+v, want a row of its own for the whisk", got)
	}
	if _, err := g.Commit(ctx, *e.Id, eSaved.Ref.Version, RecipeCommitOptions{Message: "balloon whisk"}); err != nil {
		t.Fatalf("commit e: %v", err)
	}
	mergedE, err := g.Merge(ctx, *e.Id, mainID, withWhisk.Ref.Version, nil, RecipeCommitOptions{})
	if err != nil {
		t.Fatalf("Merge e: %v", err)
	}
	if len(mergedE.Conflicts) != 0 || mergedE.Commit == nil {
		t.Fatalf("merge e = %+v, want a clean merge commit", mergedE)
	}
	mainTree, err = g.Compose(ctx, mainID)
	if err != nil {
		t.Fatalf("Compose main: %v", err)
	}
	assertShellUtensils(t, "main after e", mainTree, "balloon whisk")

	// Reverting to a tree without the whisk, and merging that, deletes it
	// on main.
	r, err := g.Branch(ctx, mainID, "no whisk")
	if err != nil {
		t.Fatalf("Branch r: %v", err)
	}
	if _, err := g.Revert(ctx, *r.Id, r.Version, *mergedD.Commit.Id); err != nil {
		t.Fatalf("Revert past the whisk: %v", err)
	}
	revertedU, err := g.Merge(ctx, *r.Id, mainID, mergedE.Ref.Version, nil, RecipeCommitOptions{})
	if err != nil {
		t.Fatalf("Merge the revert past the whisk: %v", err)
	}
	if revertedU.Commit == nil || revertedU.Commit.ContentHash != mergedD.Commit.ContentHash {
		t.Fatalf("revert commit = %+v, want the content hash of merge d", revertedU.Commit)
	}
	mainTree, err = g.Compose(ctx, mainID)
	if err != nil {
		t.Fatalf("Compose main: %v", err)
	}
	assertShellUtensils(t, "main reverted past the whisk", mainTree)
	if !shellTombstone(t, pool, "utensil", *whisk.EntityKey, mainID) {
		t.Fatal("the revert left main's whisk row live, want a row that deletes it")
	}
	if got := shellPatchOperation(t, pool, *revertedU.Commit.Id, "utensil"); got != "DELETE" {
		t.Fatalf("the revert's utensil patch = %q, want DELETE", got)
	}

	// Concurrent taggers take consecutive sequences. The test holds the
	// root's lock, both tagging commits wait on it, and once it is released
	// they tag one after the other.
	var taggers []types.IdentityUUID
	var versions []int64
	for i, instruction := range []string{"Fold", "Shape"} {
		ref, err := g.Branch(ctx, mainID, fmt.Sprintf("tagger %d", i))
		if err != nil {
			t.Fatalf("Branch tagger: %v", err)
		}
		s, err := g.Save(ctx, *ref.Id, ref.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{{Position: types.GenericInt64(3 + i), Instruction: instruction, Timings: types.GenericJSON("{}")}}}})
		if err != nil {
			t.Fatalf("save tagger: %v", err)
		}
		taggers = append(taggers, *ref.Id)
		versions = append(versions, s.Ref.Version)
	}
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the root lock: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	var holderPID int32
	if err := holder.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&holderPID); err != nil {
		t.Fatalf("read the lock holder's pid: %v", err)
	}
	if _, err := holder.Exec(ctx, "SELECT 1 FROM recipe WHERE id = $1 FOR NO KEY UPDATE", recipe.Id.ToUUID()); err != nil {
		t.Fatalf("lock the root: %v", err)
	}
	var wg sync.WaitGroup
	sequences := make([]int64, len(taggers))
	errs := make([]error, len(taggers))
	for i := range taggers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := g.Commit(ctx, taggers[i], versions[i], RecipeCommitOptions{Tag: true})
			if err != nil {
				errs[i] = err
				return
			}
			sequences[i] = int64(*result.Commit.Sequence)
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	if waiting := waitForShellWaiters(t, pool, holderPID, len(taggers), done); waiting != len(taggers) {
		_ = holder.Rollback(ctx)
		<-done
		t.Fatalf("%d of %d tagging commits waited on the root's lock (errors %v, sequences %v)", waiting, len(taggers), errs, sequences)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("release the root lock: %v", err)
	}
	<-done
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent tag: %v", err)
		}
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	if !reflect.DeepEqual(sequences, []int64{2, 3}) {
		t.Fatalf("concurrent tags took sequences %v, want [2 3]", sequences)
	}

	// A commit that pins a row version history no longer holds cannot be
	// read.
	if _, err := pool.Exec(ctx, "DELETE FROM utensil_history WHERE id = $1 AND _version = $2", whisk.Id.ToUUID(), whisk.Version); err != nil {
		t.Fatalf("remove the whisk's history row: %v", err)
	}
	if _, err := g.Materialize(ctx, *wCommit.Commit.Id); !errors.Is(err, ErrHistoryMissing) {
		t.Fatalf("Materialize over a missing history row = %v, want ErrHistoryMissing", err)
	}

	// The walk ceiling stops a long walk; a newer schema epoch is refused.
	if _, err := g.WithWalkCeiling(1).Materialize(ctx, *mergedA.Commit.Id); !errors.Is(err, ErrWalkCeiling) {
		t.Fatalf("Materialize past the ceiling = %v, want ErrWalkCeiling", err)
	}
	if _, err := g.WithWalkCeiling(2).Materialize(ctx, *mergedA.Commit.Id); err != nil {
		t.Fatalf("Materialize within the ceiling: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE recipe_commit SET schema_epoch = $1 WHERE id = $2", RecipeGraphSchemaEpoch+1, noBake.Commit.Id.ToUUID()); err != nil {
		t.Fatalf("move a commit to a newer epoch: %v", err)
	}
	for _, commit := range []types.IdentityUUID{*noBake.Commit.Id, *reverted.Commit.Id} {
		if _, err := g.Materialize(ctx, commit); !errors.Is(err, ErrSchemaEpoch) {
			t.Fatalf("Materialize over a newer epoch = %v, want ErrSchemaEpoch", err)
		}
	}

	// A discarded change set is gone.
	if err := g.Discard(ctx, *b.Id, bCommit.Ref.Version); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := g.Compose(ctx, *b.Id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Compose a discarded ref = %v, want ErrNotFound", err)
	}

	assertCanonicalRows(t, db, pool)
}

// assertCanonicalRows normalizes every row the lifecycle left, live and in
// every history image, read under another session time zone, by the
// descriptor's value classes (D19): the
// descriptor names each kind's table and history table and declares every
// column, and each column's class reads what Postgres renders for it. Each
// live step's canonical row then matches the typed row's JSON, the schema
// runtime's JSON for its fields: exactly for its UUIDs, integer and string,
// as the same instant for its date-times, and as the same JSON value for
// its JSON column.
func assertCanonicalRows(t *testing.T, db *Database, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var descriptor struct {
		Version int ` + "`json:\"version\"`" + `
		Kinds   []struct {
			Kind         string            ` + "`json:\"kind\"`" + `
			Table        string            ` + "`json:\"table\"`" + `
			HistoryTable string            ` + "`json:\"historyTable\"`" + `
			Columns      map[string]string ` + "`json:\"columns\"`" + `
		} ` + "`json:\"kinds\"`" + `
	}
	if err := json.Unmarshal([]byte(RecipeGraphDescriptor), &descriptor); err != nil || descriptor.Version != 2 {
		t.Fatalf("read the descriptor: version %d, %v", descriptor.Version, err)
	}
	// Read under a session time zone other than the writers': a live row's
	// date-times render with its offset, and still normalize to UTC.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SET TIME ZONE 'Asia/Kolkata'"); err != nil {
		t.Fatalf("set the time zone: %v", err)
	}
	defer func() { _, _ = conn.Exec(ctx, "RESET TIME ZONE") }()
	read := func(query string, args ...any) []string {
		rows, err := conn.Query(ctx, query, args...)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				t.Fatalf("%s: %v", query, err)
			}
			out = append(out, text)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}
	live, images := 0, 0
	for _, kind := range descriptor.Kinds {
		for _, row := range read("SELECT to_jsonb(t)::text FROM " + kind.Table + " AS t") {
			if _, err := canonical.PostgresRow(kind.Columns, json.RawMessage(row)); err != nil {
				t.Fatalf("%s live row %s: %v", kind.Kind, row, err)
			}
			live++
		}
		for _, image := range read("SELECT data::text FROM " + kind.HistoryTable) {
			if _, err := canonical.PostgresRow(kind.Columns, json.RawMessage(image)); err != nil {
				t.Fatalf("%s history image %s: %v", kind.Kind, image, err)
			}
			images++
		}
	}
	if live == 0 || images == 0 {
		t.Fatalf("normalized %d live rows and %d history images; the lifecycle left both", live, images)
	}

	var columns map[string]string
	for _, kind := range descriptor.Kinds {
		if kind.Kind == "step" {
			columns = kind.Columns
		}
	}
	steps := read("SELECT to_jsonb(t)::text FROM step AS t")
	for _, row := range steps {
		out, err := canonical.PostgresRow(columns, json.RawMessage(row))
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		var id types.IdentityUUID
		if err := json.Unmarshal(got["id"], &id); err != nil {
			t.Fatalf("the canonical id %s is not an Identity.UUID: %v", got["id"], err)
		}
		step, err := db.Step.GetOne(ctx, id, nil)
		if err != nil {
			t.Fatalf("read step %s: %v", got["id"], err)
		}
		typedJSON, err := json.Marshal(step)
		if err != nil {
			t.Fatal(err)
		}
		var typed map[string]json.RawMessage
		if err := json.Unmarshal(typedJSON, &typed); err != nil {
			t.Fatal(err)
		}
		for column, field := range map[string]string{"id": "id", "entity_key": "entityKey", "position": "position", "instruction": "instruction", "updated_by": "updatedBy"} {
			if string(got[column]) != string(typed[field]) {
				t.Fatalf("step %s: canonical %s is %s, the typed row's %s is %s", got["id"], column, got[column], field, typed[field])
			}
		}
		for column, field := range map[string]string{"created_at": "createdAt", "updated_at": "updatedAt"} {
			var want, have time.Time
			if err := json.Unmarshal(got[column], &have); err != nil || !strings.HasSuffix(string(got[column]), "Z\"") {
				t.Fatalf("step %s: canonical %s is %s, not a UTC date-time", got["id"], column, got[column])
			}
			if err := json.Unmarshal(typed[field], &want); err != nil || !have.Equal(want) {
				t.Fatalf("step %s: canonical %s is %s, the typed row's %s is %s", got["id"], column, got[column], field, typed[field])
			}
		}
		var fromRow, fromType any
		if err := json.Unmarshal(got["timings"], &fromRow); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(typed["timings"], &fromType); err != nil || !reflect.DeepEqual(fromRow, fromType) {
			t.Fatalf("step %s: canonical timings %s, the typed row's %s", got["id"], got["timings"], typed["timings"])
		}
	}
	if len(steps) == 0 {
		t.Fatal("the lifecycle left no step rows")
	}
}

func assertShellUtensils(t *testing.T, what string, tree *RecipeTree, names ...string) {
	t.Helper()
	var got []string
	for _, utensil := range tree.Utensil {
		got = append(got, utensil.Name)
	}
	if !reflect.DeepEqual(got, names) {
		t.Fatalf("%s: utensils %q, want %q", what, got, names)
	}
}

// shellTombstone reports whether ref's own row of an entity is the row that
// deletes it.
func shellTombstone(t *testing.T, pool *pgxpool.Pool, table string, entityKey, ref types.IdentityUUID) bool {
	t.Helper()
	var tombstone bool
	if err := pool.QueryRow(context.Background(), "SELECT deleted_on_ref FROM "+table+" WHERE entity_key = $1 AND ref_id = $2", entityKey.ToUUID(), ref.ToUUID()).Scan(&tombstone); err != nil {
		t.Fatalf("read the %s row of %v on %v: %v", table, entityKey, ref, err)
	}
	return tombstone
}

// shellRow reads the id, as hyphenated text, and the version of ref's own
// row of an entity.
func shellRow(t *testing.T, pool *pgxpool.Pool, table string, entityKey, ref types.IdentityUUID) (string, int64) {
	t.Helper()
	var id string
	var version int64
	if err := pool.QueryRow(context.Background(), "SELECT id::text, _version FROM "+table+" WHERE entity_key = $1 AND ref_id = $2", entityKey.ToUUID(), ref.ToUUID()).Scan(&id, &version); err != nil {
		t.Fatalf("read the %s row of %v on %v: %v", table, entityKey, ref, err)
	}
	return id, version
}

// shellPatches reads a commit's patches by kind: each one's operation, row
// id as hyphenated text, and row version.
func shellPatches(t *testing.T, pool *pgxpool.Pool, commit types.IdentityUUID) map[string][3]any {
	t.Helper()
	pins := map[string][3]any{}
	rows, err := pool.Query(context.Background(), "SELECT entity_kind, operation, entity_id::text, entity_version FROM recipe_patch WHERE commit_id = $1", commit.ToUUID())
	if err != nil {
		t.Fatalf("read patches: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, operation, id string
		var version int64
		if err := rows.Scan(&kind, &operation, &id, &version); err != nil {
			t.Fatalf("scan patch: %v", err)
		}
		pins[kind] = [3]any{operation, id, version}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read patches: %v", err)
	}
	return pins
}

// shellPatchOperation reads the operation of a commit's one patch of kind.
func shellPatchOperation(t *testing.T, pool *pgxpool.Pool, commit types.IdentityUUID, kind string) string {
	t.Helper()
	var operation string
	if err := pool.QueryRow(context.Background(), "SELECT operation FROM recipe_patch WHERE commit_id = $1 AND entity_kind = $2", commit.ToUUID(), kind).Scan(&operation); err != nil {
		t.Fatalf("read the %s patch of %v: %v", kind, commit, err)
	}
	return operation
}

// waitForShellWaiters waits until want backends wait, directly or behind one
// another, on a lock the backend holder holds, and returns how many do. It
// stops early when done closes: the waiters finished without waiting.
func waitForShellWaiters(t *testing.T, pool *pgxpool.Pool, holder int32, want int, done <-chan struct{}) int {
	t.Helper()
	const waiters = "WITH RECURSIVE waiting(pid) AS (" +
		"SELECT pid FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)) " +
		"UNION SELECT a.pid FROM pg_stat_activity AS a JOIN waiting AS w ON w.pid = ANY(pg_blocking_pids(a.pid))" +
		") SELECT count(*) FROM waiting"
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := pool.QueryRow(context.Background(), waiters, holder).Scan(&n); err != nil {
			t.Fatalf("count lock waiters: %v", err)
		}
		if n >= want {
			return n
		}
		select {
		case <-done:
			return n
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return n
		}
	}
}

// landedShell is a change set's edits landed on a line: the change set's
// save and commit, and the merge.
type landedShell struct {
	saved     *RecipeSaveResult
	committed *RecipeCommitResult
	merged    *RecipeMergeResult
}

// landShell branches a change set of line, saves edits on it, commits it and
// merges it back at version with opts, and fails unless the merge commits.
func landShell(t *testing.T, ctx context.Context, g *RecipeGraph, line types.IdentityUUID, version int64, name string, edits RecipeEdits, opts RecipeCommitOptions) landedShell {
	t.Helper()
	draft, err := g.Branch(ctx, line, name)
	if err != nil {
		t.Fatalf("Branch %s: %v", name, err)
	}
	saved, err := g.Save(ctx, *draft.Id, draft.Version, edits)
	if err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
	committed, err := g.Commit(ctx, *draft.Id, saved.Ref.Version, RecipeCommitOptions{Message: name})
	if err != nil {
		t.Fatalf("commit %s: %v", name, err)
	}
	merged, err := g.Merge(ctx, *draft.Id, line, version, nil, opts)
	if err != nil {
		t.Fatalf("merge %s: %v", name, err)
	}
	if len(merged.Conflicts) != 0 || merged.Commit == nil {
		t.Fatalf("merge %s = %+v, want a clean merge commit", name, merged)
	}
	return landedShell{saved: saved, committed: committed, merged: merged}
}

func stepEdit(step *types.Step, instruction, timings string) *types.Step {
	return &types.Step{EntityKey: step.EntityKey, Position: step.Position, Instruction: instruction, Timings: types.GenericJSON(timings)}
}

func ingredientEdit(ingredient *types.Ingredient, quantity string) *types.Ingredient {
	return &types.Ingredient{EntityKey: ingredient.EntityKey, StepKey: ingredient.StepKey, Quantity: quantity, Substitutes: ingredient.Substitutes}
}

func assertShellTree(t *testing.T, what string, tree *RecipeTree, steps, ingredients []string) {
	t.Helper()
	var gotSteps, gotIngredients []string
	for _, step := range tree.Step {
		gotSteps = append(gotSteps, step.Instruction)
	}
	for _, ingredient := range tree.Ingredient {
		gotIngredients = append(gotIngredients, ingredient.Quantity)
	}
	// Steps are ordered by position; ingredients have no order column, so
	// they come by entity key, which is random.
	sort.Strings(gotIngredients)
	ingredients = append([]string(nil), ingredients...)
	sort.Strings(ingredients)
	if !reflect.DeepEqual(gotSteps, steps) || !reflect.DeepEqual(gotIngredients, ingredients) {
		t.Fatalf("%s: steps %q and ingredients %q, want %q and %q", what, gotSteps, gotIngredients, steps, ingredients)
	}
}

func assertShellJSON[T ~[]byte](t *testing.T, what string, got T, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("%s: decode %s: %v", what, got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("%s: decode %s: %v", what, want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("%s = %s, want %s", what, got, want)
	}
}

func mustShellUUID(t *testing.T, value string) types.IdentityUUID {
	t.Helper()
	id, err := types.ParseIdentityUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func openShellDatabase(t *testing.T, dsn string) (*Database, *pgxpool.Pool) {
	t.Helper()
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(base.Close)
	schema := fmt.Sprintf("version_graph_shell_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	t.Cleanup(db.Close)
	return db, pool
}
`
