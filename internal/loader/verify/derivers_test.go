package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestDeriversRefusedInIR: an API schema authored as IR is held to what
// @deriver takes (D74): schemas each named once, a
// concurrency of one or more, a batch size from 1 to 100, a name no other
// deriver, worker, job, type or operation set takes, and a member of the
// generated Derivers no other deriver takes; and only an API declares a
// deriver.
func TestDeriversRefusedInIR(t *testing.T) {
	schema := ir.NewSchema("tickets", ir.SchemaKindAPI)
	schema.Types["TicketView"] = &ir.TypeDef{Name: "TicketView", Role: ir.RoleAPIView}
	schema.OperationSets = []*ir.OperationSet{{Name: "TicketQueries"}}
	schema.Jobs = []*ir.Job{{Name: "CloseTickets"}}
	schema.Workers = []*ir.Worker{{Name: "AnswerPings", Queue: "Ticket"}}
	schema.Derivers = []*ir.Deriver{
		{Name: "TriageTickets", Schemas: []string{"Ticket"}},
		{Name: "TriageTickets"},
		{Name: "Twice", Schemas: []string{"Ticket", "Ticket"}},
		{Name: "Negative", Concurrency: -1},
		{Name: "TooLarge", BatchSize: 101},
		{Name: "TicketView"},
		{Name: "TicketQueries"},
		{Name: "CloseTickets"},
		{Name: "AnswerPings"},
		{Name: "triageTickets"},
	}
	r := &Result{}
	checkDerivers(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		"deriver TriageTickets is declared twice",
		"deriver Twice: names schema Ticket twice",
		"deriver Negative: concurrency is -1; it is one or more",
		"deriver TooLarge: batchSize is 101; it is from 1 to 100",
		"deriver TicketView takes the name of a type of the schema; a deriver's class is no type, so give one of them another name",
		"deriver TicketQueries takes the name of an operation set of the schema; give one of them another name",
		"deriver CloseTickets takes the name of a job of the schema, and both would be the deployable tickets-close-tickets; give one of them another name",
		"deriver AnswerPings takes the name of a worker of the schema, and both would be the deployable tickets-answer-pings; give one of them another name",
		"derivers TriageTickets and triageTickets are both the member triageTickets of the API's Derivers; give one of them another name",
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors =\n%q\nwant\n%q", got, want)
	}

	db := ir.NewSchema("tickets-db", ir.SchemaKindDB)
	db.Derivers = []*ir.Deriver{{Name: "TriageTickets"}}
	r = &Result{}
	checkDerivers(db, r)
	if len(r.Errors) != 1 || r.Errors[0].Msg != "schema tickets-db is a DB service and declares derivers; only an API service declares one" {
		t.Errorf("errors = %v, want the DB schema's deriver refused", r.Errors)
	}
}
