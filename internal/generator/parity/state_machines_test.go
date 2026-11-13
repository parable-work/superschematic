package parity

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The parity of a @stateMachine (D117). Six things answer whether a field
// may move from one state to another: the helpers generated for Go,
// TypeScript, Python and Rust, and the guards of the field's column on
// Postgres and on SQLite. TestStateMachineParity asks each of them every
// pair of a field's states, null among them, and holds every answer to one
// expectation, read from the IR alone (stateMachineAllows). Each
// generator's own test holds its output to the IR; this one fails when two
// of the six disagree, whichever is right.

// The state machine services: the DB's Ticket is a table with a machine on
// a required field, two on nullable fields, and one on a field typed by an
// enum of the enums service; its SignOff is a @jsonField class with a
// machine, which has the helpers and no column to guard.
const (
	stateMachineFixtures     = "../../loader/tsreader/testdata/services"
	stateMachineService      = "fixture-state-machine-db"
	stateMachineEnumsService = "fixture-state-machine-enums"
)

// stateMachineParties are the six that answer, in the order they are asked.
var stateMachineParties = []string{"go", "typescript", "python", "rust", "postgres", "sqlite"}

// stateMachineAllows is D117's rule, read from the IR alone: an unchanged
// value is no move and is allowed, null to null among them; from null a
// field takes a fromNull state; no move leaves a terminal state; and any
// other move is allowed only when the machine lists it, to null where the
// state's transition has toNull. nil is null.
func stateMachineAllows(machine *ir.StateMachine, from, to *string) bool {
	switch {
	case from == nil && to == nil:
		return true
	case from == nil:
		return slices.Contains(machine.FromNull, *to)
	case to != nil && *from == *to:
		return true
	case slices.Contains(machine.Terminal, *from):
		return false
	}
	for _, transition := range machine.Transitions {
		if transition.From != *from {
			continue
		}
		if to == nil {
			return transition.ToNull
		}
		return slices.Contains(transition.To, *to)
	}
	return false
}

// stateMachineCase is one machine as the six are asked about it: the IR's
// machine, the type, field and enum it names, and the states of its field,
// each a member's serialized value, with nil for null, first, when the
// field is nullable. A helper of a required field takes no null, and its
// column holds none, so null is no state of it.
type stateMachineCase struct {
	info    codegen.StateMachineInfo
	machine *ir.StateMachine
	states  []*string
	// table is the type's table when it has one: a machine on a table has
	// a guard, and one on any other class has the helpers alone.
	table string
}

// name is the machine as a failure names it: Ticket.review.
func (c stateMachineCase) name() string {
	return c.info.TypeName + "." + c.info.FieldName
}

// stateMachineCases returns every machine of schema, by type name and then
// in the order declared, with its field's enum read from schema or, when a
// dependency owns it, from deps.
func stateMachineCases(t *testing.T, schema *ir.Schema, deps map[string]*ir.Schema) []stateMachineCase {
	t.Helper()
	machines, err := codegen.ExtractStateMachines(schema, deps)
	if err != nil {
		t.Fatalf("the machines of %s: %v", schema.Name, err)
	}
	tables := map[string]string{}
	for _, td := range sqlutil.TableTypes(schema) {
		tables[td.Name] = sqlutil.TableName(td)
	}
	var cases []stateMachineCase
	for _, info := range machines {
		c := stateMachineCase{info: info, machine: schema.Types[info.TypeName].StateMachineOf(info.FieldName), table: tables[info.TypeName]}
		enum := schema.Enums[info.EnumName]
		if info.EnumOwner != "" {
			enum = deps[info.EnumOwner].Enums[info.EnumName]
		}
		if c.machine == nil || enum == nil {
			t.Fatalf("%s: the IR has no machine, or no enum %s", c.name(), info.EnumName)
		}
		if info.Nullable {
			c.states = append(c.states, nil)
		}
		for _, value := range enum.Values {
			state := value.Serialized()
			c.states = append(c.states, &state)
		}
		// A machine whose moves are all allowed, or all refused, would
		// tell an answer that is always one of the two from no other.
		allowed, refused := 0, 0
		for _, from := range c.states {
			for _, to := range c.states {
				if stateMachineAllows(c.machine, from, to) {
					allowed++
				} else {
					refused++
				}
			}
		}
		if allowed == 0 || refused == 0 {
			t.Fatalf("%s: %d moves allowed and %d refused; the machine proves nothing", c.name(), allowed, refused)
		}
		cases = append(cases, c)
	}
	return cases
}

