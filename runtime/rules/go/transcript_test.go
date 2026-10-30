package rules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// The recorded scenarios are the specification (D103): the transcript
// superschematic-rules-testkit writes from core-workflow.json
// (runtime/engine/testdata/rules/core-workflow.transcript.json, whose
// format its transcript module states) replayed through this adapter on
// SQLite and on Postgres, as the TypeScript adapter replays it
// (runtime/http/typescript/src/rules-transcript.test.ts). Each scenario
// gets a table per type and database, its own fields' columns beside the
// owned columns the layout rule gives each use of any version and
// _version, with @optimistic's trigger; each check runs on the core alone,
// answering whether the type has rows from its table; each command runs
// through Execute in a transaction of its own with the version live at it,
// its refusal compared whole and the row it names read back after it and
// compared; and each raw step writes owned columns with SQL past the
// adapter. The Postgres replay skips without
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL.
//
// The tables here follow the layout rule as the core's README states it,
// restated by this test (the corpus's uses are type-anchored: Workflow's
// workflow_status and workflow_entered_at). The layout the generator writes
// (internal/generator/behaviorlayout) is held by the generated packages'
// own end-to-end tests, not by this one.

// transcriptFloor is the steps the transcript holds, the testkit's floor
// (tests/transcript.rs): each dialect replays every one, so a regression
// cannot pass by skipping.
const transcriptFloor = 96

const transcriptPath = "../../engine/testdata/rules/core-workflow.transcript.json"

type transcript struct {
	File      string     `json:"file"`
	Behaviors []string   `json:"behaviors"`
	Scenarios []scenario `json:"scenarios"`
}

type scenario struct {
	Name    string           `json:"name"`
	Types   []transcriptType `json:"types"`
	Steps   []transcriptStep `json:"steps"`
	Skipped []struct {
		Step   int    `json:"step"`
		Call   string `json:"call"`
		Reason string `json:"reason"`
	} `json:"skipped"`
}

type transcriptType struct {
	Name   string `json:"name"`
	Fields []struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Required bool   `json:"required"`
	} `json:"fields"`
	Versions []rules.TypeSpec `json:"versions"`
}

type transcriptStep struct {
	Kind     string                                `json:"kind"`
	Step     int                                   `json:"step"`
	Call     string                                `json:"call"`
	Database string                                `json:"database"`
	Type     string                                `json:"type"`
	Version  int                                   `json:"version"`
	Key      string                                `json:"key"`
	Spec     *rules.TypeSpec                       `json:"spec"`
	Command  *rules.Command                        `json:"command"`
	Owned    map[string]map[string]json.RawMessage `json:"owned"`
	Expect   struct {
		Refused json.RawMessage `json:"refused"`
		Result  json.RawMessage `json:"result"`
		Key     *string         `json:"key"`
		Row     json.RawMessage `json:"row"`
	} `json:"expect"`
}

func readTranscript(t *testing.T) *transcript {
	t.Helper()
	text, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	var out transcript
	if err := json.Unmarshal(text, &out); err != nil {
		t.Fatalf("%s: %v", transcriptPath, err)
	}
	return &out
}

func TestTranscriptOnSQLite(t *testing.T) {
	replayTranscript(t, rulestest.SQLite(t))
}

func TestTranscriptOnPostgres(t *testing.T) {
	replayTranscript(t, rulestest.Postgres(t))
}

// storageKey is a behavior's storage key: its name lowercased, each run of
// other characters one _ (the README's layout rule).
func storageKey(behavior string) string {
	return otherRun.ReplaceAllString(strings.ToLower(behavior), "_")
}

var otherRun = regexp.MustCompile(`[^a-z0-9]+`)

var snakeBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// snake is a field's name in snake case as the generators write it
// (codegen.ToSnakeCase, the core README's layout rule): a _ before each
// capital that follows a lowercase letter or a digit, each - a _, the whole
// lowercased. enteredAt is entered_at, lastIPAddress last_ipaddress.
func snake(name string) string {
	return strings.ToLower(snakeBoundary.ReplaceAllString(strings.ReplaceAll(name, "-", "_"), "${1}_${2}"))
}

var camelBoundary = regexp.MustCompile(`_([a-z0-9])`)

// camel is a column's name in camel case, the IR field name a type
// anchor's owned column takes here.
func camel(column string) string {
	return camelBoundary.ReplaceAllStringFunc(column, func(m string) string { return strings.ToUpper(m[1:]) })
}

