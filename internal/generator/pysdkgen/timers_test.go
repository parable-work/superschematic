package pysdkgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// timersService is the API that serves fixture-timers-db's Ticket's
// behavior operations, those of the Timers it implements among them
// (D103).
const timersService = "fixture-timers-api"

// TestTimersSDKGolden pins every file of the Python SDK for
// fixture-timers-api: the operations of the Timers Ticket implements are
// methods of a class of their own, the property timers of the namespace,
// sdk.ticket.timers.fire_timer. Regenerate with:
// go test ./internal/generator/pysdkgen -run TestTimersSDKGolden -update
func TestTimersSDKGolden(t *testing.T) {
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load(timersService), load("fixture-timers-db")
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   timersService,
		Dependencies: map[string]*ir.Schema{"fixture-timers-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(sdkOutput, outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", timersService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.AccessorName+"."+member.FieldName+" "+member.ClassName+"."+ep.MethodName)
			}
		}
	}
	want := "ticket.status TicketStatusOperations.transition; ticket.timers TicketTimersOperations.fire_timer; " +
		"ticket.timers TicketTimersOperations.reset_timer; ticket.timers TicketTimersOperations.snooze_timer"
	if strings.Join(got, "; ") != want {
		t.Errorf("members = %v, want %s", got, want)
	}
}