// stateName is a state as a failure names it.
func stateName(state *string) string {
	if state == nil {
		return "null"
	}
	return *state
}

// moveAnswer is one party's answer to one move, as every driver writes
// it: the machine, the two states, null for null, and whether the move is
// allowed. Error is a driver's account of a move it got no answer to.
type moveAnswer struct {
	Machine string  `json:"machine"`
	From    *string `json:"from"`
	To      *string `json:"to"`
	Allowed *bool   `json:"allowed"`
	Error   string  `json:"error"`
}

// readMoveAnswers reads the answers a driver wrote to path.
func readMoveAnswers(t *testing.T, path string) []moveAnswer {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the driver's answers: %v", err)
	}
	var answers []moveAnswer
	if err := json.Unmarshal(data, &answers); err != nil {
		t.Fatalf("decode the driver's answers: %v\n%s", err, data)
	}
	return answers
}

// holdMoveAnswers holds party's answers to the expectation: one answer to
// every pair of the states of every machine of cases, each the one
// stateMachineAllows gives, and no other answer. A failure names the
// machine, the two states, what the machine says of the move and what the
// party says. It returns the number of pairs it asked about.
func holdMoveAnswers(t *testing.T, party string, cases []stateMachineCase, answers []moveAnswer) int {
	t.Helper()
	key := func(machine string, from, to *string) string {
		// A state holds no line break, and null is no state's name.
		return machine + "\n" + stateName(from) + "\n" + stateName(to) + "\n" + fmt.Sprint(from == nil, to == nil)
	}
	verb := func(allowed bool) string {
		if allowed {
			return "allows"
		}
		return "refuses"
	}
	given := map[string]moveAnswer{}
	for _, answer := range answers {
		k := key(answer.Machine, answer.From, answer.To)
		if _, twice := given[k]; twice {
			t.Errorf("%s from %s to %s: %s answered twice", answer.Machine, stateName(answer.From), stateName(answer.To), party)
		}
		given[k] = answer
	}
	pairs := 0
	for _, c := range cases {
		for _, from := range c.states {
			for _, to := range c.states {
				pairs++
				move := fmt.Sprintf("%s from %s to %s", c.name(), stateName(from), stateName(to))
				want := stateMachineAllows(c.machine, from, to)
				k := key(c.name(), from, to)
				answer, ok := given[k]
				delete(given, k)
				switch {
				case !ok:
					t.Errorf("%s: the machine %s it, and %s gave no answer", move, verb(want), party)
				case answer.Error != "":
					t.Errorf("%s: the machine %s it, and %s gave no answer: %s", move, verb(want), party, answer.Error)
				case answer.Allowed == nil:
					t.Errorf("%s: the machine %s it, and %s's answer is no boolean", move, verb(want), party)
				case *answer.Allowed != want:
					t.Errorf("%s: the machine %s it, and %s %s it", move, verb(want), party, verb(*answer.Allowed))
				}
			}
		}
	}
	for _, answer := range given {
		t.Errorf("%s from %s to %s: %s answered for a move the IR has no states of", answer.Machine, stateName(answer.From), stateName(answer.To), party)
	}
	return pairs
}

// askedMachine is one machine as the TypeScript and Python drivers read
// it: its name, its states, and the helper to ask, which Python finds on
// the model Type of Module and asks with the members of Enum.
type askedMachine struct {
	Machine string    `json:"machine"`
	States  []*string `json:"states"`
	Helper  string    `json:"helper"`
	Module  string    `json:"module,omitempty"`
	Type    string    `json:"type,omitempty"`
	Enum    string    `json:"enum,omitempty"`
}