// irType is the IR type of an own field of a JSON type.
func irType(jsonType string) string {
	switch jsonType {
	case "string", "number", "boolean":
		return jsonType
	case "integer":
		return "number"
	}
	return "Generic.JSON"
}

// sqlType is the SQL type of a column in the database's dialect.
func sqlType(db *rulestest.DB, column rules.Column) string {
	types := db.ColumnTypes()
	if column.Convert == rules.ConvertEpochMillis {
		return types.Timestamp
	}
	switch column.Type {
	case "string":
		return types.Text
	case "number":
		return types.Number
	case "boolean":
		return types.Boolean
	}
	return types.JSON
}

// usesOf is the uses of a type across its versions, each with the owned
// columns the layout rule gives it and its declared operations.
func usesOf(t *testing.T, declarations []rules.Declaration, typ transcriptType) []rules.Use {
	t.Helper()
	var uses []rules.Use
	seen := map[string]bool{}
	for _, version := range typ.Versions {
		for _, use := range version.Uses {
			anchor := use.Anchor.Field
			id := anchor
			if id == "" {
				id = use.Behavior
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			var declaration *rules.Declaration
			for i := range declarations {
				if declarations[i].Name == use.Behavior {
					declaration = &declarations[i]
				}
			}
			if declaration == nil {
				t.Fatalf("the core declares no behavior %s", use.Behavior)
			}
			out := rules.Use{ID: id, Behavior: use.Behavior, Anchor: anchor, Fields: []rules.OwnedField{}, Operations: []rules.Operation{}}
			for _, field := range declaration.Fields {
				if field.Type == "" {
					continue
				}
				value := anchor != "" && field.Name == declaration.Value
				var column string
				switch {
				case anchor == "":
					column = storageKey(use.Behavior) + "_" + snake(field.Name)
				case value:
					column = snake(anchor)
				default:
					column = snake(anchor) + "_" + snake(field.Name)
				}
				owned := rules.OwnedField{Name: field.Name, Column: rules.Column{Field: camel(column), Column: column, Type: field.Type}, Value: value}
				if field.Type == "Temporal.DateTime" {
					owned.Convert = rules.ConvertEpochMillis
				}
				out.Fields = append(out.Fields, owned)
			}
			for _, operation := range declaration.Operations {
				out.Operations = append(out.Operations, rules.Operation{Name: operation.Name, Writes: operation.Writes})
			}
			uses = append(uses, out)
		}
	}
	return uses
}

// mismatch is a step that answered otherwise than the transcript.
type mismatch struct {
	step     int
	call     string
	what     string
	expected string
	actual   string
}

func replayTranscript(t *testing.T, db *rulestest.DB) {
	tr := readTranscript(t)
	declarations, err := rules.Native().Declarations()
	if err != nil {
		t.Fatal(err)
	}
	core := rules.Native()
	ctx := context.Background()
	counts := map[string]int{}
	skipped := 0
	var mismatches []mismatch

	for index, sc := range tr.Scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			skipped += len(sc.Skipped)
			for _, left := range sc.Skipped {
				if strings.TrimSpace(left.Reason) == "" {
					t.Errorf("step %d (%s) is left out with no reason", left.Step, left.Call)
				}
			}
			var databases []string
			for _, step := range sc.Steps {
				known := false
				for _, d := range databases {
					known = known || d == step.Database
				}
				if !known {
					databases = append(databases, step.Database)
				}
			}
			databaseIndex := func(database string) int {
				for i, d := range databases {
					if d == database {
						return i
					}
				}
				return -1
			}
			typeOf := func(name string) *transcriptType {
				for i := range sc.Types {
					if sc.Types[i].Name == name {
						return &sc.Types[i]
					}
				}
				return nil
			}
			// tableOf is the layout of a type's table in one of the
			// scenario's databases, with a version's spec.
			tableOf := func(database, typeName string, spec *rules.TypeSpec) *rules.Table {
				table := &rules.Table{
					Type:    typeName,
					Table:   fmt.Sprintf("s%d_d%d_%s", index, databaseIndex(database), snake(typeName)),
					Key:     rules.Column{Field: "id", Column: "id", Type: "string"},
					Version: "_version",
					Own:     []rules.Column{},
					Uses:    []rules.Use{},
					Spec:    rules.TypeSpec{Name: typeName},
				}
				if typ := typeOf(typeName); typ != nil {
					for _, field := range typ.Fields {
						table.Own = append(table.Own, rules.Column{Field: field.Name, Column: field.Name, Type: irType(field.Type)})
					}
					table.Uses = usesOf(t, declarations, *typ)
					if len(typ.Versions) > 0 {
						table.Spec = typ.Versions[0]
					}
				}
				if spec != nil {
					table.Spec = *spec
				}
				return table
			}
			created := map[string]bool{}
			// ensure creates a table the first time a step needs it.
			ensure := func(table *rules.Table) {
				if created[table.Table] {
					return
				}
				created[table.Table] = true
				columns := []string{"id " + db.ColumnTypes().Key}
				for _, column := range table.Own {
					columns = append(columns, fmt.Sprintf(`"%s" %s`, column.Column, sqlType(db, column)))
				}
				for _, use := range table.Uses {
					for _, field := range use.Fields {
						columns = append(columns, fmt.Sprintf(`"%s" %s`, field.Column.Column, sqlType(db, field.Column)))
					}
				}
				columns = append(columns, "_version "+db.ColumnTypes().Version)
				db.Exec(t, fmt.Sprintf(`CREATE TABLE "%s" (%s)`, table.Table, strings.Join(columns, ", ")))
				for _, ddl := range db.BumpVersion(table.Table) {
					db.Exec(t, ddl)
				}
			}
			var found []mismatch
			compare := func(step transcriptStep, what string, expected, actual json.RawMessage) {
				if len(expected) == 0 && len(actual) == 0 {
					return
				}
				if len(expected) == 0 || len(actual) == 0 || !sameJSON(expected, actual) {
					found = append(found, mismatch{step: step.Step, call: step.Call, what: what, expected: absentOr(expected), actual: absentOr(actual)})
				}
			}

			for _, step := range sc.Steps {
				counts[step.Kind]++
				switch step.Kind {
				case "check":
					// On the core alone: a publish asks whether the type has
					// rows, which its table in the step's database answers.
					ensure(tableOf(step.Database, step.Spec.Name, nil))
					var reads []rules.Answered
					var refused *rules.Refusal
					settled := false
					for round := 0; round < 16 && !settled; round++ {
						run, err := core.Run(*step.Spec, *step.Command, rules.Answers{Reads: reads})
						if err != nil {
							t.Fatalf("step %d: %v", step.Step, err)
						}
						switch {
						case run.Done != nil:
							refused = run.Done.Refused
							settled = true
						case run.Read != nil:
							for _, request := range run.Read {
								if request.Kind != rules.ReadKindHasRows {
									t.Fatalf("step %d: a check asked for %+v", step.Step, request)
								}
								table := tableOf(step.Database, request.Type, nil)
								ensure(table)
								rows := db.Rows(t, fmt.Sprintf(`SELECT 1 AS one FROM "%s" LIMIT 1`, table.Table))
								reads = append(reads, rules.Answered{Request: request, Answer: rules.Answer{Kind: rules.ReadKindHasRows, HasRows: len(rows) > 0}})
							}
						default:
							t.Fatalf("step %d: a check asked for %s", step.Step, jsonOf(t, run))
						}
					}
					if !settled {
						t.Fatalf("step %d: the check never settled", step.Step)
					}
					compare(step, "refused", step.Expect.Refused, refusalJSON(t, refused))
				case "raw":
					// Past the adapter, as an operator's repair or the test's
					// own SQL: the escape hatch D103 keeps open.
					table := tableOf(step.Database, step.Type, nil)
					ensure(table)
					var sets []string
					var values []any
					for useID, fields := range step.Owned {
						var use *rules.Use
						for i := range table.Uses {
							if table.Uses[i].ID == useID {
								use = &table.Uses[i]
							}
						}
						if use == nil {
							t.Fatalf("step %d: %s has no use %s", step.Step, step.Type, useID)
						}
						for name, raw := range fields {
							var field *rules.OwnedField
							for i := range use.Fields {
								if use.Fields[i].Name == name {
									field = &use.Fields[i]
								}
							}
							if field == nil {
								t.Fatalf("step %d: %s's use %s has no field %s", step.Step, step.Type, useID, name)
							}
							values = append(values, rawValue(t, db, raw, field.Column))
							sets = append(sets, fmt.Sprintf(`"%s" = %s`, field.Column.Column, db.P(len(values))))
						}
					}
					values = append(values, step.Key)
					changed := db.Exec(t, fmt.Sprintf(`UPDATE "%s" SET %s WHERE id = %s`, table.Table, strings.Join(sets, ", "), db.P(len(values))), values...)
					compare(step, "raw.changes", json.RawMessage("1"), json.RawMessage(strconv.FormatInt(changed, 10)))
				case "command":
					typ := typeOf(step.Type)
					if typ == nil || step.Version >= len(typ.Versions) {
						t.Fatalf("step %d: no version %d of %s", step.Step, step.Version, step.Type)
					}
					table := tableOf(step.Database, step.Type, &typ.Versions[step.Version])
					ensure(table)
					layout := &rules.Layout{Types: map[string]*rules.Table{step.Type: table}}
					var result *rules.Result
					err := db.InTx(ctx, func(tx *rulestest.Tx) error {
						var err error
						result, err = rules.Execute(ctx, rules.Host{Core: core, Layout: layout, Store: tx.Store()}, step.Type, *step.Command, nil)
						return err
					})
					var refused *rules.Refusal
					if err != nil {
						var ok bool
						if refused, ok = rules.AsRefusal(err); !ok {
							t.Fatalf("step %d: %v", step.Step, err)
						}
					}
					compare(step, "refused", step.Expect.Refused, refusalJSON(t, refused))
					expectedResult := step.Expect.Result
					if len(expectedResult) == 0 {
						expectedResult = json.RawMessage("null")
					}
					actualResult := json.RawMessage("null")
					if refused == nil {
						actualResult = result.Result
					}
					compare(step, "result", expectedResult, actualResult)
					if step.Expect.Key != nil {
						actualKey := json.RawMessage("null")
						if result != nil {
							actualKey = jsonOf(t, result.Key)
						}
						compare(step, "key", jsonOf(t, *step.Expect.Key), actualKey)
					}
					action := step.Command.Action
					rowKey := action.Key
					if action.Kind == rules.ActionCreate {
						rowKey = action.ID
					}
					if rowKey != "" {
						var row *rules.Row
						err := db.InTx(ctx, func(tx *rulestest.Tx) error {
							var err error
							row, err = rules.ReadRow(ctx, rules.Host{Core: core, Layout: layout, Store: tx.Store()}, step.Type, rowKey)
							return err
						})
						if err != nil {
							t.Fatalf("step %d: read %s %s back: %v", step.Step, step.Type, rowKey, err)
						}
						var actualRow json.RawMessage
						if row != nil {
							actualRow = jsonOf(t, map[string]any{"own": row.Own, "owned": row.Owned})
						}
						compare(step, "row", step.Expect.Row, actualRow)
					}
				default:
					t.Fatalf("step %d: a step of kind %q", step.Step, step.Kind)
				}
			}
			for _, m := range found {
				t.Errorf("step %d (%s): %s\n  expected %s\n  actual   %s", m.step, m.call, m.what, m.expected, m.actual)
			}
			mismatches = append(mismatches, found...)
		})
	}

	replayed := counts["command"] + counts["check"] + counts["raw"]
	t.Logf("%s on %s: %d scenarios, %d steps replayed (%d commands, %d checks, %d raw writes), %d left out by the transcript with its reasons",
		tr.File, db.Name, len(tr.Scenarios), replayed, counts["command"], counts["check"], counts["raw"], skipped)
	if replayed < transcriptFloor {
		t.Errorf("replayed %d steps, below the floor of %d", replayed, transcriptFloor)
	}
	if len(mismatches) > 0 {
		t.Errorf("%d mismatches", len(mismatches))
	}
}

// rawValue is a raw step's value as the dialect binds its column.
func rawValue(t *testing.T, db *rulestest.DB, raw json.RawMessage, column rules.Column) any {
	t.Helper()
	if string(raw) == "null" {
		return nil
	}
	if column.Convert == rules.ConvertEpochMillis {
		millis, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			t.Fatalf("%s: %s is no epoch milliseconds", column.Field, raw)
		}
		return db.Timestamp(time.UnixMilli(millis))
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	switch value.(type) {
	case map[string]any, []any:
		return string(raw)
	}
	return value
}

// refusalJSON is a refusal's JSON, nil for none.
func refusalJSON(t *testing.T, refusal *rules.Refusal) json.RawMessage {
	t.Helper()
	if refusal == nil {
		return nil
	}
	return jsonOf(t, refusal)
}

func jsonOf(t *testing.T, value any) json.RawMessage {
	t.Helper()
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func absentOr(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "absent"
	}
	return string(raw)
}
