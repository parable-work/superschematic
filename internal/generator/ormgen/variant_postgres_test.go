package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// variantFixture is the fixture whose Step holds a variant field (D149).
const variantFixture = "fixture-variant-field-db"

// TestVariantFieldOnPostgres generates the ORM module of
// fixture-variant-field-db, then builds, vets and tests it. Its generated
// test, against the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (it
// skips without the database), writes steps through the repository and
// reads each back with its result as the member its kind names, through
// GetOne, a selection of fields and FindMany, and a note with none; a step
// whose result its kind names no member for is refused by Validate and by
// the column's CHECK, ck_step_result.
func TestVariantFieldOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, variantFixture)
	if err := os.WriteFile(filepath.Join(ormDir, "variant_test.go"), []byte(variantORMTest), 0o644); err != nil {
		t.Fatalf("write variant test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestVariantFieldOnPostgres") {
		t.Fatalf("the generated ORM did not run TestVariantFieldOnPostgres:\n%s", out)
	}
}

var variantORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-variant-field-db"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestVariantFieldOnPostgres(t *testing.T) {` + strings.Replace(pageTestSetup, "PREFIX", "variant_orm", 1) + `
	ctx = WithUserID(ctx, types.NewUUID())
	notes := "fine"

	verify, err := db.Step.CreateOne(ctx, &types.Step{Title: "lint", Kind: types.StepKind_Verify, Result: types.VerifyResult{Passed: true}})
	if err != nil {
		t.Fatalf("create a verify step: %v", err)
	}
	review, err := db.Step.CreateOne(ctx, &types.Step{Title: "read", Kind: types.StepKind_Review, Result: types.ReviewResult{Verdict: types.Verdict_Approved, Notes: notes}})
	if err != nil {
		t.Fatalf("create a review step: %v", err)
	}
	note, err := db.Step.CreateOne(ctx, &types.Step{Title: "aside", Kind: types.StepKind_Note})
	if err != nil {
		t.Fatalf("create a note: %v", err)
	}

	// Each read decodes the member the row's kind names.
	check := func(label string, row *types.Step) {
		t.Helper()
		switch row.Kind {
		case types.StepKind_Verify:
			if result, ok := row.Result.(types.VerifyResult); !ok || !result.Passed {
				t.Errorf("%s: a verify step's result = %#v, want its VerifyResult", label, row.Result)
			}
		case types.StepKind_Review:
			if result, ok := row.Result.(types.ReviewResult); !ok || result.Verdict != types.Verdict_Approved {
				t.Errorf("%s: a review step's result = %#v, want its ReviewResult", label, row.Result)
			}
		case types.StepKind_Note:
			if row.Result != nil {
				t.Errorf("%s: a note's result = %#v, want none", label, row.Result)
			}
		}
		if errs := row.Validate(); errs.HasErrors() {
			t.Errorf("%s: the row read back is invalid: %v", label, errs)
		}
	}
	for _, step := range []*types.Step{verify, review, note} {
		row, err := db.Step.GetOne(ctx, *step.Id, nil)
		if err != nil {
			t.Fatalf("get %s: %v", step.Title, err)
		}
		check("GetOne "+step.Title, row)
		row, err = db.Step.GetOne(ctx, *step.Id, &StepGetOptions{Fields: StepFields{Result: true, Kind: true}})
		if err != nil {
			t.Fatalf("get %s's result and kind: %v", step.Title, err)
		}
		check("a selection of "+step.Title, row)
	}
	rows, _, err := db.Step.FindMany(ctx, nil, &StepFindOptions{Fields: StepFields{Id: true, Title: true, Kind: true, Result: true}})
	if err != nil {
		t.Fatalf("list the steps: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("listed %d steps, want 3", len(rows))
	}
	for _, row := range rows {
		check("FindMany "+row.Title, row)
	}

	// A result its kind names no member for is refused by the column's
	// CHECK; Validate refuses it first.
	stray := &types.Step{Title: "stray", Kind: types.StepKind_Note, Result: types.VerifyResult{Passed: true}}
	if errs := stray.Validate(); !errs.HasErrors() {
		t.Errorf("Validate passes a note with a result")
	}
	_, err = db.Step.CreateOne(ctx, stray)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "ck_step_result" {
		t.Errorf("a note with a result: got %v, want check_violation on ck_step_result", err)
	}
	_ = fmt.Sprint
	_ = time.Now
}
`
