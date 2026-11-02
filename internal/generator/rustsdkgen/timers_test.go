package rustsdkgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// timersService is the API that serves fixture-timers-db's Ticket's
// behavior operations, those of the Timers it implements among them
// (D103).
const timersService = "fixture-timers-api"

// TestTimersSDKGolden pins every file of the Rust SDK for
// fixture-timers-api, its tool documents included: the operations of the
// Timers Ticket implements are methods of a struct of their own, the field
// timers of the namespace, ticket.timers.fire_timer, whose tools are named
// ticket.timers.fireTimer with the method timers.fire_timer. Regenerate
// with:
// go test ./internal/generator/rustsdkgen -run TestTimersSDKGolden -update
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
		ModulePath:   "example.com/schemas/api/" + timersService,
		TypesModule:  "example.com/schemas/types/go/" + timersService,
		Dependencies: map[string]*ir.Schema{"fixture-timers-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", timersService)
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(timersService), naming.Default().RustTypesCrate(timersService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "rust", timersService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", timersService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.FieldName+"."+member.FieldName+" "+member.StructName+"::"+ep.MethodName)
			}
		}
	}
	want := "ticket.status TicketStatusOperations::transition; ticket.timers TicketTimersOperations::fire_timer; " +
		"ticket.timers TicketTimersOperations::reset_timer; ticket.timers TicketTimersOperations::snooze_timer"
	if strings.Join(got, "; ") != want {
		t.Errorf("members = %v, want %s", got, want)
	}
	tools, err := GenerateTools(sdkOutput, apiOutput, nestedArraysClock)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name+" "+tool.MethodName)
	}
	if want := "ticket.status.transition status.transition; ticket.timers.fireTimer timers.fire_timer; ticket.timers.resetTimer timers.reset_timer; ticket.timers.snoozeTimer timers.snooze_timer"; strings.Join(names, "; ") != want {
		t.Errorf("tools = %v, want %s", names, want)
	}
}
