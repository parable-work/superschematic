package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVersionGraphShellOnPostgres generates fixture-version-graph-db, whose
// ORM carries the Recipe graph's shell (versiongraph_recipe.go), then
// builds, vets and tests the ORM module with versionGraphShellTest. That
// test runs the shell against the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL through a whole lifecycle: a
// primary line, a tagged commit, two change sets merged back (one cleanly,
// one with a conflict settled by a resolution), a parent deleted with its
// children, a revert, pruned history, an unset override, and the fence, the
// seal, the walk ceiling and the schema epoch refusing what they refuse.
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
	out := runVersionGraphModule(t, ormDir)
	if !strings.Contains(out, "--- PASS: TestVersionGraphShellOnPostgres") {
		t.Fatal("the generated ORM module did not run TestVersionGraphShellOnPostgres")
	}
}

const versionGraphShellTest = `package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	versiongraph "github.com/parable-work/superschematic/runtime/versiongraph/go"
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

	// A primary line holds its steps and ingredients as its own rows.
	main, err := g.CreatePrimary(ctx, *recipe.Id, "main")
	if err != nil {
		t.Fatalf("CreatePrimary: %v", err)
	}
	mainID := *main.Id
	if main.ParentRef != nil || main.HeadCommit != nil {
		t.Fatalf("a primary line has no parent and no head: %+v", main)
	}
	saved, err := g.Save(ctx, mainID, main.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{
		{Position: 1, Instruction: "Mix", Timings: types.GenericJSON(` + "`" + `{"knead": 10, "rest": 30}` + "`" + `)},
		{Position: 2, Instruction: "Bake", Timings: types.GenericJSON(` + "`" + `{"oven": 40}` + "`" + `)},
	}}})
	if err != nil {
		t.Fatalf("save steps: %v", err)
	}
	if saved.Ref.Version != main.Version+1 {
		t.Fatalf("Save moved the ref to version %d, want %d", saved.Ref.Version, main.Version+1)
	}
	mix, bake := saved.Saved.Step[0], saved.Saved.Step[1]
	if mix.EntityKey == nil || bake.EntityKey == nil || *mix.EntityKey == *bake.EntityKey {
		t.Fatalf("new steps need generated entity keys of their own: %v, %v", mix.EntityKey, bake.EntityKey)
	}
	saved, err = g.Save(ctx, mainID, saved.Ref.Version, RecipeEdits{Ingredient: GraphEdits[types.Ingredient]{Upsert: []*types.Ingredient{
		{StepKey: *mix.EntityKey, Quantity: "200g flour", Substitutes: types.GenericJSON(` + "`" + `{"type": "object"}` + "`" + `)},
		{StepKey: *bake.EntityKey, Quantity: "1 egg wash", Substitutes: types.GenericJSON(` + "`" + `{"type": "object"}` + "`" + `)},
	}}})
	if err != nil {
		t.Fatalf("save ingredients: %v", err)
	}
	flour, wash := saved.Saved.Ingredient[0], saved.Saved.Ingredient[1]

	// A tagged commit is published version 1.
	first, err := g.Commit(ctx, mainID, saved.Ref.Version, RecipeCommitOptions{Message: "first", Tag: true})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	c1 := first.Commit
	if c1.Sequence == nil || *c1.Sequence != 1 || c1.SchemaEpoch != RecipeGraphSchemaEpoch || c1.Message != "first" || c1.ParentCommit != nil {
		t.Fatalf("first commit = %+v, want sequence 1 at epoch %d with no parent", c1, RecipeGraphSchemaEpoch)
	}
	if first.Ref.HeadCommit == nil || *first.Ref.HeadCommit.Id != *c1.Id {
		t.Fatalf("the ref's head is %+v, want the commit", first.Ref.HeadCommit)
	}
	if _, err := g.Commit(ctx, mainID, first.Ref.Version, RecipeCommitOptions{}); !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("a second commit with no change = %v, want ErrNothingToCommit", err)
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
	mergedA, err := g.Merge(ctx, *a.Id, mainID, first.Ref.Version, nil)
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
	conflicted, err := g.Merge(ctx, *b.Id, mainID, mergedA.Ref.Version, nil)
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
	}})
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
	deleted, err := g.Save(ctx, mainID, mergedB.Ref.Version, RecipeEdits{Step: GraphEdits[types.Step]{Delete: []types.IdentityUUID{*bake.EntityKey}}})
	if err != nil {
		t.Fatalf("delete bake: %v", err)
	}
	mainTree, err = g.Compose(ctx, mainID)
	if err != nil {
		t.Fatalf("Compose main: %v", err)
	}
	assertShellTree(t, "main without bake", mainTree, []string{"Mix"}, []string{"300g flour"})
	noBake, err := g.Commit(ctx, mainID, deleted.Ref.Version, RecipeCommitOptions{Message: "no baking"})
	if err != nil {
		t.Fatalf("commit the delete: %v", err)
	}
	noBakeTree, err := g.Materialize(ctx, *noBake.Commit.Id)
	if err != nil {
		t.Fatalf("Materialize the delete: %v", err)
	}
	assertShellTree(t, "commit without bake", noBakeTree, []string{"Mix"}, []string{"300g flour"})
	// The step's DELETE pins its tombstone; the egg wash's, removed with
	// its step, pins its last committed row.
	pins := map[string][3]any{}
	rows, err := pool.Query(ctx, "SELECT entity_kind, operation, entity_id::text, entity_version FROM recipe_patch WHERE commit_id = $1", noBake.Commit.Id.ToUUID())
	if err != nil {
		t.Fatalf("read patches: %v", err)
	}
	for rows.Next() {
		var kind, operation, id string
		var version int64
		if err := rows.Scan(&kind, &operation, &id, &version); err != nil {
			t.Fatalf("scan patch: %v", err)
		}
		pins[kind] = [3]any{operation, id, version}
	}
	rows.Close()
	if got := pins["step"]; len(pins) != 2 || got[0] != "DELETE" || got[1] != bake.Id.ToUUID().String() || got[2].(int64) <= bake.Version {
		t.Fatalf("step patch = %v, want a DELETE pinned to bake's tombstone version", got)
	}
	if got := pins["ingredient"]; got[0] != "DELETE" || got[1] != wash.Id.ToUUID().String() || got[2].(int64) != wash.Version {
		t.Fatalf("ingredient patch = %v, want a DELETE pinned to the egg wash's last committed row", got)
	}

	// Reverting to published version 1 composes and hashes as it did.
	reverted, err := g.Revert(ctx, mainID, noBake.Ref.Version, *c1.Id)
	if err != nil {
		t.Fatalf("Revert: %v", err)
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
	churn, err := g.Save(ctx, mainID, reverted.Ref.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{stepEdit(mix, "Mix well", ` + "`" + `{"knead": 10, "rest": 30}` + "`" + `)}}})
	if err != nil {
		t.Fatalf("save churn: %v", err)
	}
	churn, err = g.Save(ctx, mainID, churn.Ref.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{stepEdit(mix, "Mix gently", ` + "`" + `{"knead": 10, "rest": 30}` + "`" + `)}}})
	if err != nil {
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
	for _, ref := range []types.IdentityUUID{mainID, *a.Id, *b.Id} {
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
	// c reads its base commit, not main's uncommitted edits.
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

	// Concurrent taggers take distinct sequences.
	var taggers []types.IdentityUUID
	var versions []int64
	for i, instruction := range []string{"Fold", "Shape"} {
		ref, err := g.Branch(ctx, mainID, fmt.Sprintf("tagger %d", i))
		if err != nil {
			t.Fatalf("Branch tagger: %v", err)
		}
		s, err := g.Save(ctx, *ref.Id, ref.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{{Position: types.GenericInt64(3 + i), Instruction: instruction, Timings: types.GenericJSON(` + "`" + `{}` + "`" + `)}}}})
		if err != nil {
			t.Fatalf("save tagger: %v", err)
		}
		taggers = append(taggers, *ref.Id)
		versions = append(versions, s.Ref.Version)
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
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent tag: %v", err)
		}
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	if !reflect.DeepEqual(sequences, []int64{2, 3}) {
		t.Fatalf("concurrent tags took sequences %v, want [2 3]", sequences)
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
