package ormgen

import (
	"os"
	"strings"
	"testing"
)

// TestUniqueViolationOnPostgres builds, vets and tests the ORM module of
// versionBatchSchema with uniqueViolationORMTest. The generated
// UniqueViolation and IsUniqueViolation read Postgres errors built by hand,
// wrapped and joined. With SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL set, they
// also read the error a duplicate insert returns through a generated
// repository.
func TestUniqueViolationOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir, createSQL := generateVersionBatchModule(t)
	out := runGeneratedORMModule(t, ormDir, createSQL, "unique_violation_test.go", uniqueViolationORMTest)
	names := []string{"TestIsUniqueViolationReadsTheSQLStateAndTheConstraint", "TestUniqueViolationReturnsThePostgresError"}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" {
		names = append(names, "TestADuplicateInsertIsAUniqueViolationOnPostgres")
	}
	for _, name := range names {
		if !strings.Contains(out, "--- PASS: "+name) {
			t.Fatalf("the generated ORM module did not pass %s", name)
		}
	}
}

const uniqueViolationORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/version-batch-recipes"
)

func TestIsUniqueViolationReadsTheSQLStateAndTheConstraint(t *testing.T) {
	unique := &pgconn.PgError{Code: "23505", TableName: "kitchen", ConstraintName: "uq_kitchen_name"}
	foreignKey := &pgconn.PgError{Code: "23503", TableName: "kitchen", ConstraintName: "uq_kitchen_name"}
	cases := []struct {
		name        string
		err         error
		constraints []string
		want        bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "an error that is not a Postgres error", err: errors.New("duplicate key value violates unique constraint"), want: false},
		{name: "a unique violation", err: unique, want: true},
		{name: "a unique violation wrapped with %w", err: fmt.Errorf("create kitchen: %w", unique), want: true},
		{name: "a unique violation joined to another error", err: errors.Join(errors.New("rollback failed"), unique), want: true},
		{name: "a foreign key violation", err: foreignKey, want: false},
		{name: "a check violation", err: &pgconn.PgError{Code: "23514"}, want: false},
		{name: "an integrity violation of no named class", err: &pgconn.PgError{Code: "23000"}, want: false},
		{name: "a unique violation on the named constraint", err: unique, constraints: []string{"uq_kitchen_name"}, want: true},
		{name: "a wrapped unique violation on one of the named constraints", err: fmt.Errorf("create kitchen: %w", unique), constraints: []string{"kitchen_name_key", "uq_kitchen_name"}, want: true},
		{name: "a unique violation on another constraint", err: unique, constraints: []string{"kitchen_name_key"}, want: false},
		{name: "a foreign key violation on the named constraint", err: foreignKey, constraints: []string{"uq_kitchen_name"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUniqueViolation(tc.err, tc.constraints...); got != tc.want {
				t.Fatalf("IsUniqueViolation(%v, %q) = %v, want %v", tc.err, tc.constraints, got, tc.want)
			}
		})
	}
}

func TestUniqueViolationReturnsThePostgresError(t *testing.T) {
	unique := &pgconn.PgError{Code: "23505", TableName: "kitchen", ConstraintName: "uq_kitchen_name"}
	got, ok := UniqueViolation(fmt.Errorf("create kitchen: %w", unique))
	if !ok || got != unique {
		t.Fatalf("UniqueViolation(wrapped) = %v, %v; want the wrapped error, true", got, ok)
	}
	for _, err := range []error{nil, errors.New("plain"), &pgconn.PgError{Code: "23503"}} {
		if got, ok := UniqueViolation(err); ok || got != nil {
			t.Fatalf("UniqueViolation(%v) = %v, %v; want nil, false", err, got, ok)
		}
	}
}

func TestADuplicateInsertIsAUniqueViolationOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to insert a duplicate through the generated ORM")
	}
	db, _ := openRecipesDatabase(t, dsn)
	ctx := context.Background()
	first, err := db.GetKitchenRepository().CreateOne(ctx, &types.Kitchen{Name: "Bakery"})
	if err != nil {
		t.Fatalf("create the first kitchen: %v", err)
	}
	_, err = db.GetKitchenRepository().CreateOne(ctx, &types.Kitchen{Id: first.Id, Name: "Bakery again"})
	pgErr, ok := UniqueViolation(err)
	if !ok {
		t.Fatalf("a second kitchen with the first one's id answered %v, want a unique violation", err)
	}
	if pgErr.TableName != "kitchen" || !IsUniqueViolation(err, pgErr.ConstraintName) || IsUniqueViolation(err, "no_such_constraint") {
		t.Fatalf("the violation names table %q and constraint %q; IsUniqueViolation does not match it by name", pgErr.TableName, pgErr.ConstraintName)
	}
}
` + versionedORMTestHelpers