// writeAskedMachines writes the machines a driver asks about to a file,
// and returns its path.
func writeAskedMachines(t *testing.T, name string, machines any) string {
	t.Helper()
	data, err := json.MarshalIndent(machines, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// goStateMachineDriver writes a Go test that asks every helper of cases
// every pair of its states and writes the answers to PARITY_ANSWERS. Go
// names a function at compile time, so each move is a line of its own. A
// state is the enum's type over the serialized value, which is how a
// service reads one from a column, behind a pointer on a nullable field.
func goStateMachineDriver(pkg string, cases []stateMachineCase) string {
	var moves strings.Builder
	for _, c := range cases {
		helper := c.info.TypeName + "CanTransition" + codegen.ToPascalCase(c.info.FieldName)
		state := func(state *string) string {
			switch {
			case state == nil:
				return "nil"
			case c.info.Nullable:
				return fmt.Sprintf("parityPtr(%s(%q))", c.info.EnumName, *state)
			}
			return fmt.Sprintf("%s(%q)", c.info.EnumName, *state)
		}
		text := func(state *string) string {
			if state == nil {
				return "nil"
			}
			return fmt.Sprintf("%q", *state)
		}
		for _, from := range c.states {
			for _, to := range c.states {
				fmt.Fprintf(&moves, "\t\t{\"machine\": %q, \"from\": %s, \"to\": %s, \"allowed\": %s(%s, %s)},\n",
					c.name(), text(from), text(to), helper, state(from), state(to))
			}
		}
	}
	return `package ` + pkg + `

import (
	"encoding/json"
	"os"
	"testing"
)

func parityPtr[T any](value T) *T { return &value }

func TestStateMachineParityDriver(t *testing.T) {
	answersPath := os.Getenv("PARITY_ANSWERS")
	if answersPath == "" {
		t.Skip("PARITY_ANSWERS not set")
	}
	answers := []map[string]any{
` + moves.String() + `	}
	out, err := json.MarshalIndent(answers, "", "  ")
	if err != nil {
		t.Fatalf("marshal answers: %v", err)
	}
	if err := os.WriteFile(answersPath, out, 0o644); err != nil {
		t.Fatalf("write answers: %v", err)
	}
}
`
}

// tsStateMachineDriver asks each helper PARITY_MACHINES names, from
// state-machines.ts, every pair of the machine's states. A state is the
// enum's value, which a string enum's member is.
const tsStateMachineDriver = `import { readFileSync, writeFileSync } from 'node:fs';
import * as helpers from './state-machines.ts';

type Machine = { machine: string; helper: string; states: (string | null)[] };
type Helper = (from: string | null, to: string | null) => unknown;

const machines: Machine[] = JSON.parse(readFileSync(process.env.PARITY_MACHINES as string, 'utf8'));
const answers: { machine: string; from: string | null; to: string | null; allowed: unknown }[] = [];
for (const machine of machines) {
  const canTransition = (helpers as Record<string, unknown>)[machine.helper] as Helper;
  for (const from of machine.states) {
    for (const to of machine.states) {
      answers.push({ machine: machine.machine, from, to, allowed: canTransition(from, to) });
    }
  }
}
writeFileSync(process.env.PARITY_ANSWERS as string, JSON.stringify(answers, null, 2));
`

// pyStateMachineDriver asks each helper PARITY_MACHINES names every pair
// of the machine's states, with the enum's members and with their values,
// which a model built with use_enum_values holds. The two are one answer:
// where they differ the move has none.
const pyStateMachineDriver = `
import importlib
import json
import os

with open(os.environ["PARITY_MACHINES"]) as f:
    machines = json.load(f)

answers = []
for machine in machines:
    module = importlib.import_module(machine["module"])
    can_transition = getattr(getattr(module, machine["type"]), machine["helper"])
    enum = getattr(module, machine["enum"])

    def member(state):
        return None if state is None else enum(state)

    for current in machine["states"]:
        for target in machine["states"]:
            answer = {"machine": machine["machine"], "from": current, "to": target}
            by_member = can_transition(member(current), member(target))
            by_value = can_transition(current, target)
            if by_member is by_value:
                answer["allowed"] = by_member
            else:
                answer["error"] = f"the helper answers {by_member!r} for the members and {by_value!r} for their values"
            answers.append(answer)

with open(os.environ["PARITY_ANSWERS"], "w") as f:
    json.dump(answers, f, indent=2)
`

// rustStateMachineDriver writes a Rust test that asks every helper of
// cases every pair of its field's states and writes the answers to
// PARITY_ANSWERS. The states are the enum's own: its ALL, with None first
// on a nullable field, each named by its as_str, so a variant whose value
// is not the IR's answers for a state the IR lacks. crate is the generated
// crate's name.
func rustStateMachineDriver(crate string, cases []stateMachineCase) string {
	var uses []string
	var asks strings.Builder
	for _, c := range cases {
		enum := c.info.EnumName
		helper := codegen.ToSnakeCase(c.info.TypeName) + "_can_transition_" + codegen.ToSnakeCase(c.info.FieldName)
		for _, name := range []string{enum, helper} {
			if !slices.Contains(uses, name) {
				uses = append(uses, name)
			}
		}
		states, name := fmt.Sprintf("%s::ALL.to_vec()", enum), fmt.Sprintf("|state: &%s| state.as_str()", enum)
		if c.info.Nullable {
			states = fmt.Sprintf("std::iter::once(None).chain(%s::ALL.iter().copied().map(Some)).collect::<Vec<_>>()", enum)
			name = fmt.Sprintf("|state: &Option<%s>| state.map(|state| state.as_str())", enum)
		}
		fmt.Fprintf(&asks, "    let states = %s;\n    let name = %s;\n", states, name)
		asks.WriteString("    for from in &states {\n        for to in &states {\n            answers.push(json!({\n")
		fmt.Fprintf(&asks, "                \"machine\": %q,\n", c.name())
		asks.WriteString("                \"from\": name(from),\n                \"to\": name(to),\n")
		fmt.Fprintf(&asks, "                \"allowed\": %s(*from, *to),\n", helper)
		asks.WriteString("            }));\n        }\n    }\n")
	}
	slices.Sort(uses)
	return "use serde_json::{json, Value};\nuse " + crate + "::{" + strings.Join(uses, ", ") + `};

#[test]
fn state_machine_parity_driver() {
    let Ok(answers_path) = std::env::var("PARITY_ANSWERS") else {
        return;
    };
    let mut answers: Vec<Value> = Vec::new();
` + asks.String() + `    std::fs::write(answers_path, serde_json::to_string_pretty(&answers).unwrap()).unwrap();
}
`
}

// guardedTable is one table whose guards testdata/guardmoves asks: its key
// column, which each row is given, the other columns an insert sets with
// their literals, and the guarded columns.
type guardedTable struct {
	Table   string            `json:"table"`
	Key     string            `json:"key"`
	Insert  map[string]string `json:"insert"`
	Columns []guardedColumn   `json:"columns"`
}

// guardedColumn is one machine's column: the states it takes, and the
// text that marks its guard's refusal in the dialect asked.
type guardedColumn struct {
	Machine string    `json:"machine"`
	Column  string    `json:"column"`
	States  []*string `json:"states"`
	Refusal string    `json:"refusal"`
}

// stateMachineInserts are the columns an insert of a fixture's table must
// set beside its key and the guarded column, with their literals, by
// table: every other column of ticket is nullable or has a default.
var stateMachineInserts = map[string]map[string]string{
	"ticket": {"subject": "'A ticket'"},
}

// guardedTables returns the tables of the machines of cases that have
// one, with the column sqlgen gives each machine's field and the mark of
// its guard's refusal in dialect: on Postgres the table and column the
// message starts with, on SQLite the guard's fixed message. A machine on a
// table that sqlgen builds no guard for fails here.
func guardedTables(t *testing.T, ddl *sqlgen.DDLOutput, cases []stateMachineCase, dialect sqlmigrate.Dialect) []guardedTable {
	t.Helper()
	var tables []guardedTable
	for _, c := range cases {
		if c.table == "" {
			continue
		}
		at := slices.IndexFunc(ddl.StateGuards, func(g sqlgen.StateGuard) bool {
			return g.Origin == c.info.TypeName && g.Field == c.info.FieldName
		})
		if at < 0 {
			t.Fatalf("%s is a machine of the table %s, and sqlgen builds it no guard", c.name(), c.table)
		}
		guard := ddl.StateGuards[at]
		column := guardedColumn{Machine: c.name(), Column: guard.Column, States: c.states, Refusal: guard.Table + "." + guard.Column + " "}
		if dialect == sqlmigrate.SQLite {
			column.Refusal = guard.SQLiteMessage()
		}
		i := slices.IndexFunc(tables, func(table guardedTable) bool { return table.Table == guard.Table })
		if i < 0 {
			built := slices.IndexFunc(ddl.Tables, func(table sqlgen.Table) bool { return table.Name == guard.Table })
			if built < 0 || ddl.Tables[built].PrimaryKey == "" {
				t.Fatalf("%s: sqlgen builds no table %s with a key", c.name(), guard.Table)
			}
			tables = append(tables, guardedTable{Table: guard.Table, Key: ddl.Tables[built].PrimaryKey, Insert: stateMachineInserts[guard.Table]})
			i = len(tables) - 1
		}
		tables[i].Columns = append(tables[i].Columns, column)
	}
	return tables
}

// buildGuardMoves builds testdata/guardmoves, a module of its own so the
// database drivers stay out of the compiler's module graph, and returns
// the binary's path.
func buildGuardMoves(t *testing.T) string {
	t.Helper()
	module := t.TempDir()
	if err := os.CopyFS(module, os.DirFS(filepath.Join("testdata", "guardmoves"))); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "guardmoves")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = module
	// Without cgo, as the SQLite driver is built everywhere else: neither
	// driver needs it.
	build.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build testdata/guardmoves: %v\n%s", err, out)
	}
	return bin
}

