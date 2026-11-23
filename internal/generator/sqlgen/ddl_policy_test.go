package sqlgen

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// policySchema is one table whose columns take each default and check the
// naming file's [sql] table governs: an enum with a Default<T, V>, a
// number with Validate bounds and a Default<T, V>, a required timestamp no
// rule gives the time of the insert, one declared Default<T, "now">, and
// an audit createdAt.
func policySchema() *ir.Schema {
	s := ir.NewSchema("policy-db", ir.SchemaKindDB)
	s.Enums["Stage"] = &ir.EnumDef{Name: "Stage", Values: []ir.EnumValueDef{{Name: "Open", SerializedAs: "open"}, {Name: "Done", SerializedAs: "done"}}}
	s.Scalars["Temporal.DateTime"] = &ir.ScalarDef{Name: "Temporal.DateTime", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "TIMESTAMPTZ"}}
	s.Scalars["Generic.Int64"] = &ir.ScalarDef{Name: "Generic.Int64", LanguagePrimitive: ir.LanguageNumber, TypeMappings: map[string]string{"sql": "BIGINT"}}
	open, three, now := "open", "3", "now"
	low, high := 1.0, 9.0
	s.Types["Task"] = &ir.TypeDef{Name: "Task", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
		{Name: "stage", TypeRef: ir.TypeRef{Name: "Stage"}, Required: true, Default: &open},
		{Name: "tries", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Required: true, Default: &three, ValidateMin: &low, ValidateMax: &high},
		{Name: "dueAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true},
		{Name: "seenAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true, Default: &now},
		{Name: "createdAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true},
	}, StateMachines: []*ir.StateMachine{{
		Field:       "stage",
		Transitions: []*ir.StateTransition{{From: "open", To: []string{"done"}}},
		Terminal:    []string{"done"},
	}}}
	return s
}

// columnLine is the line of create.sql that declares column in the task
// table.
func columnLine(t *testing.T, create, column string) string {
	t.Helper()
	for _, line := range strings.Split(create, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), column+" ") {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("create.sql declares no column %s:\n%s", column, create)
	return ""
}

// TestTheNamingFilesSQLTableSetsTheDDL builds one table under the core's
// [sql] defaults and under the opposite of each: column_checks and
// column_defaults "off" leave out the enum's and the bounds' CHECK and the
// literal DEFAULTs, keep Default<T, "now">, and now_defaults "required"
// gives the time of the insert to the timestamp no rule names. The guard's
// two messages are the naming file's.
func TestTheNamingFilesSQLTableSetsTheDDL(t *testing.T) {
	core, err := Generate(policySchema(), SQLOptionsForTest(naming.Default()))
	if err != nil {
		t.Fatal(err)
	}
	create := createSQL(t, core)
	for column, want := range map[string]string{
		"stage":      "stage TEXT DEFAULT 'open' NOT NULL,",
		"tries":      "tries BIGINT DEFAULT 3 NOT NULL,",
		"due_at":     "due_at TIMESTAMPTZ NOT NULL,",
		"seen_at":    "seen_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL,",
		"created_at": "created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL",
	} {
		if got := columnLine(t, create, column); !strings.HasPrefix(got, strings.TrimSuffix(want, ",")) {
			t.Errorf("under the core's [sql] the %s column is %q, want %q", column, got, want)
		}
	}
	for _, want := range []string{"ck_task_stage CHECK", "ck_task_tries CHECK", "is terminal at %: it cannot move to %", "cannot move from % to %"} {
		if !strings.Contains(create, want) {
			t.Errorf("under the core's [sql] create.sql lacks %q", want)
		}
	}

	n := naming.Default()
	n.SQL = naming.SQLConfig{
		ColumnChecks:       naming.SQLOff,
		ColumnDefaults:     naming.SQLOff,
		NowDefaults:        naming.SQLNowRequired,
		StateGuardTerminal: "{column} is terminal at % and cannot change to %",
		StateGuardRefused:  "{column} cannot change from % to %",
	}
	out, err := Generate(policySchema(), SQLOptionsForTest(n))
	if err != nil {
		t.Fatal(err)
	}
	create = createSQL(t, out)
	for column, want := range map[string]string{
		"stage":      "stage TEXT NOT NULL",
		"tries":      "tries BIGINT NOT NULL",
		"due_at":     "due_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL",
		"seen_at":    "seen_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL",
		"created_at": "created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP NOT NULL",
	} {
		if got := strings.TrimSuffix(columnLine(t, create, column), ","); got != want {
			t.Errorf("under the opposite [sql] the %s column is %q, want %q", column, got, want)
		}
	}
	for _, absent := range []string{"CHECK", "cannot move"} {
		if strings.Contains(create, absent) {
			t.Errorf("under the opposite [sql] create.sql holds %q:\n%s", absent, create)
		}
	}
	for _, want := range []string{
		"RAISE EXCEPTION 'task.stage is terminal at % and cannot change to %', OLD.stage, NEW.stage",
		"RAISE EXCEPTION 'task.stage cannot change from % to %', OLD.stage, NEW.stage",
	} {
		if !strings.Contains(create, want) {
			t.Errorf("create.sql lacks %q", want)
		}
	}
}

// TestAGuardMessageQuotesItsText: a quote in the naming file's message is
// doubled in the RAISE literal, and a % in the guarded name is no
// parameter's place.
func TestAGuardMessageQuotesItsText(t *testing.T) {
	g := StateGuard{Table: "task", Column: "stage%"}
	if got, want := g.message("{column} can't leave % for %", ""), "task.stage%% can''t leave % for %"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
	if got, want := g.message("", naming.DefaultGuardRefused), "task.stage%% cannot move from % to %"; got != want {
		t.Errorf("the default message = %q, want %q", got, want)
	}
}

// createSQL renders out's create.sql.
func createSQL(t *testing.T, out *DDLOutput) string {
	t.Helper()
	b, err := renderTemplate("create.tmpl", out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// SQLOptionsForTest is the Options a build passes for n's [sql] table.
func SQLOptionsForTest(n naming.Naming) Options {
	return Options{
		SchemaName:         "policy-db",
		DDL:                sqlutil.DDLPolicyOf(n),
		StateGuardTerminal: n.SQL.StateGuardTerminal,
		StateGuardRefused:  n.SQL.StateGuardRefused,
	}
}
