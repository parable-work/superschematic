package orm

// The Go repositories of fixture-ledger-db on a SQLite file and on
// Postgres, result for result (D116): one script of calls, every method
// of a repository among them, runs through each dialect's DatabaseInterface,
// over the DDL the build writes for it, and what each call answers on
// SQLite is what it answers on Postgres, whose statements the TypeScript
// and Rust repositories are held to (the table parity tests).
//
// A row's key is named by the script, so the two databases hold the same
// rows. What a database stamps by its clock, createdAt, updatedAt and
// deletedAt, is compared as set or unset, a time as its instant, and a
// refusal by its kind, since each database words its own.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-ledger-db"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"
)

// resultCall is a call of the script and what it answered.
type resultCall struct {
	name   string
	answer any
}

const ada = "11111111-1111-4111-8111-111111111111"

// key is the key of the script's row n.
func key(n int) types.IdentityUUID {
	id, err := types.ParseIdentityUUID(fmt.Sprintf("00000000-0000-4000-8000-0000000000%02x", n))
	if err != nil {
		panic(err)
	}
	return id
}

func keyPtr(n int) *types.IdentityUUID {
	id := key(n)
	return &id
}

// stamped are the members a database stamps by its own clock.
var stamped = map[string]bool{"createdAt": true, "updatedAt": true, "deletedAt": true}

// compared is value's JSON with each stamp as whether it is set, and each
// time as its instant in UTC.
func compared(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for member, inner := range v {
			if stamped[member] {
				if inner == nil {
					out[member] = "unset"
				} else {
					out[member] = "set"
				}
				continue
			}
			out[member] = compared(inner)
		}
		for member := range stamped {
			if _, ok := out[member]; !ok {
				if _, has := v["id"]; has {
					out[member] = "unset"
				}
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, inner := range v {
			out[i] = compared(inner)
		}
		return out
	case string:
		if strings.Contains(v, "T") && len(v) >= 20 {
			if at, err := time.Parse(time.RFC3339Nano, v); err == nil {
				return at.UTC().Format(time.RFC3339Nano)
			}
		}
	}
	return value
}

// refusal is an error as both dialects share it: its kind.
func refusal(err error) string {
	var pg *pgconn.PgError
	text := err.Error()
	switch {
	case errors.Is(err, ErrNotFound):
		return "not found"
	case errors.Is(err, ErrNoUserInContext):
		return "no user"
	case errors.Is(err, ErrPageKey):
		return "page key"
	case errors.As(err, &pg) && pg.Code == "23505", strings.Contains(text, "UNIQUE constraint failed"):
		return "unique"
	case errors.As(err, &pg) && pg.Code == "23503", strings.Contains(text, "FOREIGN KEY constraint failed"):
		return "foreign key"
	case errors.As(err, &pg) && pg.Code == "23502", strings.Contains(text, "NOT NULL constraint failed"):
		return "not null"
	case strings.Contains(text, "both value and null assignment"):
		return "value and null"
	case strings.Contains(text, "fields must be specified"):
		return "no fields"
	case strings.Contains(text, "rolled back"):
		return "rolled back"
	}
	return "error: " + text
}

// answer is what a call answered, as the two dialects are compared.
func answer(value any, err error) any {
	if err != nil {
		return map[string]any{"refused": refusal(err)}
	}
	data, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return map[string]any{"unmarshalable": marshalErr.Error()}
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return map[string]any{"undecodable": err.Error()}
	}
	return map[string]any{"ok": compared(decoded)}
}

// sortedMap is a GetManyByIDs map as a list in key order.
func sortedMap[T any](m map[types.IdentityUUID]T) []T {
	keys := make([]string, 0, len(m))
	byText := map[string]T{}
	for k, v := range m {
		keys = append(keys, k.String())
		byText[k.String()] = v
	}
	sort.Strings(keys)
	out := make([]T, len(keys))
	for i, k := range keys {
		out[i] = byText[k]
	}
	return out
}

