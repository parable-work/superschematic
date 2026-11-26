package ormgen

import (
	"os"
	"strings"
	"testing"
)

// TestCommitHooksOnPostgres builds, vets and tests the ORM module of
// versionBatchSchema with commitHooksORMTest against the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (D166): the functions a
// transaction's AfterCommit adds run after its commit, in order, through
// Transaction, TransactionWith, ReadOnlyTransaction and a nested
// Transaction, and an error, a panic or a rollback runs none of them.
// TestScheduleTablesOnPostgres holds RunEvent, the other path that hands a
// Tx to the caller's code.
func TestCommitHooksOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the commit hooks against Postgres")
	}
	ormDir, createSQL := generateVersionBatchModule(t)
	out := runGeneratedORMModule(t, ormDir, createSQL, "commit_hooks_test.go", commitHooksORMTest)
	for _, name := range []string{
		"TestAfterCommitRunsAfterTheCommitOnPostgres",
		"TestAfterCommitRunsNothingOnARollbackOnPostgres",
		"TestAfterCommitHookOnPostgres",
		"TestAfterCommitInASavepointOnPostgres",
		"TestAfterCommitOnEveryCommitPathOnPostgres",
	} {
		if !strings.Contains(out, "--- PASS: "+name) {
			t.Fatalf("the generated ORM module did not run %s", name)
		}
	}
}

const commitHooksORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/version-batch-recipes"
)

type hookContextKey struct{}

// kitchens counts the kitchens of a name, read on pool, outside any
// transaction.
func kitchens(t *testing.T, pool *pgxpool.Pool, name string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM kitchen WHERE name = $1", name).Scan(&n); err != nil {
		t.Fatalf("count kitchens: %v", err)
	}
	return n
}

func TestAfterCommitRunsAfterTheCommitOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM against Postgres")
	}
	db, pool := openRecipesDatabase(t, dsn)
	ctx := context.WithValue(context.Background(), hookContextKey{}, "the transaction's")

	var ran []string
	var seen int
	var seenContext any
	var during, after time.Duration
	var held *Tx
	err := db.Transaction(ctx, func(tx TxInterface) error {
		held = tx.(*Tx)
		if _, err := tx.GetKitchenRepository().CreateOne(ctx, &types.Kitchen{Name: "Bakery"}); err != nil {
			return err
		}
		held.AfterCommit(func(ctx context.Context) {
			ran = append(ran, "first")
			// Another connection sees the transaction's write.
			seen = kitchens(t, pool, "Bakery")
			seenContext = ctx.Value(hookContextKey{})
			after = held.CommitDuration()
		})
		held.AfterCommit(func(context.Context) { ran = append(ran, "second") })
		during = held.CommitDuration()
		if len(ran) != 0 {
			return fmt.Errorf("a function ran before the commit: %v", ran)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if !reflect.DeepEqual(ran, []string{"first", "second"}) {
		t.Fatalf("the functions ran as %v, want first then second, before Transaction returned", ran)
	}
	if seen != 1 || seenContext != "the transaction's" {
		t.Fatalf("the first function saw %d kitchens and context value %v, want 1 and the transaction's context", seen, seenContext)
	}
	if during != 0 || after <= 0 || held.CommitDuration() != after {
		t.Fatalf("CommitDuration = %v before the commit, %v after it, %v once Transaction returned; want zero, then one positive duration", during, after, held.CommitDuration())
	}
}

func TestAfterCommitRunsNothingOnARollbackOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM against Postgres")
	}
	db, pool := openRecipesDatabase(t, dsn)
	ctx := context.Background()
	ran := 0

	// An error rolls back and runs nothing.
	failed := errors.New("the oven is cold")
	err := db.Transaction(ctx, func(tx TxInterface) error {
		if _, err := tx.GetKitchenRepository().CreateOne(ctx, &types.Kitchen{Name: "Failed"}); err != nil {
			return err
		}
		tx.(*Tx).AfterCommit(func(context.Context) { ran++ })
		return failed
	})
	if !errors.Is(err, failed) || ran != 0 || kitchens(t, pool, "Failed") != 0 {
		t.Fatalf("Transaction = %v, %d functions ran, %d kitchens; want its error, none run and none written", err, ran, kitchens(t, pool, "Failed"))
	}

	// A panic rolls back, runs nothing and goes on up.
	func() {
		defer func() {
			if p := recover(); p != "dropped the tray" {
				t.Fatalf("recovered %v, want the panic", p)
			}
		}()
		_ = db.Transaction(ctx, func(tx TxInterface) error {
			if _, err := tx.GetKitchenRepository().CreateOne(ctx, &types.Kitchen{Name: "Panicked"}); err != nil {
				return err
			}
			tx.(*Tx).AfterCommit(func(context.Context) { ran++ })
			panic("dropped the tray")
		})
	}()
	if ran != 0 || kitchens(t, pool, "Panicked") != 0 {
		t.Fatalf("after a panic %d functions ran and %d kitchens are written, want none", ran, kitchens(t, pool, "Panicked"))
	}

	// The commit fails when the transaction already failed, and runs
	// nothing either.
	err = db.Transaction(ctx, func(tx TxInterface) error {
		tx.(*Tx).AfterCommit(func(context.Context) { ran++ })
		_, _ = tx.Exec(ctx, "SELECT 1/0")
		return nil
	})
	if err == nil || ran != 0 {
		t.Fatalf("Transaction over a failed statement = %v, %d functions ran; want a commit error and none run", err, ran)
	}
}

