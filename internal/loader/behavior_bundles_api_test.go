package loader

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
)

// reservationAPIFixture serves the booking table's operations, its
// Reservation's parts', and runs its deadlines.
const reservationAPIFixture = "tsreader/testdata/services/fixture-reservation-api"

// TestBundleOperationsAreTheirParts (D103, step 5): an API's
// @behaviorOperations set over a table that implements a bundle serves the
// parts' operations, each named as the use it is, its route's member and
// handle the part's (status for the Workflow on status, Holds, Payments and
// Timers for the parts the table implements), none of them named for the
// bundle; and its @behaviorWorker runs the parts' deadline queue and change
// queue.
func TestBundleOperationsAreTheirParts(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	api, err := LoadService(filepath.FromSlash(reservationAPIFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
	}
	var uses, names []string
	for _, set := range api.OperationSets {
		if set.Name != "BookingReservation" {
			continue
		}
		for _, op := range set.Operations {
			marker := op.BehaviorOperation
			if marker == nil {
				t.Fatalf("operation %s has no behavior operation marker", op.Name)
			}
			names = append(names, op.Name)
			if !slices.Contains(uses, marker.Use) {
				uses = append(uses, marker.Use)
			}
		}
	}
	slices.Sort(uses)
	if want := []string{"Holds", "Payments", "Timers", "status"}; !slices.Equal(uses, want) {
		t.Errorf("BookingReservation's uses = %v, want %v", uses, want)
	}
	for _, want := range []string{"statusTransition", "holdsRetakeHolds", "paymentsOpenPayment", "paymentsRecordPaymentEvent", "timersResetTimer"} {
		if !slices.Contains(names, want) {
			t.Errorf("BookingReservation's operations = %v, want %s among them", names, want)
		}
	}
	for _, name := range names {
		if strings.Contains(strings.ToLower(name), "reservation") {
			t.Errorf("operation %s is named for the bundle", name)
		}
	}
	t.Logf("BookingReservation's operations: %v", names)
	var queues []string
	for _, worker := range api.Workers {
		if worker != nil && worker.Name == "BookingDeadlines" {
			queues = worker.Queues
		}
	}
	slices.Sort(queues)
	if want := []string{"BookingChange", "BookingDeadline"}; !slices.Equal(queues, want) {
		t.Errorf("BookingDeadlines' queues = %v, want %v", queues, want)
	}
}
