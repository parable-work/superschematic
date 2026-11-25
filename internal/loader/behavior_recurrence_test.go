package loader_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRecurringDutiesExampleLoads: examples/recurring-duties's schemas
// load (D152): Duty implements Recurrence with its owned fields, its
// occurrences sub-table and its deadline and change queues; Duty and
// Agreement are calendar sources; and the API serves Duty's operations and
// runs its behavior worker.
func TestRecurringDutiesExampleLoads(t *testing.T) {
	services := filepath.Join("..", "..", "examples", "recurring-duties", "schemas", "services")
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(services, "duties-db"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load duties-db: %v", err)
	}
	duty := db.Types["Duty"]
	if duty == nil {
		t.Fatal("duties-db has no Duty")
	}
	var owned []string
	for _, f := range duty.OwnedFields() {
		owned = append(owned, f.Name)
	}
	for _, name := range []string{"recurrence", "nextDueAt", "lastDoneAt"} {
		if !slices.ContainsFunc(owned, func(have string) bool { return strings.HasSuffix(strings.ToLower(have), strings.ToLower(name)) }) {
			t.Errorf("Duty has no owned field for %s (its owned fields: %v)", name, owned)
		}
	}
	var occurrences *ir.TypeDef
	for _, td := range db.Types {
		if m := td.BehaviorTable; m != nil && m.Row == "Duty" && m.Use == "Recurrence" && m.Table == "occurrences" {
			occurrences = td
		}
	}
	if occurrences == nil {
		t.Fatal("Duty keeps no occurrences sub-table")
	}
	for _, kind := range []string{ir.BehaviorQueueDeadlines, ir.BehaviorQueueChanges} {
		if db.BehaviorQueueOf("Duty", kind) == nil {
			t.Errorf("Duty has no %s queue", kind)
		}
	}
	if len(duty.CalendarSources) != 1 || !duty.CalendarSources[0].Occurrences || ir.CalendarSourceName(duty, 0) != "duty" {
		t.Errorf("Duty's calendar sources are %+v", duty.CalendarSources)
	}
	if agreement := db.Types["Agreement"]; agreement == nil || len(agreement.CalendarSources) != 1 || agreement.CalendarSources[0].Field != "endsOn" {
		t.Errorf("Agreement's calendar sources are %+v", agreement)
	}
	if _, err := loader.LoadService(filepath.Join(services, "duties-api"), loader.WithRegistry(reg)); err != nil {
		t.Fatalf("load duties-api: %v", err)
	}
}
