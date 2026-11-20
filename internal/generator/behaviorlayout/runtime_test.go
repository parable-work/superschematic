package behaviorlayout_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// runtimeLayoutOf is the runtime layout of a fixture service.
func runtimeLayoutOf(t *testing.T, dir string) *behaviorlayout.Layout {
	t.Helper()
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.FromSlash(dir), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	layout, err := behaviorlayout.RuntimeOf(schema, reg)
	if err != nil {
		t.Fatal(err)
	}
	return layout
}

// The runtime layout of the timers fixture (D117): Ticket and Invoice, the
// tables its schema documents define, and none of the sub-tables or queues
// the loader adds for them, each laid out as Of lays it out but for its own
// columns, which hold every field the document declares.
func TestRuntimeLayoutHoldsEveryTableOfTheDocuments(t *testing.T) {
	const dir = "../../loader/tsreader/testdata/services/fixture-timers-db"
	runtime := runtimeLayoutOf(t, dir)
	compiled := layoutOfFixture(t, dir, "fixture-timers-db")
	var names []string
	for name := range runtime.Types {
		names = append(names, name)
	}
	slices.Sort(names)
	if got := strings.Join(names, ","); got != "Invoice,Ticket" {
		t.Fatalf("the runtime layout's types = %s, want Invoice,Ticket", got)
	}
	ticket, want := runtime.Types["Ticket"], compiled.Types["Ticket"]
	if ticket.Table != want.Table || ticket.Key != want.Key || ticket.Version != want.Version || len(ticket.Uses) != len(want.Uses) || ticket.Deadlines == nil || ticket.Changes == nil {
		t.Errorf("Ticket's runtime layout = %+v, want Of's %+v", ticket, want)
	}
	var own []string
	for _, c := range ticket.Own {
		own = append(own, c.Field+" "+c.Column)
	}
	if got := strings.Join(own, ", "); got != "title title, visitAt visit_at" {
		t.Errorf("Ticket's own = %s", got)
	}
}

// A declared audit field is an own column of the runtime layout, and the
// layout's audit is empty: the handler holds createdBy as a field the
// document declares (D117).
func TestRuntimeLayoutHoldsADeclaredAuditFieldAsOwn(t *testing.T) {
	runtime := runtimeLayoutOf(t, "../../loader/tsreader/testdata/services/fixture-workflow-principals-db")
	question := runtime.Types["Question"]
	if question == nil {
		t.Fatalf("no Question in %+v", runtime.Types)
	}
	var own []string
	for _, c := range question.Own {
		own = append(own, c.Field)
	}
	if got := strings.Join(own, ","); got != "prompt,askedOf,createdBy" {
		t.Errorf("Question's own = %s, want its declared fields, createdBy among them", got)
	}
	if len(question.Audit) != 0 {
		t.Errorf("Question's audit = %+v, want none", question.Audit)
	}
}
