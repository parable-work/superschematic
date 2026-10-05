package sqlmigrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Hashes in the hand-built plans are placeholders: the renderings print
// them and never check them.
var (
	renderFromHash     = strings.Repeat("a1", 32)
	renderToHash       = strings.Repeat("b2", 32)
	renderPlanHash     = strings.Repeat("c3", 32)
	renderExpandedHash = strings.Repeat("d4", 32)
)

// renderPlans are hand-built plans covering what the renderings show:
// both phases and the model between them, renames, hazards with and
// without a reader, a step outside a transaction with its recovery, a step
// of several statements, a statement over several lines with semicolons in
// its body, a step with no statements, a SQLite rebuild with foreign keys
// off, and a plan with no steps.
func renderPlans() map[string]*Plan {
	return map[string]*Plan{
		"expand-contract": {
			Version:       PlanVersion,
			Dialect:       Postgres,
			Service:       "shop-db",
			From:          renderFromHash,
			To:            renderToHash,
			ToModel:       json.RawMessage(`{}`),
			Expanded:      renderExpandedHash,
			ExpandedModel: json.RawMessage(`{}`),
			Renames:       []string{"purchase=order", "order.amount=order.total_cents"},
			Hash:          renderPlanHash,
			Steps: []*Step{
				{
					Index: 1, Phase: Expand, Op: "renameTable", Subject: "table/order",
					Statements:    []string{`ALTER TABLE "purchase" RENAME TO "order"`},
					Transactional: true,
					Hazards: []*Hazard{{
						ID: "compat:table/order", Class: HazardCompat, Subject: "table/order",
						Reason: "purchase is renamed to order: a server built from the previous version still names purchase.",
					}},
				},
				{
					Index: 2, Phase: Expand, Op: "addColumn", Subject: "table/order/column/note",
					Statements:    []string{`ALTER TABLE "order" ADD COLUMN "note" TEXT`},
					Transactional: true,
				},
				{
					Index: 3, Phase: Expand, Op: "createIndex", Subject: "table/order/index/order_note_idx",
					Statements: []string{`CREATE INDEX CONCURRENTLY "order_note_idx" ON "order" ("note")`},
					Recovery:   []string{`DROP INDEX CONCURRENTLY IF EXISTS "order_note_idx"`},
				},
				{
					Index: 4, Phase: Expand, Op: "replaceFunction", Subject: "function/order_history_capture",
					Statements: []string{strings.Join([]string{
						`CREATE OR REPLACE FUNCTION "order_history_capture"() RETURNS TRIGGER AS $$`,
						`BEGIN`,
						`  INSERT INTO "order_history" SELECT NEW.*;`,
						`  RETURN NEW;`,
						`END;`,
						`$$ LANGUAGE plpgsql`,
					}, "\n")},
					Transactional: true,
				},
				{
					Index: 5, Phase: Expand, Op: "changeGraphContent", Subject: "table/step",
					Statements:    []string{},
					Transactional: true,
					Hazards: []*Hazard{{
						ID: "history:table/step", Class: HazardHistory, Subject: "table/step",
						Reason: "Step.scratch joins the content of step in version graph Recipe: commits made before this change hash and merge rows of the old shape. The graph's schemaEpoch stays 1.",
					}},
				},
				{
					Index: 6, Phase: Contract, Op: "setNotNull", Subject: "table/order/column/note",
					Statements: []string{
						`ALTER TABLE "order" VALIDATE CONSTRAINT "order_note_not_null"`,
						`ALTER TABLE "order" ALTER COLUMN "note" SET NOT NULL`,
						`ALTER TABLE "order" DROP CONSTRAINT "order_note_not_null"`,
					},
					Transactional: true,
					Hazards: []*Hazard{{
						ID: "data-dependent:table/order/column/note", Class: HazardDataDependent, Subject: "table/order/column/note",
						Reason: "Order.note becomes required: the step fails while a row has no note.",
					}},
				},
				{
					Index: 7, Phase: Contract, Op: "dropColumn", Subject: "table/order/column/total",
					Statements:    []string{`ALTER TABLE "order" DROP COLUMN "total"`},
					Transactional: true,
					Hazards: []*Hazard{
						{
							ID: "destructive:table/order/column/total", Class: HazardDestructive, Subject: "table/order/column/total",
							Reason: "Order.total is dropped with its data.",
						},
						{
							ID:    "api-breaking:table/order/column/total@shop-api/OrderView.total",
							Class: HazardAPIBreaking, Subject: "table/order/column/total", Reader: "shop-api/OrderView.total",
							Reason: "shop-api reads order.total through OrderView.total | a pipe stays in its cell.",
						},
					},
				},
			},
		},
		"sqlite-rebuild": {
			Version: PlanVersion,
			Dialect: SQLite,
			Service: "edge-db",
			From:    renderFromHash,
			To:      renderToHash,
			ToModel: json.RawMessage(`{}`),
			Hash:    renderPlanHash,
			Steps: []*Step{{
				Index: 1, Phase: Expand, Op: "copyTable", Subject: "table/reading",
				Statements: []string{
					`CREATE TABLE "reading_new" ("id" TEXT NOT NULL PRIMARY KEY, "value" REAL NOT NULL)`,
					`INSERT INTO "reading_new" ("id", "value") SELECT "id", "value" FROM "reading"`,
					`DROP TABLE "reading"`,
					`ALTER TABLE "reading_new" RENAME TO "reading"`,
				},
				Transactional:  true,
				ForeignKeysOff: true,
				Hazards: []*Hazard{
					{ID: "blocking:table/reading", Class: HazardBlocking, Subject: "table/reading", Reason: "Reading.value changes from INTEGER to REAL: SQLite rebuilds the table."},
					{ID: "copy-table:table/reading", Class: HazardCopyTable, Subject: "table/reading", Reason: "SQLite's ALTER TABLE cannot retype a column."},
				},
			}},
		},
		"no-steps": {
			Version: PlanVersion,
			Dialect: Postgres,
			Service: "shop-db",
			To:      renderToHash,
			ToModel: json.RawMessage(`{}`),
			Hash:    renderPlanHash,
		},
	}
}

