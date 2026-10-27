package sqlmigrate

import (
	"slices"
	"strings"
	"testing"
)

const (
	destructive   = HazardDestructive
	blockingClass = HazardBlocking
	compat        = HazardCompat
	dataDependent = HazardDataDependent
	apiBreaking   = HazardAPIBreaking
	history       = HazardHistory
	rawSQL        = HazardRawSQL
)

// TestHazardClasses checks the hazard classes of each operation: every
// step of the case whose op (and subject, when given) matches has exactly
// these classes. A wrong class reads as an expected change in a golden;
// here it fails.
func TestHazardClasses(t *testing.T) {
	checkHazardClasses(t, planCases, []hazardTest{
		{plan: "from-empty", op: "createTable", phase: Expand},
		{plan: "from-empty", op: "createIndex", phase: Expand},
		{plan: "from-empty", op: "addForeignKey", phase: Expand},

		{plan: "add-column", op: "addColumn", phase: Expand},
		{plan: "add-required-column", op: "addColumn", phase: Expand, want: []HazardClass{compat, dataDependent}},
		{plan: "add-required-column-with-default", op: "addColumn", phase: Expand},
		{plan: "drop-column", op: "dropColumn", phase: Contract, want: []HazardClass{destructive, apiBreaking}, reason: "Order.note"},
		{plan: "drop-required-column", op: "dropNotNull", phase: Expand},
		{plan: "drop-required-column", op: "dropColumn", phase: Contract, want: []HazardClass{destructive}},
		{plan: "rename-column-as-drop-add", op: "dropColumn", phase: Contract, want: []HazardClass{destructive},
			reason: "--rename order.customer_id=order.buyer_id"},
		{plan: "rename-column", op: "renameColumn", phase: Expand, want: []HazardClass{compat, apiBreaking}},
		{plan: "rename-column", op: "renameConstraint", phase: Expand},
		{plan: "rename-column", op: "renameIndex", phase: Expand},
		{plan: "rename-unique-column", op: "renameConstraint", subject: "table/customer/constraint/customer_contact_email_key", phase: Expand},
		{plan: "retype-column", op: "alterColumnType", phase: Expand, want: []HazardClass{compat, apiBreaking}},
		{plan: "retype-column-cast", op: "alterColumnType", phase: Expand, want: []HazardClass{blockingClass, compat, dataDependent}},
		{plan: "retype-column-narrow", op: "alterColumnType", phase: Expand, want: []HazardClass{destructive, blockingClass, compat}},
		{plan: "retype-text-to-jsonb", op: "alterColumnType", phase: Expand, want: []HazardClass{blockingClass, compat, dataDependent},
			reason: "Order.shipTo changes from TEXT to JSONB: the cast fails on a value JSONB cannot hold."},
		{plan: "make-column-required", op: "addNotNullCheck", phase: Contract},
		{plan: "make-column-required", op: "validateNotNullCheck", phase: Contract, want: []HazardClass{dataDependent}},
		{plan: "make-column-required", op: "setNotNull", phase: Contract},
		{plan: "make-column-optional", op: "dropNotNull", phase: Expand},

		{plan: "add-table", op: "createTable", phase: Expand},
		{plan: "drop-table", op: "dropTable", phase: Contract, want: []HazardClass{destructive}},
		{plan: "drop-tables-in-cycle", op: "dropForeignKey", phase: Contract},
		{plan: "drop-tables-in-cycle", op: "dropTable", phase: Contract, want: []HazardClass{destructive}},
		{plan: "rename-table-as-drop-add", op: "dropTable", subject: "table/label", phase: Contract, want: []HazardClass{destructive},
			reason: "--rename label=tag"},
		{plan: "rename-table", op: "renameTable", phase: Expand, want: []HazardClass{compat}},
		{plan: "rename-table", op: "renameColumn", phase: Expand, want: []HazardClass{compat}},
		{plan: "rename-table-with-dependents", op: "renameTable", subject: "table/purchase", phase: Expand,
			want: []HazardClass{compat, apiBreaking}},
		// A changed @table or @column renames as a --rename does, and says
		// what it follows (D85).
		{plan: "rename-table-by-override", op: "renameTable", subject: "table/purchaseOrder", phase: Expand,
			want: []HazardClass{compat, apiBreaking}, reason: "Table order is renamed to purchaseOrder, following Order's @table"},
		{plan: "rename-table-by-override", op: "renameColumn", phase: Expand, want: []HazardClass{compat}, reason: "following Order's @table"},
		{plan: "rename-column-by-override", op: "renameColumn", subject: "table/customer/column/emailAddress", phase: Expand,
			want: []HazardClass{compat}, reason: "customer.email is renamed to customer.emailAddress, following Customer.email's @column"},
		{plan: "rename-column-by-override", op: "renameColumn", subject: "table/order/column/customerId", phase: Expand,
			want: []HazardClass{compat}, reason: "following Order.customer's @column"},
		{plan: "rename-column-by-override", op: "renameConstraint", phase: Expand},
		{plan: "rename-versioned-table-by-override", op: "renameColumn", subject: "table/purchaseOrder/column/orderNote", phase: Expand,
			want: []HazardClass{compat, history}},
		{plan: "rename-versioned-table-by-override", op: "renameFunction", phase: Expand},
		{plan: "rename-versioned-table", op: "renameFunction", phase: Expand},
		{plan: "rename-versioned-table", op: "renameTrigger", phase: Expand},
		{plan: "rename-versioned-table", op: "replaceFunction", phase: Expand},

		{plan: "add-index", op: "createIndex", phase: Expand},
		{plan: "add-unique-index", op: "createIndex", phase: Expand, want: []HazardClass{compat, dataDependent}},
		{plan: "drop-index", op: "dropIndex", phase: Contract},
		{plan: "add-unique", op: "buildUniqueIndex", phase: Expand, want: []HazardClass{compat, dataDependent}},
		{plan: "add-unique", op: "addUnique", phase: Expand},
		{plan: "drop-unique", op: "dropUnique", phase: Contract},

		{plan: "add-relation", op: "addForeignKey", phase: Expand},
		{plan: "add-relation", op: "validateForeignKey", phase: Expand},
		{plan: "relation-on-existing-column", op: "addForeignKey", phase: Contract},
		{plan: "relation-on-existing-column", op: "validateForeignKey", phase: Contract, want: []HazardClass{dataDependent}},
		{plan: "change-on-delete", op: "replaceForeignKey", phase: Contract},
		{plan: "change-on-delete-to-set-null", op: "replaceForeignKey", phase: Contract},
		{plan: "drop-relation", op: "dropForeignKey", phase: Contract},
		{plan: "add-join-table", op: "createTable", phase: Expand},
		{plan: "drop-join-table", op: "dropTable", phase: Contract, want: []HazardClass{destructive}},

		{plan: "add-search-field", op: "addColumn", phase: Expand, want: []HazardClass{blockingClass}},
		{plan: "change-search-fields", op: "regenerateColumn", phase: Expand, want: []HazardClass{blockingClass}},
		{plan: "drop-search-field", op: "dropColumn", phase: Contract},
		{plan: "retype-search-field", op: "alterColumnType", phase: Expand, want: []HazardClass{blockingClass, compat}},

		{plan: "versioned-on", op: "addColumn", phase: Expand},
		{plan: "versioned-on", op: "seedHistory", phase: Expand, want: []HazardClass{blockingClass}},
		{plan: "versioned-off", op: "dropTrigger", phase: Contract},
		{plan: "versioned-off", op: "dropFunction", phase: Contract},
		{plan: "versioned-off", op: "dropTable", phase: Contract, want: []HazardClass{destructive}},
		{plan: "versioned-retention", op: "createFunction", phase: Expand},
		{plan: "versioned-retention-change", op: "replaceFunction", phase: Expand},
		{plan: "versioned-prune-keep", op: "replaceFunction", phase: Expand},
		{plan: "versioned-exclude", op: "replaceFunction", phase: Expand, want: []HazardClass{history}},
		{plan: "versioned-partitioned-retention", op: "createIndex", phase: Expand, want: []HazardClass{blockingClass}},
		{plan: "versioned-retype", op: "alterColumnType", phase: Expand,
			want: []HazardClass{destructive, blockingClass, compat, apiBreaking, history}},
		{plan: "optimistic-on", op: "createTrigger", phase: Expand},
		{plan: "optimistic-off", op: "dropTrigger", phase: Contract},
		{plan: "optimistic-to-versioned", op: "seedHistory", phase: Expand, want: []HazardClass{blockingClass}},
		{plan: "versioned-to-optimistic", op: "replaceTrigger", phase: Contract},

		{plan: "add-default", op: "setDefault", phase: Expand},
		{plan: "drop-default", op: "dropDefault", phase: Contract},
		{plan: "add-enum-check", op: "addCheck", phase: Contract},
		{plan: "add-enum-check", op: "validateCheck", phase: Contract, want: []HazardClass{dataDependent},
			reason: "Order.status gets a CHECK in the database: validating ck_order_status fails when a row of order holds a value other than 'placed', 'shipped'."},
		{plan: "add-column-with-check", op: "addColumn", phase: Expand},
		{plan: "add-column-with-check", op: "addCheck", phase: Expand},
		{plan: "add-column-with-check", op: "validateCheck", phase: Expand},
		{plan: "widen-enum-check", op: "replaceCheck", phase: Expand},
		{plan: "widen-enum-check", op: "validateCheck", phase: Expand},
		{plan: "narrow-enum-check", op: "replaceCheck", phase: Contract},
		{plan: "narrow-enum-check", op: "validateCheck", phase: Contract, want: []HazardClass{dataDependent}, reason: "holds 'cancelled'"},
		{plan: "change-validate-check", op: "dropCheck", phase: Expand},
		{plan: "change-validate-check", op: "addCheck", phase: Contract},
		{plan: "change-validate-check", op: "validateCheck", phase: Contract, want: []HazardClass{dataDependent}, reason: "longer than 50 characters"},
		{plan: "drop-validate-check", op: "dropCheck", phase: Expand},
		{plan: "drop-checked-column", op: "dropColumn", phase: Contract, want: []HazardClass{destructive}},

		{plan: "add-expression-index", op: "createIndex", phase: Expand},
		{plan: "add-index-in-transaction", op: "createIndex", phase: Expand, want: []HazardClass{blockingClass}, reason: "concurrently: false"},
		{plan: "add-partial-brin-index", op: "createIndex", phase: Expand},
		{plan: "add-opclass-index", op: "createIndex", phase: Expand},

		{plan: "add-raw-sql", op: "runRawSQL", phase: Expand, want: []HazardClass{rawSQL}, reason: "runs for the first time"},
		{plan: "change-raw-sql", op: "runRawSQL", phase: Expand, want: []HazardClass{rawSQL}, reason: "changed: the step runs its statements again"},
		{plan: "drop-raw-sql", op: "undoRawSQL", phase: Contract, want: []HazardClass{rawSQL}, reason: "the step runs its down"},
		{plan: "drop-raw-sql-without-down", op: "undoRawSQL", subject: "rawSql/order_code_lower", phase: Contract, want: []HazardClass{rawSQL},
			reason: "has no down: nothing undoes what it made"},

		{plan: "add-queue-key", op: "createIndex", subject: "table/order_shipped_queue/index/uq_order_shipped_queue_ready_key", phase: Expand,
			want: []HazardClass{destructive, compat, dataDependent}, reason: "but one per order_id, the latest enqueued, which replace keeps"},
		{plan: "add-queue-key", op: "createIndex", subject: "table/order_shipped_queue/index/uq_order_shipped_queue_claimed_key", phase: Expand,
			want: []HazardClass{dataDependent}, reason: "while two claims hold one order_id"},
		{plan: "change-queue-key", op: "createIndex", phase: Expand},
		{plan: "change-queue-key", op: "dropIndex", phase: Contract},
		{plan: "add-enqueue-on-change", op: "createFunction", phase: Expand},
		{plan: "add-enqueue-on-change", op: "createTrigger", phase: Expand},
		{plan: "change-enqueue-on-change", op: "replaceFunction", phase: Expand},
		{plan: "change-enqueue-on-change", op: "replaceTrigger", phase: Expand},
		{plan: "drop-enqueue-on-change", op: "dropTrigger", phase: Contract},
		{plan: "drop-enqueue-on-change", op: "dropFunction", phase: Contract},
		// The backfill holds the new trigger's lock while it enqueues a
		// message per row (D88).
		{plan: "add-enqueue-on-change-backfill", op: "createTrigger", phase: Expand, want: []HazardClass{blockingClass},
			reason: "stack queue backfill"},
		{plan: "add-enqueue-on-change-backfill-per-key", op: "createTrigger", phase: Expand, want: []HazardClass{blockingClass}},

		// A required timestamp's DEFAULT now is declared (D88): dropping
		// the one a compiler before gave it, and adding a declared one,
		// take no hazard.
		{plan: "drop-implied-now-default", op: "dropDefault", phase: Contract},
		{plan: "add-now-default", op: "setDefault", phase: Expand},

		// The roles @seedRoles keeps (D88): an insert takes no hazard; an
		// update replaces the role's permissions, in expand when it only
		// gains and in contract when it loses one.
		{plan: "add-seed-roles", op: "seedRole", phase: Expand},
		{plan: "seed-roles-gain", op: "updateRole", phase: Expand, want: []HazardClass{destructive}, reason: "gains identity.users.write"},
		{plan: "seed-roles-lose", op: "updateRole", phase: Contract, want: []HazardClass{destructive}, reason: "loses identity.users.write"},

		{plan: "add-projection", op: "createView", phase: Expand},
		{plan: "change-projection", op: "replaceView", phase: Expand, want: []HazardClass{apiBreaking}},
		{plan: "drop-projection", op: "dropView", phase: Contract, want: []HazardClass{apiBreaking}},
		{plan: "projection-owner", op: "replaceView", phase: Expand},

		{plan: "graph-content-retype", op: "alterColumnType", phase: Expand,
			want: []HazardClass{blockingClass, compat, dataDependent, history}, reason: "rose from 1 to 2"},
		{plan: "graph-content-add", op: "addColumn", phase: Expand, want: []HazardClass{history}, reason: "stays 1"},
		{plan: "graph-content-exclude", op: "changeGraphContent", phase: Expand, want: []HazardClass{history},
			reason: "Note.body leaves the content of note in version graph Recipe: commits made before this change hash and merge rows of the old shape. The graph's schemaEpoch rose from 1 to 2."},
	})
}

