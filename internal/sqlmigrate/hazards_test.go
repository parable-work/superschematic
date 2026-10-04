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
		{plan: "make-column-required", op: "addNotNullCheck", phase: Contract},
		{plan: "make-column-required", op: "validateNotNullCheck", phase: Contract, want: []HazardClass{dataDependent}},
		{plan: "make-column-required", op: "setNotNull", phase: Contract},
		{plan: "make-column-optional", op: "dropNotNull", phase: Expand},

		{plan: "add-table", op: "createTable", phase: Expand},
		{plan: "drop-table", op: "dropTable", phase: Contract, want: []HazardClass{destructive}},
		{plan: "rename-table-as-drop-add", op: "dropTable", subject: "table/label", phase: Contract, want: []HazardClass{destructive},
			reason: "--rename label=tag"},
		{plan: "rename-table", op: "renameTable", phase: Expand, want: []HazardClass{compat}},
		{plan: "rename-table", op: "renameColumn", phase: Expand, want: []HazardClass{compat}},
		{plan: "rename-table-with-dependents", op: "renameTable", subject: "table/purchase", phase: Expand,
			want: []HazardClass{compat, apiBreaking}},
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

		{plan: "add-projection", op: "createView", phase: Expand},
		{plan: "change-projection", op: "replaceView", phase: Expand, want: []HazardClass{apiBreaking}},
		{plan: "drop-projection", op: "dropView", phase: Contract, want: []HazardClass{apiBreaking}},
		{plan: "projection-owner", op: "replaceView", phase: Expand},

		{plan: "graph-content-retype", op: "alterColumnType", phase: Expand,
			want: []HazardClass{blockingClass, compat, dataDependent, history}, reason: "rose from 1 to 2"},
		{plan: "graph-content-add", op: "addColumn", phase: Expand, want: []HazardClass{history}, reason: "stays 1"},
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