// countingHook counts the writes added to it and its runs.
type countingHook struct {
	writes int
	runs   int
}

func (h *countingHook) AfterCommit(context.Context) { h.runs++ }

type writesKey struct{}
type otherKey struct{}

func TestAfterCommitHookOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM against Postgres")
	}
	db, _ := openRecipesDatabase(t, dsn)
	ctx := context.Background()

	made := 0
	var writes, other *countingHook
	err := db.Transaction(ctx, func(tx TxInterface) error {
		for range 3 {
			hook := tx.(*Tx).AfterCommitHook(writesKey{}, func() CommitHook {
				made++
				return &countingHook{}
			})
			counter := hook.(*countingHook)
			if writes != nil && counter != writes {
				return fmt.Errorf("AfterCommitHook returned %p, then %p, for one key", writes, counter)
			}
			writes = counter
			writes.writes++
		}
		other = tx.(*Tx).AfterCommitHook(otherKey{}, func() CommitHook { return &countingHook{} }).(*countingHook)
		if writes.runs != 0 {
			return fmt.Errorf("a hook ran before the commit")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if made != 1 || writes.writes != 3 || writes.runs != 1 {
		t.Fatalf("one key made %d hooks, added %d writes and ran %d times; want 1, 3 and 1", made, writes.writes, writes.runs)
	}
	if other == writes || other.runs != 1 {
		t.Fatalf("another key's hook is %p (the first is %p) and ran %d times; want its own, run once", other, writes, other.runs)
	}

	// The next transaction holds no hook of the last one's.
	err = db.Transaction(ctx, func(tx TxInterface) error {
		next := tx.(*Tx).AfterCommitHook(writesKey{}, func() CommitHook { return &countingHook{} }).(*countingHook)
		if next == writes {
			return fmt.Errorf("a new transaction returned the last one's hook")
		}
		return nil
	})
	if err != nil || writes.runs != 1 {
		t.Fatalf("the next Transaction = %v and the last one's hook ran %d times, want it run once", err, writes.runs)
	}
}

func TestAfterCommitInASavepointOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM against Postgres")
	}
	db, pool := openRecipesDatabase(t, dsn)
	ctx := context.Background()

	var ran []string
	var outer *Tx
	var nestedDuration time.Duration
	err := db.Transaction(ctx, func(tx TxInterface) error {
		outer = tx.(*Tx)
		outer.AfterCommit(func(context.Context) { ran = append(ran, "before") })
		hook := outer.AfterCommitHook(writesKey{}, func() CommitHook { return &countingHook{} })
		// A nested Transaction that fails rolls back to its savepoint and
		// keeps every function: the ones added before it and in it.
		failed := errors.New("the dough did not rise")
		if err := tx.Transaction(ctx, func(nested TxInterface) error {
			inner := nested.(*Tx)
			if _, err := nested.GetKitchenRepository().CreateOne(ctx, &types.Kitchen{Name: "Rolled back"}); err != nil {
				return err
			}
			inner.AfterCommit(func(context.Context) {
				ran = append(ran, "inside")
				nestedDuration = inner.CommitDuration()
			})
			if inner.AfterCommitHook(writesKey{}, func() CommitHook { return &countingHook{} }) != hook {
				return fmt.Errorf("the nested Tx holds another hook under the key")
			}
			return failed
		}); !errors.Is(err, failed) {
			return fmt.Errorf("nested Transaction = %v, want its error", err)
		}
		outer.AfterCommit(func(context.Context) { ran = append(ran, "after") })
		_, err := tx.GetKitchenRepository().CreateOne(ctx, &types.Kitchen{Name: "Kept"})
		return err
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if !reflect.DeepEqual(ran, []string{"before", "inside", "after"}) {
		t.Fatalf("the functions ran as %v, want before, inside, after", ran)
	}
	if kitchens(t, pool, "Rolled back") != 0 || kitchens(t, pool, "Kept") != 1 {
		t.Fatalf("the savepoint's write was kept or the transaction's was lost")
	}
	if nestedDuration <= 0 || nestedDuration != outer.CommitDuration() {
		t.Fatalf("the nested Tx's CommitDuration = %v, the outer's %v; want the one commit's", nestedDuration, outer.CommitDuration())
	}
}

func TestAfterCommitOnEveryCommitPathOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM against Postgres")
	}
	db, pool := openRecipesDatabase(t, dsn)
	ctx := context.Background()

	// TransactionWith runs them after its commit, and its rollback none.
	seen := -1
	err := db.TransactionWith(ctx, TxOptions{Isolation: Serializable}, func(tx TxInterface) error {
		if _, err := tx.GetKitchenRepository().CreateOne(ctx, &types.Kitchen{Name: "Serializable"}); err != nil {
			return err
		}
		tx.(*Tx).AfterCommit(func(context.Context) { seen = kitchens(t, pool, "Serializable") })
		return nil
	})
	if err != nil || seen != 1 {
		t.Fatalf("TransactionWith = %v and its function saw %d kitchens, want it run after the commit", err, seen)
	}
	ran := false
	failed := errors.New("refused")
	err = db.TransactionWith(ctx, TxOptions{ReadOnly: true}, func(tx TxInterface) error {
		tx.(*Tx).AfterCommit(func(context.Context) { ran = true })
		return failed
	})
	if !errors.Is(err, failed) || ran {
		t.Fatalf("TransactionWith = %v and ran %t, want its error and nothing run", err, ran)
	}

	// ReadOnlyTransaction's Tx is a *Tx too.
	var committed time.Duration
	err = db.ReadOnlyTransaction(ctx, func(tx ReadOnlyTxInterface) error {
		held := tx.(*Tx)
		held.AfterCommit(func(context.Context) { committed = held.CommitDuration() })
		return nil
	})
	if err != nil || committed <= 0 {
		t.Fatalf("ReadOnlyTransaction = %v and its function read CommitDuration %v, want it run after the commit", err, committed)
	}

	// The no-op transaction is no *Tx, so a caller's type assertion finds
	// no AfterCommit there.
	if err := NewNoOpDatabase().Transaction(ctx, func(tx TxInterface) error {
		if _, ok := tx.(interface{ AfterCommit(func(context.Context)) }); ok {
			return fmt.Errorf("the no-op transaction has AfterCommit")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
` + versionedORMTestHelpers