// hazardTest gives the hazard classes of every step of a plan case with an
// op (and a subject, when given) in a phase.
type hazardTest struct {
	plan    string
	op      string
	subject string
	want    []HazardClass
	phase   Phase
	// reason is text a hazard's reason must contain.
	reason string
}

// checkHazardClasses plans the cases the tests name and checks each test:
// a step matches, and every matching step has exactly the classes it
// lists.
func checkHazardClasses(t *testing.T, cases []planCase, tests []hazardTest) {
	t.Helper()
	plans := map[string]*Plan{}
	for _, pc := range cases {
		plans[pc.name] = nil
	}
	for _, tc := range tests {
		t.Run(tc.plan+"/"+string(tc.phase)+"/"+tc.op, func(t *testing.T) {
			if _, ok := plans[tc.plan]; !ok {
				t.Fatalf("no plan case %s", tc.plan)
			}
			if plans[tc.plan] == nil {
				for _, pc := range cases {
					if pc.name == tc.plan {
						plans[tc.plan] = pc.plan(t)
					}
				}
			}
			matched := 0
			for _, step := range plans[tc.plan].Steps {
				if step.Op != tc.op || step.Phase != tc.phase || (tc.subject != "" && step.Subject != tc.subject) {
					continue
				}
				matched++
				var got []HazardClass
				reasons := ""
				for _, h := range step.Hazards {
					if !slices.Contains(got, h.Class) {
						got = append(got, h.Class)
					}
					reasons += h.Reason + "\n"
					if h.ID != HazardID(h.Class, h.Subject, h.Reader) {
						t.Errorf("hazard %s has the wrong id", h.ID)
					}
				}
				if !slices.Equal(got, tc.want) {
					t.Errorf("step %d %s has hazards %v, want %v", step.Index, step.Subject, got, tc.want)
				}
				if tc.reason != "" && !strings.Contains(reasons, tc.reason) {
					t.Errorf("step %d %s: no reason says %q:\n%s", step.Index, step.Subject, tc.reason, reasons)
				}
			}
			if matched == 0 {
				t.Errorf("no %s step in %s", tc.op, tc.phase)
			}
		})
	}
}

// TestPhases checks that no expand step follows a contract step and that
// steps are numbered from 1, in every plan case.
func TestPhases(t *testing.T) {
	for _, pc := range planCases {
		plan := pc.plan(t)
		contract := false
		for i, step := range plan.Steps {
			if step.Index != i+1 {
				t.Errorf("%s: step %d has index %d", pc.name, i+1, step.Index)
			}
			if step.Phase == Contract {
				contract = true
			} else if contract {
				t.Errorf("%s: expand step %d follows a contract step", pc.name, step.Index)
			}
			if !step.Transactional && len(step.Recovery) == 0 {
				t.Errorf("%s: step %d runs outside a transaction with no recovery", pc.name, step.Index)
			}
			for _, stmt := range step.Statements {
				if strings.HasSuffix(strings.TrimSpace(stmt), ";") {
					t.Errorf("%s: step %d has a statement with a trailing semicolon", pc.name, step.Index)
				}
				if strings.Contains(strings.ToLower(stmt), "lock_timeout") {
					t.Errorf("%s: step %d sets lock_timeout, which the runner sets", pc.name, step.Index)
				}
			}
		}
	}
}