// askGuards runs guardmoves on the database where names, of dialect, made
// by the create script, for tables, and returns its answers.
func askGuards(t *testing.T, bin, dialect, where, create string, tables []guardedTable) []moveAnswer {
	t.Helper()
	dir := t.TempDir()
	createPath := filepath.Join(dir, "create.sql")
	if err := os.WriteFile(createPath, []byte(create), 0o644); err != nil {
		t.Fatal(err)
	}
	movesPath := writeAskedMachines(t, "moves.json", tables)
	answersPath := filepath.Join(dir, "answers.json")
	run := exec.Command(bin, dialect, where, createPath, movesPath, answersPath)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("guardmoves %s failed: %v\n%s", dialect, err, out)
	}
	return readMoveAnswers(t, answersPath)
}

// TestStateMachineParity asks the six that answer whether a field may move
// from one state to another, for every machine of the state machine
// fixture and every pair of its field's states, null among them where the
// field is nullable, and holds each answer to stateMachineAllows on the
// IR (D117). The helpers of Go, TypeScript, Python and Rust are asked from
// the types module, package and crate a build writes; the guards are asked
// by an update of the column, from each pair's first state to its second,
// on a Postgres and a SQLite database the service's create.sql made
// (testdata/guardmoves). SignOff is no table, so its machine is the
// helpers' alone.
//
// A party whose toolchain is missing skips, as the other tests of the
// package do, and the Postgres guards skip without
// SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL, which names the server the
// tests of sqlgen's DDL run on. CI's full tier has all six.
func TestStateMachineParity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping state machine parity check in -short mode")
	}
	enums, err := loader.LoadService(filepath.Join(stateMachineFixtures, stateMachineEnumsService))
	if err != nil {
		t.Fatalf("load %s: %v", stateMachineEnumsService, err)
	}
	schema, err := loader.LoadService(filepath.Join(stateMachineFixtures, stateMachineService))
	if err != nil {
		t.Fatalf("load %s: %v", stateMachineService, err)
	}
	deps := map[string]*ir.Schema{stateMachineEnumsService: enums}
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	cases := stateMachineCases(t, schema, deps)
	var names []string
	var guarded []stateMachineCase
	for _, c := range cases {
		names = append(names, c.name())
		if c.table != "" {
			guarded = append(guarded, c)
		}
	}
	if want := []string{"SignOff.step", "Ticket.state", "Ticket.review", "Ticket.escalation", "Ticket.accountStatus"}; !slices.Equal(names, want) {
		t.Fatalf("the machines of %s are %v, want %v", stateMachineService, names, want)
	}
	if len(guarded) != 4 || guarded[0].info.TypeName != "Ticket" {
		t.Fatalf("%d machines of %s are a table's, want Ticket's four", len(guarded), stateMachineService)
	}
	// SignOff.step and Ticket.state, of 3 and 4 members; Ticket.review and
	// Ticket.escalation, of 3 and 2 with null; Ticket.accountStatus, of 3.
	// The guards answer for all but SignOff.step.
	const helperPairs, guardPairs = 3*3 + 4*4 + 4*4 + 3*3 + 3*3, 4*4 + 4*4 + 3*3 + 3*3

	// The rule the answers are held to, held to the fixture's own words.
	state := func(s string) *string { return &s }
	for _, known := range []struct {
		machine  string
		from, to *string
		want     bool
	}{
		{"Ticket.state", state("open"), state("open"), true},
		{"Ticket.state", state("closed"), state("closed"), true},
		{"Ticket.state", state("open"), state("closed"), true},
		{"Ticket.state", state("in_progress"), state("closed"), false},
		{"Ticket.state", state("closed"), state("open"), false},
		{"Ticket.review", nil, nil, true},
		{"Ticket.review", nil, state("APPROVED"), true},
		{"Ticket.review", state("RETRY"), nil, true},
		{"Ticket.review", state("APPROVED"), nil, false},
		{"Ticket.review", state("APPROVED"), state("RETRY"), false},
		{"Ticket.escalation", nil, state("acknowledged"), false},
		{"Ticket.escalation", state("raised"), nil, false},
		{"Ticket.escalation", state("acknowledged"), nil, true},
		{"Ticket.escalation", state("acknowledged"), state("raised"), false},
		{"Ticket.accountStatus", state("suspended"), state("active"), true},
		{"Ticket.accountStatus", state("closed"), state("active"), false},
		{"SignOff.step", state("requested"), state("refused"), true},
		{"SignOff.step", state("granted"), state("refused"), false},
	} {
		c := cases[slices.Index(names, known.machine)]
		if got := stateMachineAllows(c.machine, known.from, known.to); got != known.want {
			t.Fatalf("the rule lets %s move from %s to %s: %v, want %v", known.machine, stateName(known.from), stateName(known.to), got, known.want)
		}
	}

	// hold holds a party's answers to the expectation and to the number of
	// pairs it is asked.
	answered := map[string]int{}
	hold := func(t *testing.T, party string, asked []stateMachineCase, want int, answers []moveAnswer) {
		t.Helper()
		pairs := holdMoveAnswers(t, party, asked, answers)
		if pairs != want || len(answers) != want {
			t.Errorf("%s answered %d moves of %d asked, want %d", party, len(answers), pairs, want)
		}
		if !t.Failed() {
			answered[party] = pairs
		}
	}
	run := func(t *testing.T, dir string, env []string, program string, args ...string) {
		t.Helper()
		cmd := exec.Command(program, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %s: %v\n%s", filepath.Base(program), strings.Join(args, " "), err, out)
		}
	}
	// The dependency first: each language's package of the DB service
	// names the enums service's beside it.
	services := []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{stateMachineEnumsService, enums, nil},
		{stateMachineService, schema, deps},
	}

	t.Run("go", func(t *testing.T) {
		paths := testpaths.Local(t)
		// A module names its dependency's by ../<service>.
		root := testpaths.TempDir(t)
		var pkg string
		for _, service := range services {
			modules := map[string]string{}
			for dep := range service.deps {
				modules[dep] = "example.com/schemas/types/go/" + dep
			}
			output, err := typegen.Generate(service.schema, typegen.Options{
				SchemaName:        service.name,
				ModulePath:        "example.com/schemas/types/go/" + service.name,
				Dependencies:      service.deps,
				DependencyModules: modules,
				Clock:             fixedClock,
			})
			if err != nil {
				t.Fatalf("generate go for %s: %v", service.name, err)
			}
			outDir := filepath.Join(root, service.name)
			if err := typegen.SetReplacePaths(output, paths, outDir); err != nil {
				t.Fatalf("set replace paths for %s: %v", service.name, err)
			}
			if err := typegen.WriteTypes(output, outDir); err != nil {
				t.Fatalf("write go types of %s: %v", service.name, err)
			}
			tidy := exec.Command("go", "mod", "tidy")
			tidy.Dir = outDir
			if out, err := tidy.CombinedOutput(); err != nil {
				t.Skipf("go mod tidy failed for %s (likely offline): %v\n%s", service.name, err, out)
			}
			pkg = output.PackageName
		}
		outDir := filepath.Join(root, stateMachineService)
		if err := os.WriteFile(filepath.Join(outDir, "state_machine_parity_test.go"), []byte(goStateMachineDriver(pkg, cases)), 0o644); err != nil {
			t.Fatalf("write go driver: %v", err)
		}
		answersPath := filepath.Join(t.TempDir(), "answers-go.json")
		run(t, outDir, []string{"PARITY_ANSWERS=" + answersPath}, "go", "test", "-count=1", "-run", "^TestStateMachineParityDriver$", ".")
		hold(t, "go", cases, helperPairs, readMoveAnswers(t, answersPath))
	})

	t.Run("typescript", func(t *testing.T) {
		bunPath, err := exec.LookPath("bun")
		if err != nil {
			testpaths.RequireOrSkipTS(t, "bun not available for the TypeScript state machine parity check")
		}
		paths := testpaths.Local(t)
		// The output root is the Bun workspace root, so the DB package's
		// workspace:* dependency on the enums package resolves.
		root := testpaths.TempDir(t)
		typesRoot := filepath.Join(root, "types", "typescript")
		for _, service := range services {
			output, err := tsgen.Generate(service.schema, tsgen.Options{SchemaName: service.name, Dependencies: service.deps, Clock: fixedClock})
			if err != nil {
				t.Fatalf("generate typescript for %s: %v", service.name, err)
			}
			outDir := filepath.Join(typesRoot, service.name)
			if err := tsgen.SetScalarLibSpec(output, paths, outDir); err != nil {
				t.Fatalf("set superscalar spec for %s: %v", service.name, err)
			}
			if err := tsgen.WriteTypes(output, outDir); err != nil {
				t.Fatalf("write typescript types of %s: %v", service.name, err)
			}
		}
		if err := (tsgen.WorkspaceRoot{OutputRoot: root, Paths: paths}).Write(); err != nil {
			t.Fatalf("write workspace root: %v", err)
		}
		install := exec.Command(bunPath, "install")
		install.Dir = root
		if out, err := install.CombinedOutput(); err != nil {
			testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed (likely offline): %v\n%s", err, out))
		}

		var machines []askedMachine
		for _, c := range cases {
			machines = append(machines, askedMachine{
				Machine: c.name(),
				States:  c.states,
				Helper:  codegen.ToCamelCase(c.info.TypeName) + "CanTransition" + codegen.ToPascalCase(c.info.FieldName),
			})
		}
		outDir := filepath.Join(typesRoot, stateMachineService)
		if err := os.WriteFile(filepath.Join(outDir, "state_machine_parity_driver.ts"), []byte(tsStateMachineDriver), 0o644); err != nil {
			t.Fatalf("write ts driver: %v", err)
		}
		answersPath := filepath.Join(t.TempDir(), "answers-ts.json")
		run(t, outDir, []string{"PARITY_MACHINES=" + writeAskedMachines(t, "machines-ts.json", machines), "PARITY_ANSWERS=" + answersPath},
			bunPath, "run", "state_machine_parity_driver.ts")
		hold(t, "typescript", cases, helperPairs, readMoveAnswers(t, answersPath))
	})

	t.Run("python", func(t *testing.T) {
		// The generated package imports pydantic, so the driver runs in
		// runtime/schema/python's environment; the system python3 may lack it.
		uvPath, err := exec.LookPath("uv")
		if err != nil {
			t.Skip("uv not available; skipping Python state machine parity check")
		}
		project := filepath.Join(testpaths.RepoRoot(t), "runtime", "schema", "python")
		probe := exec.Command(uvPath, "run", "--project", project, "python", "-c", "import pydantic")
		if out, err := probe.CombinedOutput(); err != nil {
			t.Skipf("runtime/schema/python's environment lacks pydantic; skipping Python state machine parity check: %v\n%s", err, out)
		}

		root := t.TempDir()
		var importPaths []string
		var module string
		for _, service := range services {
			output, err := pygen.Generate(service.schema, pygen.Options{SchemaName: service.name, Dependencies: service.deps, Clock: fixedClock})
			if err != nil {
				t.Fatalf("generate python for %s: %v", service.name, err)
			}
			outDir := filepath.Join(root, service.name)
			if err := pygen.WriteTypes(output, outDir); err != nil {
				t.Fatalf("write python types of %s: %v", service.name, err)
			}
			importPaths = append(importPaths, outDir)
			module = output.PythonModuleName
		}

		var machines []askedMachine
		for _, c := range cases {
			machines = append(machines, askedMachine{
				Machine: c.name(),
				States:  c.states,
				Helper:  "can_transition_" + codegen.ToSnakeCase(c.info.FieldName),
				Module:  module,
				Type:    c.info.TypeName,
				Enum:    c.info.EnumName,
			})
		}
		answersPath := filepath.Join(t.TempDir(), "answers-py.json")
		run(t, root, []string{
			"PYTHONPATH=" + strings.Join(importPaths, string(os.PathListSeparator)),
			"PARITY_MACHINES=" + writeAskedMachines(t, "machines-py.json", machines),
			"PARITY_ANSWERS=" + answersPath,
		}, uvPath, "run", "--project", project, "python", "-B", "-c", pyStateMachineDriver)
		hold(t, "python", cases, helperPairs, readMoveAnswers(t, answersPath))
	})

	t.Run("rust", func(t *testing.T) {
		cargoPath, err := exec.LookPath("cargo")
		if err != nil {
			t.Skip("cargo not available; skipping Rust state machine parity check")
		}
		paths := testpaths.Local(t)
		// A crate names its dependency's by ../<service>.
		root := testpaths.TempDir(t)
		var crate string
		for _, service := range services {
			// SQLite as the service's build sets it, for its sql.dialects.
			output, err := rustgen.Generate(service.schema, rustgen.Options{SchemaName: service.name, Dependencies: service.deps, SQLite: service.deps != nil, Clock: fixedClock})
			if err != nil {
				t.Fatalf("generate rust for %s: %v", service.name, err)
			}
			outDir := filepath.Join(root, service.name)
			if err := rustgen.SetLocalPaths(output, paths, outDir); err != nil {
				t.Fatalf("set local paths for %s: %v", service.name, err)
			}
			if err := rustgen.WriteTypes(output, outDir); err != nil {
				t.Fatalf("write rust types of %s: %v", service.name, err)
			}
			crate = strings.ReplaceAll(output.CrateName, "-", "_")
		}
		outDir := filepath.Join(root, stateMachineService)
		if err := os.MkdirAll(filepath.Join(outDir, "tests"), 0o755); err != nil {
			t.Fatalf("mkdir tests: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "tests", "state_machine_parity.rs"), []byte(rustStateMachineDriver(crate, cases)), 0o644); err != nil {
			t.Fatalf("write rust driver: %v", err)
		}
		targetDir := os.Getenv("CARGO_TARGET_DIR")
		if targetDir == "" {
			targetDir = filepath.Join(root, "target")
		}
		answersPath := filepath.Join(t.TempDir(), "answers-rust.json")
		run(t, outDir, []string{"CARGO_TARGET_DIR=" + targetDir, "PARITY_ANSWERS=" + answersPath},
			cargoPath, "test", "--quiet", "--test", "state_machine_parity")
		hold(t, "rust", cases, helperPairs, readMoveAnswers(t, answersPath))
	})

	// The guards, from the create.sql of each dialect a build of the
	// service writes.
	options := sqlgen.Options{SchemaName: stateMachineService, Dependencies: deps, Clock: fixedClock}
	ddl, err := sqlgen.Generate(schema, options)
	if err != nil {
		t.Fatalf("generate the DDL: %v", err)
	}
	guardMoves := buildGuardMoves(t)

	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
		if dsn == "" {
			t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to ask the state machine guards on Postgres")
		}
		dir := t.TempDir()
		if err := sqlgen.WriteDDL(ddl, dir); err != nil {
			t.Fatalf("write the DDL: %v", err)
		}
		create, err := os.ReadFile(filepath.Join(dir, "create.sql"))
		if err != nil {
			t.Fatal(err)
		}
		answers := askGuards(t, guardMoves, "postgres", dsn, string(create), guardedTables(t, ddl, cases, sqlmigrate.Postgres))
		hold(t, "postgres", guarded, guardPairs, answers)
	})

	t.Run("sqlite", func(t *testing.T) {
		model, err := sqlmigrate.BuildModel(schema, options, sqlmigrate.SQLite)
		if err != nil {
			t.Fatalf("build the SQLite model: %v", err)
		}
		create, err := sqlmigrate.CreateSQL(model)
		if err != nil {
			t.Fatalf("write the SQLite DDL: %v", err)
		}
		answers := askGuards(t, guardMoves, "sqlite", filepath.Join(t.TempDir(), "guards.db"), create, guardedTables(t, ddl, cases, sqlmigrate.SQLite))
		hold(t, "sqlite", guarded, guardPairs, answers)
	})

	for _, party := range stateMachineParties {
		if pairs, ok := answered[party]; ok {
			t.Logf("%s answered %d moves as the machines say", party, pairs)
		} else if !t.Failed() {
			t.Logf("%s did not answer: its subtest skipped", party)
		}
	}
}
