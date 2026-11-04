package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedExpressionKeyUpsertOnPostgres generates the ORM module for
// fixture-ddl-db, then builds, vets and tests it. Its generated test,
// against the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (it skips
// without the database), upserts a Category by label, a unique index over
// lower(label) (D83): an input of another case of a held label conflicts
// with it, and an upsert whose update sets nothing returns the held row
// with its own label, which the key's keep leaves as it is.
func TestGeneratedExpressionKeyUpsertOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, "fixture-ddl-db")
	if err := os.WriteFile(filepath.Join(ormDir, "upsert_expression_test.go"), []byte(upsertExpressionORMTest), 0o644); err != nil {
		t.Fatalf("write upsert test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestExpressionKeyUpsertOnPostgres") {
		t.Fatalf("the generated ORM did not run TestExpressionKeyUpsertOnPostgres:\n%s", out)
	}
}

var upsertExpressionORMTest = `package orm

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-ddl-db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExpressionKeyUpsertOnPostgres(t *testing.T) {` + strings.Replace(pageTestSetup, "PREFIX", "upsert_expression_orm", 1) + `
	groceries, err := db.Category.CreateOne(ctx, &types.Category{Label: "Groceries", Kind: "personal"})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	kept, err := db.Category.UpsertOneResult(ctx, &types.Category{Label: "GROCERIES", Kind: "business"}, CategoryUniqueByLabel, nil, nil)
	if err != nil || kept.Status != UpsertUnchanged || *kept.Row.Id != *groceries.Id || kept.Row.Label != "Groceries" || kept.Row.Kind != "personal" {
		t.Fatalf("an upsert of GROCERIES that updates nothing = %+v, %v; want Groceries as it was", kept.Row, err)
	}
	stored, err := db.Category.GetOne(ctx, *groceries.Id, nil)
	if err != nil || stored.Label != "Groceries" {
		t.Fatalf("the category after the upsert = %+v, %v; want its label Groceries", stored, err)
	}
}
`