// script runs the calls through db and answers each.
func script(t *testing.T, db DatabaseInterface) []resultCall {
	t.Helper()
	user, err := ParseUserID(ada)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithUserID(context.Background(), user)
	var calls []resultCall
	call := func(name string, value any, err error) {
		calls = append(calls, resultCall{name: name, answer: answer(value, err)})
	}
	str := func(s string) *string { return &s }
	day := func(d string) *types.TemporalDate { v := types.TemporalDate(d); return &v }
	cents := func(n int64) *int64 { return &n }
	no, yes := false, true
	accounts := db.GetAccountRepository()
	entries := db.GetEntryRepository()
	allAccounts := AccountFields{}.All()
	allEntries := EntryFields{}.All()
	cash, bank, card := 1, 2, 3

	// Accounts: creates, reads, a search, pages in both orders.
	row, err := accounts.CreateOne(ctx, &types.Account{Id: keyPtr(cash), Name: "Cash", Code: "cash"})
	call("createAccount", row, err)
	rows, err := accounts.CreateMany(ctx, []*types.Account{
		{Id: keyPtr(bank), Name: "Bank", Code: "bank", OpenedOn: day("2026-10-01")},
		{Id: keyPtr(card), Name: "Card", Code: "card", MailingAddress: &types.MailingAddress{Line1: "1 Main St", City: "Springfield"}},
	})
	call("createAccounts", rows, err)
	row, err = accounts.CreateOne(ctx, &types.Account{Id: keyPtr(9), Name: "Again", Code: "cash"})
	call("duplicateAccount", row, err)
	row, err = accounts.CreateOne(context.Background(), &types.Account{Id: keyPtr(10), Name: "Nobody", Code: "nobody"})
	call("createAsNoOne", row, err)
	row, err = accounts.GetOne(ctx, key(cash), nil)
	call("getAccount", row, err)
	row, err = accounts.GetOne(ctx, key(99), nil)
	call("getNoAccount", row, err)
	row, err = accounts.GetOne(ctx, key(bank), &AccountGetOptions{Fields: AccountFields{Name: true, OpenedOn: true}})
	call("getAccountFields", row, err)
	byID, err := accounts.GetManyByIDs(ctx, []types.IdentityUUID{key(cash), key(bank), key(cash), key(99)})
	call("getAccounts", sortedMap(byID), err)
	row, err = accounts.FindOne(ctx, &AccountFilter{Code: &StringFilter{Eq: str("bank")}}, nil)
	call("findAccount", row, err)
	row, err = accounts.FindOne(ctx, &AccountFilter{Code: &StringFilter{Eq: str("none")}}, nil)
	call("findNoAccount", row, err)
	found, total, err := accounts.FindMany(ctx, &AccountFilter{Name: &StringFilter{ILike: str("a")}, SearchText: str("ca"), ReferencedByEntryAccount: &ReferencedByFilter{Exists: false}},
		&AccountFindOptions{Fields: allAccounts, OrderBy: []AccountOrderBy{{Field: AccountOrderByCode, Desc: true}}, Limit: 10, Offset: 1})
	call("findAccounts", map[string]any{"rows": found, "total": total}, err)
	found, total, err = accounts.FindMany(ctx, nil, &AccountFindOptions{})
	call("findAccountsNoFields", map[string]any{"rows": found, "total": total}, err)
	found, total, err = accounts.FindMany(ctx, &AccountFilter{AnyOf: []AccountFilter{{Code: &StringFilter{Eq: str("cash")}}, {OpenedOn: &DateFilter{IsNull: &no}}}},
		&AccountFindOptions{Fields: allAccounts, OrderBy: []AccountOrderBy{{Field: AccountOrderByCode}}})
	call("findAccountsAnyOf", map[string]any{"rows": found, "total": total}, err)
	found, total, err = accounts.FindMany(ctx, &AccountFilter{AnyOf: []AccountFilter{{Code: &StringFilter{Eq: str("cash")}}, {}}, AllOf: []AccountFilter{{Name: &StringFilter{Ne: str("Bank")}}}},
		&AccountFindOptions{Fields: allAccounts, OrderBy: []AccountOrderBy{{Field: AccountOrderByCode}}, SkipCount: true})
	call("findAccountsAllOf", map[string]any{"rows": found, "total": total}, err)
	byName := func(after *AccountPageKey) *AccountPageOptions {
		return &AccountPageOptions{Fields: allAccounts, Sort: AccountSort{Field: AccountSortByName}, Limit: 2, After: after}
	}
	first, err := accounts.FindPage(ctx, nil, byName(nil))
	call("pageAccounts", first, err)
	var next *AccountPageKey
	if first != nil {
		next = first.Next
	}
	page, err := accounts.FindPage(ctx, nil, byName(next))
	call("pageAccountsAfter", page, err)
	page, err = accounts.FindPage(ctx, &AccountFilter{Code: &StringFilter{In: []string{"bank", "card"}}}, &AccountPageOptions{Fields: AccountFields{Code: true}, Sort: AccountSort{Field: AccountSortById, Desc: true}, Limit: 1})
	call("pageAccountsByKey", page, err)
	page, err = accounts.FindPage(ctx, nil, &AccountPageOptions{Fields: allAccounts, Sort: AccountSort{Field: AccountSortByOpenedOn}, Limit: 2})
	call("pageAccountsByOpening", page, err)
	page, err = accounts.FindPage(ctx, nil, &AccountPageOptions{Fields: allAccounts, Sort: AccountSort{Field: AccountSortByName}, After: &AccountPageKey{Id: key(99)}})
	call("pageAfterNoRow", page, err)

	// Accounts: updates of one row and of many, and upserts that update,
	// insert and leave a row as it was.
	box := types.IdentityName("Cash box")
	row, err = accounts.UpdateOne(ctx, key(cash), &AccountUpdate{Name: &box})
	call("updateAccount", row, err)
	row, err = accounts.UpdateOne(ctx, key(99), &AccountUpdate{Name: &box})
	call("updateNoAccount", row, err)
	row, err = accounts.UpdateOne(ctx, key(cash), &AccountUpdate{})
	call("updateNothing", row, err)
	row, err = accounts.UpdateOne(ctx, key(card), &AccountUpdate{MailingAddressSetNull: true, OpenedOn: day("2026-09-30")})
	call("updateToNull", row, err)
	n, err := accounts.UpdateMany(ctx, &AccountFilter{Code: &StringFilter{In: []string{"bank", "card"}}}, &AccountUpdate{OpenedOn: day("2026-10-02")})
	call("updateAccounts", n, err)
	again := types.IdentityName("Cash again")
	upserted, err := accounts.UpsertOneResult(ctx, &types.Account{Id: keyPtr(8), Name: "Cash", Code: "cash"}, AccountUniqueByCode, &AccountUpdate{Name: &again}, nil)
	call("upsertAccount", upserted, err)
	upserted, err = accounts.UpsertOneResult(ctx, &types.Account{Id: keyPtr(8), Name: "Cash", Code: "cash"}, AccountUniqueByCode, &AccountUpdate{Name: &again}, &UpsertOptions{OnlyIfChanged: true})
	call("upsertAccountUnchanged", upserted, err)
	loan := types.IdentityName("Loan again")
	upserted, err = accounts.UpsertOneResult(ctx, &types.Account{Id: keyPtr(4), Name: "Loan", Code: "loan", OpenedOn: day("2026-09-30")}, AccountUniqueByCode, &AccountUpdate{Name: &loan}, nil)
	call("upsertNewAccount", upserted, err)
	upserted, err = accounts.UpsertOneResult(ctx, &types.Account{Id: keyPtr(4), Name: "Loan", Code: "loan"}, AccountUniqueByCode, &AccountUpdate{}, nil)
	call("upsertSetsNothing", upserted, err)
	row, err = accounts.UpsertOne(ctx, &types.Account{Id: keyPtr(5), Name: "Save", Code: "save"}, AccountUniqueById, &AccountUpdate{Name: &loan})
	call("upsertByKey", row, err)
	fund := types.IdentityName("Fund")
	upserted, err = accounts.UpsertOneResult(ctx, &types.Account{Id: keyPtr(6), Name: fund, Code: "fund"}, AccountUniqueByCode, &AccountUpdate{Name: &fund}, nil)
	call("upsertFund", upserted, err)

	// Entries: creates, the filters of each kind of column, the negated
	// ones, and pages across a column's nulls.
	reviewed := types.TemporalDateTime(time.Date(2026, 10, 25, 9, 15, 0, 0, time.UTC))
	metadata := types.GenericJSON(`{"source":"bank","n":1}`)
	entry, err := entries.CreateOne(ctx, &types.Entry{Id: keyPtr(11), Account: types.Account{Id: keyPtr(cash)}, AmountCents: -1250, BookedOn: "2026-10-25", Memo: "Coffee", Tags: []string{"food"}, ReviewedAt: &reviewed, Metadata: &metadata})
	call("createEntry", entry, err)
	entryRows, err := entries.CreateMany(ctx, []*types.Entry{
		{Id: keyPtr(12), Account: types.Account{Id: keyPtr(bank)}, AmountCents: 500, BookedOn: "2026-10-26", Tags: []string{}, ExternalId: "x1", Kind: types.EntryKind_Credit},
		{Id: keyPtr(13), Account: types.Account{Id: keyPtr(card)}, Transfer: &types.Account{Id: keyPtr(cash)}, AmountCents: -75, BookedOn: "2026-10-27", Tags: []string{"a", "b"}, Memo: "Transfer"},
	})
	call("createEntries", entryRows, err)
	entry, err = entries.CreateOne(ctx, &types.Entry{Id: keyPtr(19), Account: types.Account{Id: keyPtr(99)}, AmountCents: 1, BookedOn: "2026-10-28", Tags: []string{}})
	call("entryOfNoAccount", entry, err)
	entry, err = entries.GetOne(ctx, key(11), nil)
	call("getEntry", entry, err)
	entry, err = entries.GetOne(ctx, key(13), &EntryGetOptions{Fields: allEntries})
	call("getEntryHydrated", entry, err)
	entry, err = entries.GetOne(ctx, key(13), &EntryGetOptions{Fields: EntryFields{Memo: true, Transfer: true, TransferNested: AccountFields{Code: true}}})
	call("getEntryNested", entry, err)
	entry, err = entries.FindOne(ctx, &EntryFilter{
		AmountCents: &Int64Filter{Between: &Int64Range{Min: -2000, Max: 0}},
		BookedOn:    &DateFilter{Gte: str("2026-10-01")},
		Kind:        &StringFilter{In: []string{"debit"}},
		Memo:        &StringFilter{IsNull: &no},
		AccountID:   &UUIDFilter{Eq: keyPtr(cash)},
		SearchText:  str("coffee"),
	}, nil)
	call("findEntry", entry, err)
	byAmount := &EntryFindOptions{Fields: allEntries, OrderBy: []EntryOrderBy{{Field: EntryOrderByAmountCents}}}
	entryList, total, err := entries.FindMany(ctx, &EntryFilter{ReviewedAt: &DateTimeFilter{IsNull: &yes}, TransferID: &UUIDFilter{IsNull: &yes}, AmountCents: &Int64Filter{In: []int64{500, -1250}}}, byAmount)
	call("findEntries", map[string]any{"rows": entryList, "total": total}, err)
	entryList, total, err = entries.FindMany(ctx, &EntryFilter{Metadata: &StringFilter{Eq: str(`{ "n": 1.0, "source": "bank" }`)}}, byAmount)
	call("findEntriesByMetadata", map[string]any{"rows": entryList, "total": total}, err)
	entryList, total, err = entries.FindMany(ctx, &EntryFilter{Metadata: &StringFilter{In: []string{`{"source":"x"}`, `{"n":1,"source":"bank"}`}}}, byAmount)
	call("findEntriesByMetadataIn", map[string]any{"rows": entryList, "total": total}, err)
	entryList, total, err = entries.FindMany(ctx, &EntryFilter{Metadata: &StringFilter{Ne: str(`{"n":1,"source":"bank"}`)}}, byAmount)
	call("findEntriesByMetadataNe", map[string]any{"rows": entryList, "total": total}, err)
	lte := time.Date(2026, 10, 25, 11, 15, 0, 0, time.FixedZone("", 2*3600))
	entryList, total, err = entries.FindMany(ctx, &EntryFilter{ReviewedAt: &DateTimeFilter{Lte: &lte}}, &EntryFindOptions{Fields: allEntries, SkipCount: true})
	call("findEntriesReviewed", map[string]any{"rows": entryList, "total": total}, err)
	entryList, total, err = entries.FindMany(ctx, &EntryFilter{
		AmountCents: &Int64Filter{Ne: cents(1), NotIn: []int64{500, -75}},
		BookedOn:    &DateFilter{Ne: str("2026-10-26")},
		Kind:        &StringFilter{Ne: str("credit")},
		Memo:        &StringFilter{Ne: str("Transfer"), NotIn: []string{"Refund", "Fee"}},
		TransferID:  &UUIDFilter{Ne: keyPtr(cash), NotIn: []types.IdentityUUID{key(bank)}},
	}, byAmount)
	call("findEntriesNegated", map[string]any{"rows": entryList, "total": total}, err)
	entryList, total, err = entries.FindMany(ctx, &EntryFilter{Account: &AccountFilter{Code: &StringFilter{In: []string{"cash", "card"}}}}, byAmount)
	call("findEntriesThroughAccount", map[string]any{"rows": entryList, "total": total}, err)
	entryPage, err := entries.FindPage(ctx, &EntryFilter{AccountID: &UUIDFilter{In: []types.IdentityUUID{key(cash), key(bank)}}}, &EntryPageOptions{Fields: allEntries, Sort: EntrySort{Field: EntrySortByAmountCents, Desc: true}, Limit: 1})
	call("pageEntries", entryPage, err)
	entryPage, err = entries.FindPage(ctx, nil, &EntryPageOptions{Fields: allEntries, Sort: EntrySort{Field: EntrySortByMemo, Desc: true}, After: &EntryPageKey{Id: key(11)}, Limit: 2})
	call("pageEntriesByMemo", entryPage, err)
	entryPage, err = entries.FindPage(ctx, nil, &EntryPageOptions{Fields: allEntries, Sort: EntrySort{Field: EntrySortByMemo}, After: &EntryPageKey{Id: key(12)}, Limit: 2})
	call("pageEntriesAfterANull", entryPage, err)
	entryPage, err = entries.FindPage(ctx, nil, &EntryPageOptions{Fields: allEntries, Sort: EntrySort{Field: EntrySortByReviewedAt}, Limit: 5})
	call("pageEntriesByReview", entryPage, err)

	// Entries: updates, upserts, deletes and transactions.
	amount := types.GenericInt64(-1300)
	entry, err = entries.UpdateOne(ctx, key(11), &EntryUpdate{MemoSetNull: true, AmountCents: &amount, TransferID: keyPtr(bank), Tags: &[]string{"food", "drink"}})
	call("updateEntry", entry, err)
	memo := "both"
	entry, err = entries.UpdateOne(ctx, key(11), &EntryUpdate{Memo: &memo, MemoSetNull: true})
	call("updateValueAndNull", entry, err)
	credit := types.EntryKind_Credit
	n, err = entries.UpdateMany(ctx, &EntryFilter{AccountID: &UUIDFilter{In: []types.IdentityUUID{key(bank), key(card)}}}, &EntryUpdate{Kind: &credit})
	call("updateEntries", n, err)
	six := types.GenericInt64(600)
	upsert := func() *types.Entry {
		return &types.Entry{Id: keyPtr(14), Account: types.Account{Id: keyPtr(bank)}, ExternalId: "x1", AmountCents: 600, BookedOn: "2026-10-26", Tags: []string{}, Kind: types.EntryKind_Credit}
	}
	entryUpsert, err := entries.UpsertOneResult(ctx, upsert(), EntryUniqueByAccountExternalId, &EntryUpdate{AmountCents: &six}, nil)
	call("upsertEntry", entryUpsert, err)
	entryUpsert, err = entries.UpsertOneResult(ctx, upsert(), EntryUniqueByAccountExternalId, &EntryUpdate{}, nil)
	call("upsertEntryKeep", entryUpsert, err)
	call("deleteEntry", nil, entries.DeleteOne(ctx, key(11)))
	call("deleteEntryAgain", nil, entries.DeleteOne(ctx, key(11)))
	call("deleteAsNoOne", nil, entries.DeleteOne(context.Background(), key(12)))
	entry, err = entries.GetOne(ctx, key(11), nil)
	call("getDeletedEntry", entry, err)
	n, err = entries.DeleteMany(ctx, &EntryFilter{AccountID: &UUIDFilter{Eq: keyPtr(card)}})
	call("deleteEntries", n, err)
	entry, err = entries.FindOne(ctx, &EntryFilter{AccountID: &UUIDFilter{Eq: keyPtr(card)}}, &EntryFindOptions{IncludeDeleted: true})
	call("findDeleted", entry, err)
	entryUpsert, err = entries.UpsertOneResult(ctx, &types.Entry{Id: keyPtr(15), Account: types.Account{Id: keyPtr(cash)}, AmountCents: 1, BookedOn: "2026-10-30", Tags: []string{}}, EntryUniqueById, &EntryUpdate{AmountCents: &six}, nil)
	call("upsertNewEntry", entryUpsert, err)
	entryUpsert, err = entries.UpsertOneResult(ctx, &types.Entry{Id: keyPtr(13), Account: types.Account{Id: keyPtr(card)}, AmountCents: 1, BookedOn: "2026-10-30", Tags: []string{}}, EntryUniqueById, &EntryUpdate{AmountCents: &six}, nil)
	call("upsertDeletedEntry", entryUpsert, err)
	call("hardDeleteEntry", nil, entries.HardDeleteOne(ctx, key(11)))
	call("hardDeleteNoEntry", nil, entries.HardDeleteOne(ctx, key(11)))
	err = db.Transaction(ctx, func(tx TxInterface) error {
		_, err := tx.GetEntryRepository().CreateOne(ctx, &types.Entry{Id: keyPtr(16), Account: types.Account{Id: keyPtr(bank)}, AmountCents: 4200, BookedOn: "2026-10-28", Tags: []string{}})
		return err
	})
	call("transaction", nil, err)
	err = db.Transaction(ctx, func(tx TxInterface) error {
		if _, err := tx.GetEntryRepository().CreateOne(ctx, &types.Entry{Id: keyPtr(17), Account: types.Account{Id: keyPtr(bank)}, AmountCents: 1, BookedOn: "2026-10-28", Tags: []string{}}); err != nil {
			return err
		}
		return errors.New("rolled back")
	})
	call("transactionRolledBack", nil, err)
	err = db.Transaction(ctx, func(tx TxInterface) error {
		nested := tx.Transaction(ctx, func(inner TxInterface) error {
			if _, err := inner.GetEntryRepository().CreateOne(ctx, &types.Entry{Id: keyPtr(18), Account: types.Account{Id: keyPtr(bank)}, AmountCents: 2, BookedOn: "2026-10-28", Tags: []string{}}); err != nil {
				return err
			}
			return errors.New("rolled back")
		})
		if nested == nil {
			return errors.New("the savepoint did not roll back")
		}
		_, err := tx.GetEntryRepository().CreateOne(ctx, &types.Entry{Id: keyPtr(20), Account: types.Account{Id: keyPtr(bank)}, AmountCents: 3, BookedOn: "2026-10-28", Tags: []string{}})
		return err
	})
	call("nestedTransaction", nil, err)
	byIDs, err := entries.GetManyByIDs(ctx, []types.IdentityUUID{key(16), key(17), key(18), key(20)})
	call("afterTransactions", sortedMap(byIDs), err)
	call("deleteAccountInUse", nil, accounts.DeleteOne(ctx, key(card)))
	call("deleteAccount", nil, accounts.DeleteOne(ctx, key(6)))
	n, err = accounts.DeleteMany(ctx, &AccountFilter{Code: &StringFilter{In: []string{"loan", "save"}}})
	call("deleteAccounts", n, err)
	found, total, err = accounts.FindMany(ctx, nil, &AccountFindOptions{Fields: allAccounts, OrderBy: []AccountOrderBy{{Field: AccountOrderByCode}}})
	call("accountsAtTheEnd", map[string]any{"rows": found, "total": total}, err)
	entryList, total, err = entries.FindMany(ctx, nil, &EntryFindOptions{Fields: allEntries, OrderBy: []EntryOrderBy{{Field: EntryOrderById}}, IncludeDeleted: true})
	call("entriesAtTheEnd", map[string]any{"rows": entryList, "total": total}, err)
	return calls
}