func TestPlanRenderingsGolden(t *testing.T) {
	for name, plan := range renderPlans() {
		t.Run(name, func(t *testing.T) {
			checkRenderGolden(t, filepath.Join("testdata", "render", name+".sql"), plan.SQL())
			checkRenderGolden(t, filepath.Join("testdata", "render", name+".md"), plan.Markdown())
		})
	}
}

func TestPlanSQLEndsEveryStatement(t *testing.T) {
	plan := renderPlans()["expand-contract"]
	sql := plan.SQL()
	for _, step := range plan.Steps {
		for _, statement := range step.Statements {
			assert.Contains(t, sql, statement+";\n")
		}
	}
	assert.Contains(t, sql, "-- Step 3, expand, createIndex table/order/index/order_note_idx\n-- not in a transaction")
	assert.Contains(t, sql, "-- hazard api-breaking:table/order/column/total@shop-api/OrderView.total\n")
	assert.Contains(t, sql, "-- hazard history:table/step\n-- no SQL: the database does not change, and the runner logs the step\n")
}

func TestPlanMarkdownHazardTable(t *testing.T) {
	md := renderPlans()["expand-contract"].Markdown()
	assert.Contains(t, md, "| api-breaking | `table/order/column/total` | `shop-api/OrderView.total` | shop-api reads order.total through OrderView.total \\| a pipe stays in its cell. | `api-breaking:table/order/column/total@shop-api/OrderView.total` |\n")
	assert.Contains(t, md, "7 steps (5 expand, 2 contract), 5 hazards (1 destructive, 1 compat, 1 data-dependent, 1 api-breaking, 1 history).")
	assert.Contains(t, md, "**5. changeGraphContent** `table/step`. Hazards: history. No SQL: the database does not change.\n")
}

func TestPlanUnallowed(t *testing.T) {
	plan := renderPlans()["expand-contract"]
	ids := func(hazards []*Hazard) []string {
		var out []string
		for _, hazard := range hazards {
			out = append(out, hazard.ID)
		}
		return out
	}

	assert.Empty(t, plan.Unallowed(nil, nil), "no classes fail on nothing")
	assert.Equal(t, []string{
		"compat:table/order",
		"destructive:table/order/column/total",
	}, ids(plan.Unallowed([]HazardClass{HazardDestructive, HazardCompat}, nil)), "in step order, not class order")
	assert.Equal(t, []string{
		"destructive:table/order/column/total",
	}, ids(plan.Unallowed([]HazardClass{HazardDestructive, HazardCompat}, []string{"compat:table/order", "compat:table/elsewhere"})))
	assert.Equal(t, []string{
		"compat:table/order",
		"history:table/step",
		"data-dependent:table/order/column/note",
		"destructive:table/order/column/total",
		"api-breaking:table/order/column/total@shop-api/OrderView.total",
	}, ids(plan.Unallowed(HazardClasses, nil)))
	assert.Empty(t, plan.Unallowed([]HazardClass{HazardAPIBreaking}, []string{"api-breaking:table/order/column/total@shop-api/OrderView.total"}))
	// An id names one reader: allowing another reader's hazard allows
	// nothing here.
	assert.Len(t, plan.Unallowed([]HazardClass{HazardAPIBreaking}, []string{"api-breaking:table/order/column/total@admin-api/OrderRow.total"}), 1)
}

func checkRenderGolden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden %s; run go test -update", path)
	assert.Equal(t, string(want), got, "golden %s; run go test -update and review the diff", path)
}
