package writer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// ownStateMachines returns a copy of schema without Ticket's accountStatus,
// the field a dependency's enum types, its machine and the import: what
// every form writes and reads back.
func ownStateMachines(t *testing.T, schema *ir.Schema) *ir.Schema {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var clone ir.Schema
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	ticket := clone.Types["Ticket"]
	ticket.Fields = slices.DeleteFunc(ticket.Fields, func(f *ir.FieldDef) bool { return f.Name == "accountStatus" })
	ticket.StateMachines = slices.DeleteFunc(ticket.StateMachines, func(m *ir.StateMachine) bool { return m.Field == "accountStatus" })
	clone.Imports = nil
	return &clone
}

// TestWritersRoundTripStateMachines: every writer writes a type's
// @stateMachine declarations and the reader reads them back (D118). The
// TypeScript writer writes each as the decorator, its states the enum's
// members and a return to null as null, on a table and on a @jsonField
// type; the data forms write the same list under stateMachines.
func TestWritersRoundTripStateMachines(t *testing.T) {
	loaded, err := loader.LoadService(tsFixtures + "/fixture-state-machine-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	schema := ownStateMachines(t, loaded)
	want := normalizeIR(t, schema)
	for _, target := range []Format{FormatTS, FormatJSON, FormatYAML} {
		if got := normalizeIR(t, writeAndReload(t, schema, target)); got != want {
			t.Errorf("IR mismatch after TS -> %s round trip\nwant:\n%s\ngot:\n%s", target, want, got)
		}
	}

	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "src", "ticket.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`stateMachine } from "@superschematic/schema";`,
		`@stateMachine<Ticket>({ field: "state", transitions: { [TicketState.Open]: [TicketState.Open, TicketState.InProgress, TicketState.Resolved, TicketState.Closed], [TicketState.InProgress]: [TicketState.Resolved, TicketState.Open], [TicketState.Resolved]: [TicketState.Open, TicketState.Closed] }, terminal: [TicketState.Closed] })`,
		`@stateMachine<Ticket>({ field: "review", transitions: { [ReviewOutcome.RETRY]: [null, ReviewOutcome.APPROVED, ReviewOutcome.REJECTED] }, terminal: [ReviewOutcome.APPROVED, ReviewOutcome.REJECTED], fromNull: [ReviewOutcome.RETRY, ReviewOutcome.APPROVED, ReviewOutcome.REJECTED] })`,
		`@stateMachine<Ticket>({ field: "escalation", transitions: { [Escalation.Raised]: [Escalation.Acknowledged], [Escalation.Acknowledged]: [null] }, terminal: [], fromNull: [Escalation.Raised] })`,
		`@stateMachine<SignOff>({ field: "step", transitions: { [SignOffStep.Requested]: [SignOffStep.Granted, SignOffStep.Refused] }, terminal: [SignOffStep.Granted, SignOffStep.Refused] })`,
	} {
		if !strings.Contains(string(data), line) {
			t.Errorf("written TypeScript lacks %q:\n%s", line, data)
		}
	}

	jsonDir := t.TempDir()
	if _, err := WriteService(schema, FormatJSON, jsonDir); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(jsonDir, "src", "ticket.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Types map[string]struct {
			StateMachines []*ir.StateMachine `json:"stateMachines"`
		} `json:"types"`
	}
	if err := json.Unmarshal(written, &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.Types["Ticket"].StateMachines; len(got) != 3 || got[1].Field != "review" || !got[1].Transitions[0].ToNull || strings.Join(got[1].FromNull, ",") != "RETRY,APPROVED,REJECTED" {
		t.Errorf("written JSON's Ticket.stateMachines = %s", written)
	}
}

// TestTSWriterRefusesAStateMachineOnADependencysEnum: the IR holds a state
// as its member's value, and a dependency's enum is not the schema's to
// hold, so the TypeScript writer cannot name the members and says so
// rather than leave the machine out (D118).
func TestTSWriterRefusesAStateMachineOnADependencysEnum(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-state-machine-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	_, err = WriteService(schema, FormatTS, t.TempDir())
	const want = "type Ticket: @stateMachine on accountStatus: the field is typed by AccountStatus, which the schema does not declare, so the members its states name are not known; a machine on a dependency's enum has no TypeScript writer"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("WriteService = %v, want an error containing %q", err, want)
	}
}