// TestRepositoryResultsOnSQLiteAndPostgres runs the script on both
// dialects and requires the same answer of each call.
func TestRepositoryResultsOnSQLiteAndPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to hold the repositories on SQLite to Postgres")
	}
	ctx := context.Background()

	// Postgres, in a schema of its own.
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("sqlite_results_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	postgres, err := ConnectWithPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer postgres.Close()

	// SQLite, a file the SQLite DDL creates.
	sqliteDDL, err := os.ReadFile("testdata/sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "ledger.sqlite")
	raw, err := sql.Open("sqlite", SQLiteDSN(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, string(sqliteDDL)); err != nil {
		t.Fatalf("the SQLite DDL: %v", err)
	}
	lite, err := OpenSQLite(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer lite.Close()

	onPostgres := script(t, postgres)
	onSQLite := script(t, lite)

	// An upsert of many rows is refused on SQLite until the repositories
	// write sqlutil's statement of a batch.
	user, err := ParseUserID(ada)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lite.GetAccountRepository().UpsertMany(WithUserID(ctx, user), []*types.Account{{Id: keyPtr(7), Name: "Many", Code: "many"}}, AccountUniqueByCode, AccountFields{Name: true}, nil); !errors.Is(err, ErrSQLiteUnsupported) {
		t.Errorf("UpsertMany on SQLite = %v, want ErrSQLiteUnsupported", err)
	}
	if len(onPostgres) != len(onSQLite) {
		t.Fatalf("Postgres answered %d calls and SQLite %d", len(onPostgres), len(onSQLite))
	}
	refusals := 0
	for i, pg := range onPostgres {
		lt := onSQLite[i]
		if !reflect.DeepEqual(pg.answer, lt.answer) {
			pgJSON, _ := json.MarshalIndent(pg.answer, "", "  ")
			ltJSON, _ := json.MarshalIndent(lt.answer, "", "  ")
			t.Errorf("%s: the answers differ\nPostgres:\n%s\nSQLite:\n%s", pg.name, pgJSON, ltJSON)
		}
		if m, ok := pg.answer.(map[string]any); ok {
			if r, refused := m["refused"].(string); refused {
				refusals++
				if strings.HasPrefix(r, "error: ") {
					t.Errorf("%s: Postgres refused with an error of no kind: %s", pg.name, r)
				}
			}
		}
	}
	t.Logf("SQLite and Postgres answered %d calls alike, %d of them refusals", len(onPostgres), refusals)
}
