package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedUpsertOnPostgres generates the ORM module for
// fixture-ledger-db, then builds, vets and tests it. Its generated test,
// against the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (it skips
// without the database), upserts (D79): the first UpsertOne by an entry's
// account and external id inserts it, the next updates the row that holds
// the key, and one whose update sets nothing returns the
// row as it was; a soft-deleted entry no longer holds the key of the
// partial unique index, so a new row is inserted, and an upsert by the
// primary key of a deleted row is ErrNotFound; a key the table has none of
// is refused. It also filters by the operations D79 adds: In on an integer
// and IsNull on text.
func TestGeneratedUpsertOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, "fixture-ledger-db")
	if err := os.WriteFile(filepath.Join(ormDir, "upsert_test.go"), []byte(upsertORMTest), 0o644); err != nil {
		t.Fatalf("write upsert test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestUpsertOnPostgres") {
		t.Fatalf("the generated ORM did not run TestUpsertOnPostgres:\n%s", out)
	}
}

var upsertORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-ledger-db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUpsertOnPostgres(t *testing.T) {` + strings.Replace(pageTestSetup, "PREFIX", "upsert_orm", 1) + `
	alice, err := types.ParseIdentityUUID("TKbHsHDO6x9RCjbs5c2u4")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUserID(ctx, alice)
	account, err := db.Account.CreateOne(ctx, &types.Account{Name: "Cash", Code: "cash"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	input := func(amount types.GenericInt64) *types.Entry {
		return &types.Entry{Account: types.Account{Id: account.Id}, ExternalId: "plaid-1", AmountCents: amount, BookedOn: "2026-10-25", Tags: []string{}}
	}
	amount := func(n types.GenericInt64) *types.GenericInt64 { return &n }

	inserted, err := db.Entry.UpsertOne(ctx, input(100), EntryUniqueByAccountExternalId, &EntryUpdate{AmountCents: amount(100)})
	if err != nil || inserted.AmountCents != 100 || inserted.Kind != "debit" {
		t.Fatalf("the first upsert = %+v, %v; want a new entry of 100, a debit", inserted, err)
	}
	memo := "cleared"
	updated, err := db.Entry.UpsertOne(ctx, input(150), EntryUniqueByAccountExternalId, &EntryUpdate{AmountCents: amount(150), Memo: &memo})
	if err != nil || *updated.Id != *inserted.Id || updated.AmountCents != 150 || updated.Memo != "cleared" {
		t.Fatalf("the second upsert = %+v, %v; want the same entry, 150 and cleared", updated, err)
	}
	// The update stamps updatedAt with the ORM's clock, the insert the
	// database's default, so the two are not compared.
	kept, err := db.Entry.UpsertOne(ctx, input(999), EntryUniqueByAccountExternalId, nil)
	if err != nil || *kept.Id != *inserted.Id || kept.AmountCents != 150 || !time.Time(kept.UpdatedAt).Equal(time.Time(updated.UpdatedAt)) {
		t.Fatalf("an upsert that updates nothing = %+v, %v; want the row as it was", kept, err)
	}
	again, err := db.Account.UpsertOne(ctx, &types.Account{Name: "Cash", Code: "cash"}, AccountUniqueByCode, &AccountUpdate{})
	if err != nil || *again.Id != *account.Id {
		t.Fatalf("an upsert of the account by its code = %+v, %v; want the account", again, err)
	}

	// A soft-deleted entry no longer holds the live key: a new one is
	// inserted. An upsert by the deleted row's own key updates nothing.
	if err := db.Entry.DeleteOne(ctx, *inserted.Id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	fresh, err := db.Entry.UpsertOne(ctx, input(5), EntryUniqueByAccountExternalId, &EntryUpdate{})
	if err != nil || *fresh.Id == *inserted.Id {
		t.Fatalf("an upsert after the delete = %+v, %v; want a new entry", fresh, err)
	}
	byKey := input(7)
	byKey.Id = inserted.Id
	byKey.ExternalId = "plaid-2"
	if _, err := db.Entry.UpsertOne(ctx, byKey, EntryUniqueById, &EntryUpdate{Memo: &memo}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an upsert by a deleted row's key = %v, want ErrNotFound", err)
	}
	if _, err := db.Entry.UpsertOne(ctx, input(1), EntryUniqueKey("memo"), nil); err == nil {
		t.Fatal("an upsert by a key the table has none of was not refused")
	}

	// In on an integer, IsNull on text (D79).
	no := false
	found, total, err := db.Entry.FindMany(ctx, &EntryFilter{AmountCents: &Int64Filter{In: []int64{5, 6}}, Memo: &StringFilter{IsNull: &no}}, &EntryFindOptions{Fields: EntryFields{Id: true}, IncludeDeleted: true})
	if err != nil || total != 0 || len(found) != 0 {
		t.Fatalf("entries of 5 or 6 with a memo = %d, %v; want none: the one of 5 has no memo", total, err)
	}
	yes := true
	found, total, err = db.Entry.FindMany(ctx, &EntryFilter{AmountCents: &Int64Filter{In: []int64{5, 6}}, Memo: &StringFilter{IsNull: &yes}}, &EntryFindOptions{Fields: EntryFields{Id: true}})
	if err != nil || total != 1 || *found[0].Id != *fresh.Id {
		t.Fatalf("entries of 5 or 6 with no memo = %+v, %d, %v; want the fresh one", found, total, err)
	}
}
`
