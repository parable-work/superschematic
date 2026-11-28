package ormgen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// mergeAccessORMCases are the cases mergeAccessORMTest runs over
// fixture-merge-access-db, by name (D147, D156, D180), as
// merge_access_rules.rs holds the Rust repositories to them (D181) and
// merge_access.test.ts the TypeScript ones.
var mergeAccessORMCases = []string{
	"a user who may not write one of the two labels merges neither into the other, and writes nothing",
	"a party whose grant reaches one of the two labels merges neither into the other, and writes nothing",
	"a user who may write both labels merges one into the other",
	"a user who may not write a note on the label merged away merges neither, naming the notes, and writes nothing",
	"a user who may not write a board that holds the label merged away merges neither, naming the boards, and writes nothing",
	"a merge outside any operation repoints the rows no caller is held to",
	"a party merges two labels only when its grants reach the notes on the one merged away",
	"resolve answers a party nothing of a brand its grants do not reach",
	"a name another brand holds is refused naming nothing of that brand",
	"a party whose grants reach both brands merges one into the other",
	"resolve answers a user nothing of a private brand it may not read",
	"a name a private brand holds is refused naming nothing of that brand",
	"a user who may write both private brands merges one into the other",
}

// TestMergeIntoKeepsToTheCallerOnPostgres holds the generated Go ORM's
// MergeInto, and Aliased's Resolve and refusals, to per-record access
// (D147, D156, D180). Under WithPrincipal a merge finds only the two rows
// its caller may write, so a merge of a row the caller may not write, or
// into one, is ErrNotFound before any statement runs, as UpdateOne and
// DeleteOne are; and it writes no row of another table its caller may not
// write, so one that would repoint a note, or a board's pair, the caller
// may not write is ErrMergeOutOfReach, naming the table, and writes
// nothing, where a merge outside any operation repoints them. Resolve
// answers no row for a brand the caller does not see, and a name another
// brand holds is refused with neither that brand's key nor its fields. A
// user is held to the rows' audiences, and a party to the rows its grants
// reach: a brand everyone sees to a party's grants, and a private brand,
// whose audience ids the Go behaviors' core reads as a list, to its owner.
// It loads fixture-merge-access-db, generates its Go types, ORM and DDL
// into a temporary tree, and runs mergeAccessORMTest on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL; every case must pass. The module
// links the core's static archive, which scripts/rules-archive.sh stages.
// It skips without the database or the archive, and in -short mode.
func TestMergeIntoKeepsToTheCallerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated ORM's merge under per-record access in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM's merges under per-record access against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}
	ormDir := generateBehaviorORMModule(t, paths, "fixture-merge-access-db")
	if err := os.WriteFile(filepath.Join(ormDir, "merge_access_test.go"), []byte(mergeAccessORMTest), 0o644); err != nil {
		t.Fatalf("write the merge test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range mergeAccessORMCases {
		if want := "--- PASS: TestMergeIntoKeepsToTheCaller/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// mergeAccessORMTest is the generated module's test: labels, notes, boards
// and brands written with no principal, which reads and writes every row,
// then merged, resolved and renamed under one, with the rows read back
// past the repositories with SQL.
const mergeAccessORMTest = `package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-merge-access-db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

func TestMergeIntoKeepsToTheCaller(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the merges against Postgres")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	background := context.Background()
	base, err := pgxpool.New(background, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer base.Close()
	schema := fmt.Sprintf("ormgen_merge_access_%d", time.Now().UnixNano())
	if _, err := base.Exec(background, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(background, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	if _, err := pool.Exec(background, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	defer db.Close()

	id := func(t *testing.T, u uuid.UUID) types.IdentityUUID {
		t.Helper()
		parsed, err := types.ParseIdentityUUID(u.String())
		if err != nil {
			t.Fatalf("an id: %v", err)
		}
		return parsed
	}
	ada, bob, party := uuid.New(), uuid.New(), uuid.New()
	// No principal: the writes that set the rows up read and write every
	// row, as a job's do.
	ctx := WithUserID(background, id(t, ada))
	asUser := func(who uuid.UUID) context.Context {
		return WithPrincipal(ctx, access.Principal{Subject: who.String(), Kind: access.KindUser})
	}
	asParty := func(grants ...access.GrantReach) context.Context {
		return WithPrincipal(ctx, access.Principal{Subject: party.String(), Kind: access.KindParty, Grants: grants})
	}
	grant := func(target string, on types.IdentityUUID, reaches map[string][]string) access.GrantReach {
		return access.GrantReach{ID: uuid.NewString(), TargetType: target, TargetID: on.String(), Reaches: reaches}
	}
	label := func(t *testing.T, name string, owner uuid.UUID, audience string) types.IdentityUUID {
		t.Helper()
		row, err := db.Label.CreateOne(ctx, &types.Label{Name: name, Owner: id(t, owner), Audience: audience, AgentAccess: "readOnly"})
		if err != nil {
			t.Fatalf("create the label %s: %v", name, err)
		}
		return *row.Id
	}
	// note is a note on a label by author, private to its author or seen
	// by everyone.
	note := func(t *testing.T, on types.IdentityUUID, author uuid.UUID, audience string) uuid.UUID {
		t.Helper()
		var key uuid.UUID
		if err := pool.QueryRow(ctx, "INSERT INTO label_note (label_id, author, text, audience) VALUES ($1, $2, 'a note', $3) RETURNING id", on.ToUUID(), author, audience).Scan(&key); err != nil {
			t.Fatalf("a note: %v", err)
		}
		return key
	}
	notes := func(t *testing.T, on types.IdentityUUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM label_note WHERE label_id = $1", on.ToUUID()).Scan(&n); err != nil {
			t.Fatalf("count the notes: %v", err)
		}
		return n
	}
	// board is a board of owner's holding the labels, private to its owner.
	board := func(t *testing.T, owner uuid.UUID, labels ...types.IdentityUUID) uuid.UUID {
		t.Helper()
		var key uuid.UUID
		if err := pool.QueryRow(ctx, "INSERT INTO board (owner, audience) VALUES ($1, 'private') RETURNING id", owner).Scan(&key); err != nil {
			t.Fatalf("a board: %v", err)
		}
		for _, l := range labels {
			if _, err := pool.Exec(ctx, "INSERT INTO board_label (board_id, label_id) VALUES ($1, $2)", key, l.ToUUID()); err != nil {
				t.Fatalf("a board's label: %v", err)
			}
		}
		return key
	}
	pairs := func(t *testing.T, on types.IdentityUUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM board_label WHERE label_id = $1", on.ToUUID()).Scan(&n); err != nil {
			t.Fatalf("count the boards' labels: %v", err)
		}
		return n
	}
	held := func(t *testing.T, of types.IdentityUUID) bool {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM label WHERE id = $1", of.ToUUID()).Scan(&n); err != nil {
			t.Fatalf("count the labels: %v", err)
		}
		return n == 1
	}
	refused := func(t *testing.T, caller context.Context, from, into types.IdentityUUID) {
		t.Helper()
		if _, err := db.Label.MergeInto(caller, from, into); !errors.Is(err, ErrNotFound) {
			t.Fatalf("merge %v into %v: %v, want ErrNotFound", from, into, err)
		}
	}
	// beyond holds a merge to ErrMergeOutOfReach naming table, and naming
	// no key of the row beyond the caller.
	beyond := func(t *testing.T, caller context.Context, from, into types.IdentityUUID, table string, hidden uuid.UUID) {
		t.Helper()
		_, err := db.Label.MergeInto(caller, from, into)
		if !errors.Is(err, ErrMergeOutOfReach) || !strings.Contains(err.Error(), table) {
			t.Fatalf("merge %v into %v: %v, want ErrMergeOutOfReach naming %s", from, into, err, table)
		}
		if strings.Contains(err.Error(), hidden.String()) {
			t.Fatalf("the refusal names the row the caller may not write: %v", err)
		}
	}

	t.Run("a user who may not write one of the two labels merges neither into the other, and writes nothing", func(t *testing.T) {
		mine, theirs := label(t, "bug", ada, "private"), label(t, "defect", bob, "private")
		note(t, theirs, bob, "private")
		refused(t, asUser(ada), theirs, mine)
		refused(t, asUser(ada), mine, theirs)
		if !held(t, mine) || !held(t, theirs) || notes(t, theirs) != 1 || notes(t, mine) != 0 {
			t.Fatal("a merge the caller may not make wrote")
		}
	})

	t.Run("a party whose grant reaches one of the two labels merges neither into the other, and writes nothing", func(t *testing.T) {
		reached, other := label(t, "open", ada, "everyone"), label(t, "shut", ada, "everyone")
		note(t, reached, ada, "everyone")
		caller := asParty(grant("Label", reached, nil))
		refused(t, caller, reached, other)
		refused(t, caller, other, reached)
		if !held(t, reached) || !held(t, other) || notes(t, reached) != 1 || notes(t, other) != 0 {
			t.Fatal("a merge the caller may not make wrote")
		}
	})

	t.Run("a user who may write both labels merges one into the other", func(t *testing.T) {
		stays, goes := label(t, "crash", ada, "private"), label(t, "fault", ada, "private")
		note(t, goes, ada, "private")
		note(t, goes, bob, "everyone")
		board(t, ada, goes)
		result, err := db.Label.MergeInto(asUser(ada), goes, stays)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if result.Repointed["LabelNote"] != 2 || result.Repointed["Board.labels"] != 1 || held(t, goes) || notes(t, stays) != 2 || pairs(t, stays) != 1 {
			t.Fatalf("the merge repointed %v, and left the label merged away: %v", result.Repointed, held(t, goes))
		}
	})

	t.Run("a user who may not write a note on the label merged away merges neither, naming the notes, and writes nothing", func(t *testing.T) {
		stays, goes := label(t, "hang", ada, "private"), label(t, "freeze", ada, "private")
		note(t, goes, ada, "private")
		hidden := note(t, goes, bob, "private")
		board(t, ada, goes)
		beyond(t, asUser(ada), goes, stays, "LabelNote", hidden)
		if !held(t, goes) || !held(t, stays) || notes(t, goes) != 2 || notes(t, stays) != 0 || pairs(t, goes) != 1 {
			t.Fatal("a merge the caller may not make wrote")
		}
	})

	t.Run("a user who may not write a board that holds the label merged away merges neither, naming the boards, and writes nothing", func(t *testing.T) {
		stays, goes := label(t, "leak", ada, "private"), label(t, "drip", ada, "private")
		note(t, goes, ada, "private")
		hidden := board(t, bob, goes)
		beyond(t, asUser(ada), goes, stays, "Board", hidden)
		if !held(t, goes) || notes(t, goes) != 1 || pairs(t, goes) != 1 || pairs(t, stays) != 0 {
			t.Fatal("a merge the caller may not make wrote")
		}
	})

	t.Run("a merge outside any operation repoints the rows no caller is held to", func(t *testing.T) {
		stays, goes := label(t, "slow", ada, "private"), label(t, "laggy", ada, "private")
		note(t, goes, bob, "private")
		board(t, bob, goes)
		result, err := db.Label.MergeInto(ctx, goes, stays)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if result.Repointed["LabelNote"] != 1 || result.Repointed["Board.labels"] != 1 || held(t, goes) || notes(t, stays) != 1 || pairs(t, stays) != 1 {
			t.Fatalf("the merge repointed %v", result.Repointed)
		}
	})

	t.Run("a party merges two labels only when its grants reach the notes on the one merged away", func(t *testing.T) {
		stays, goes := label(t, "tidy", ada, "everyone"), label(t, "neat", ada, "everyone")
		hidden := note(t, goes, ada, "everyone")
		// Its grants reach both labels and no note.
		beyond(t, asParty(grant("Label", stays, nil), grant("Label", goes, nil)), goes, stays, "LabelNote", hidden)
		if !held(t, goes) || notes(t, goes) != 1 {
			t.Fatal("a merge the caller may not make wrote")
		}
		// Its role's path from a note to its label reaches the notes on each.
		notesOf := map[string][]string{"LabelNote": {"label"}}
		result, err := db.Label.MergeInto(asParty(grant("Label", stays, notesOf), grant("Label", goes, notesOf)), goes, stays)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if result.Repointed["LabelNote"] != 1 || held(t, goes) || notes(t, stays) != 1 {
			t.Fatalf("the merge repointed %v", result.Repointed)
		}
	})

	brand := func(t *testing.T, name string) types.IdentityUUID {
		t.Helper()
		row, err := db.Brand.CreateOne(ctx, &BrandCreate{Name: name, Owner: id(t, ada), Audience: "everyone", AgentAccess: "followRole", EffectiveAgents: "followRole"})
		if err != nil {
			t.Fatalf("create the brand %s: %v", name, err)
		}
		return *row.Id
	}
	resolve := func(t *testing.T, caller context.Context, name string) *BrandNameResolveResultRow {
		t.Helper()
		out, err := db.Brand.Name().Resolve(caller, BrandNameResolveParams{Name: name})
		if err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
		return out.Row
	}

	t.Run("resolve answers a party nothing of a brand its grants do not reach", func(t *testing.T) {
		acme, globex := brand(t, "Acme"), brand(t, "Globex")
		caller := asParty(grant("Brand", acme, nil))
		if found := resolve(t, caller, "globex"); found != nil {
			t.Fatalf("resolve globex = %+v for a party its grants do not reach, want no row", found)
		}
		if found := resolve(t, caller, "ACME"); found == nil || found.Key != acme.String() {
			t.Fatalf("resolve ACME = %+v, want %s", found, acme)
		}
		if found := resolve(t, ctx, "globex"); found == nil || found.Key != globex.String() {
			t.Fatalf("resolve globex outside any operation = %+v, want %s", found, globex)
		}
		if found := resolve(t, caller, "nobody"); found != nil {
			t.Fatalf("resolve nobody = %+v, want no row", found)
		}
	})

	t.Run("a name another brand holds is refused naming nothing of that brand", func(t *testing.T) {
		hooli, initech := brand(t, "Hooli"), brand(t, "Initech")
		caller := asParty(grant("Brand", hooli, nil))
		_, err := db.Brand.Name().Rename(caller, hooli, BrandNameRenameParams{Name: "INITECH"})
		refusal, ok := rules.AsRefusal(err)
		if !ok {
			t.Fatalf("a rename to INITECH = %v, want the veto name_taken", err)
		}
		var details struct {
			VetoCode    string                     ` + "`" + `json:"vetoCode"` + "`" + `
			VetoDetails map[string]json.RawMessage ` + "`" + `json:"vetoDetails"` + "`" + `
		}
		if err := json.Unmarshal(refusal.Details, &details); err != nil || details.VetoCode != "name_taken" {
			t.Fatalf("a rename to INITECH = %v (%s), want the veto name_taken", err, refusal.Details)
		}
		if _, ok := details.VetoDetails["holder"]; ok || len(details.VetoDetails) != 2 {
			t.Fatalf("the refusal's details %s name more than the name", refusal.Details)
		}
		if text := err.Error() + string(refusal.Details); strings.Contains(text, initech.String()) || strings.Contains(text, initech.ToUUID().String()) {
			t.Fatalf("the refusal names the brand that holds the name: %s", text)
		}
	})

	t.Run("a party whose grants reach both brands merges one into the other", func(t *testing.T) {
		stays, goes := brand(t, "Pied Piper"), brand(t, "Piper Net")
		result, err := db.Brand.MergeInto(asParty(grant("Brand", stays, nil), grant("Brand", goes, nil)), goes, stays)
		if err != nil {
			t.Fatalf("merge: %v (%+v)", err, result)
		}
		if found := resolve(t, ctx, "piper net"); found == nil || found.Key != stays.String() || !found.Alias {
			t.Fatalf("resolve piper net after the merge = %+v, want the brand that stays, by an alias", found)
		}
	})

	// privateBrand is a brand its owner alone sees, created naming no agent
	// access, which takes the table's default.
	privateBrand := func(t *testing.T, name string, owner uuid.UUID) types.IdentityUUID {
		t.Helper()
		row, err := db.Brand.CreateOne(ctx, &BrandCreate{Name: name, Owner: id(t, owner), Audience: "private"})
		if err != nil {
			t.Fatalf("create the private brand %s: %v", name, err)
		}
		return *row.Id
	}
	// nameTaken holds err to the veto name_taken, naming nothing of holder.
	nameTaken := func(t *testing.T, err error, holder types.IdentityUUID) {
		t.Helper()
		refusal, ok := rules.AsRefusal(err)
		if !ok {
			t.Fatalf("%v, want the veto name_taken", err)
		}
		var details struct {
			VetoCode    string                     ` + "`" + `json:"vetoCode"` + "`" + `
			VetoDetails map[string]json.RawMessage ` + "`" + `json:"vetoDetails"` + "`" + `
		}
		if err := json.Unmarshal(refusal.Details, &details); err != nil || details.VetoCode != "name_taken" {
			t.Fatalf("%v (%s), want the veto name_taken", err, refusal.Details)
		}
		if _, ok := details.VetoDetails["holder"]; ok || len(details.VetoDetails) != 2 {
			t.Fatalf("the refusal's details %s name more than the name", refusal.Details)
		}
		if text := err.Error() + string(refusal.Details); strings.Contains(text, holder.String()) || strings.Contains(text, holder.ToUUID().String()) {
			t.Fatalf("the refusal names the brand that holds the name: %s", text)
		}
	}

	t.Run("resolve answers a user nothing of a private brand it may not read", func(t *testing.T) {
		umbrella := privateBrand(t, "Umbrella", ada)
		if found := resolve(t, asUser(bob), "umbrella"); found != nil {
			t.Fatalf("resolve umbrella = %+v for a user outside its audience, want no row", found)
		}
		if found := resolve(t, asUser(ada), "UMBRELLA"); found == nil || found.Key != umbrella.String() {
			t.Fatalf("resolve UMBRELLA for its owner = %+v, want %s", found, umbrella)
		}
		if found := resolve(t, ctx, "umbrella"); found == nil || found.Key != umbrella.String() {
			t.Fatalf("resolve umbrella outside any operation = %+v, want %s", found, umbrella)
		}
	})

	t.Run("a name a private brand holds is refused naming nothing of that brand", func(t *testing.T) {
		tyrell, wayne := privateBrand(t, "Tyrell", ada), privateBrand(t, "Wayne", bob)
		_, err := db.Brand.CreateOne(asUser(bob), &BrandCreate{Name: "TYRELL", Owner: id(t, bob), Audience: "private"})
		nameTaken(t, err, tyrell)
		_, err = db.Brand.Name().Rename(asUser(bob), wayne, BrandNameRenameParams{Name: "tyrell"})
		nameTaken(t, err, tyrell)
	})

	t.Run("a user who may write both private brands merges one into the other", func(t *testing.T) {
		stays, goes := privateBrand(t, "Cyberdyne", ada), privateBrand(t, "Cyberdyne Systems", ada)
		if _, err := db.Brand.MergeInto(asUser(bob), goes, stays); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a merge by a user outside both brands' audience: %v, want ErrNotFound", err)
		}
		if _, err := db.Brand.MergeInto(asUser(ada), goes, stays); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if found := resolve(t, asUser(ada), "cyberdyne systems"); found == nil || found.Key != stays.String() || !found.Alias {
			t.Fatalf("resolve cyberdyne systems after the merge = %+v, want the brand that stays, by an alias", found)
		}
		if found := resolve(t, asUser(bob), "cyberdyne systems"); found != nil {
			t.Fatalf("resolve cyberdyne systems for a user outside its audience = %+v, want no row", found)
		}
	})
}
`
