package writer

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestBundlesRoundTrip (D103, step 5): a table that implements a core
// bundle is written in every format as the bundle and its settings,
// `implements Reservation<Booking, { ... }>` in TypeScript and `"bundles":
// [{ "name": "Reservation", "settings": { ... } }]` in the data forms,
// without the parts, the owned fields, sub-tables and queues the loader
// adds for them, and read back to the same expanded IR.
func TestBundlesRoundTrip(t *testing.T) {
	for _, c := range []struct {
		fixture  string
		contains map[Format][]string
		// parts are the bundle's parts' behaviors, which no written form
		// names, and added what the loader adds for them.
		parts, added []string
	}{
		{
			fixture: "fixture-reservation-db",
			contains: map[Format][]string{
				FormatTS:   {`implements Reservation<Booking, { amount: "priceCents"; currency: "GBP"; interval: { end: "endsAt"; start: "startsAt" }; resource: "room" }>`, `Calendar<Room, {`},
				FormatJSON: {`"bundles": [`, `"name": "Reservation"`, `"resource": "room"`},
				FormatYAML: {"bundles:", "name: Reservation", "resource: room"},
			},
			parts: []string{"Holds", "Payments", "Timers", "Workflow"},
			added: []string{"holdExpiry", "BookingHoldsItems", "BookingDeadline", "statusEnteredAt", "paymentsDue"},
		},
		{
			fixture: "fixture-barrier-db",
			contains: map[Format][]string{
				FormatTS:   {`implements Barrier<Rollout, { link: "rollout"; releasePermission: "rollouts.release"; rows: "Agent"; timeoutMs: 300000 }>`},
				FormatJSON: {`"name": "Barrier"`, `"timeoutMs": 300000`},
				FormatYAML: {"name: Barrier", "timeoutMs: 300000"},
			},
			parts: []string{"Rollups", "Timers"},
			added: []string{"gatedStates", "RolloutDeadline"},
		},
		{
			fixture: "fixture-mailbox-db",
			contains: map[Format][]string{
				FormatTS:   {`implements Mailbox<Message, { takePermission: "messages.take"; ttlMs: 30000 }>`},
				FormatJSON: {`"name": "Mailbox"`, `"ttlMs": 30000`},
				FormatYAML: {"name: Mailbox", "ttlMs: 30000"},
			},
			parts: []string{"StateRules", "Lease", "Workflow"},
			added: []string{"onExpiry", "leaseHolder", "MessageDeadline"},
		},
		{
			fixture: "fixture-assignment-db",
			contains: map[Format][]string{
				FormatTS:   {`implements Assignment<Problem, { assign: "problems.assign"; take: "problems.take" }>`, `implements Assignment<Chore, {}>`, `status: Workflow<`},
				FormatJSON: {`"name": "Assignment"`, `"name": "Workflow"`},
				FormatYAML: {"name: Assignment", "name: Workflow"},
			},
			parts: []string{"Seats"},
			added: []string{"onFull", "SeatsOccupants"},
		},
	} {
		schema, err := loader.LoadService(tsFixtures + "/" + c.fixture)
		if err != nil {
			t.Fatalf("loading %s: %v", c.fixture, err)
		}
		schema.Description = ""
		want := normalizeIR(t, schema)
		for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
			t.Run(c.fixture+" "+string(format), func(t *testing.T) {
				source := writtenSource(t, schema, format)
				for _, want := range c.contains[format] {
					if !strings.Contains(source, want) {
						t.Errorf("written %s lacks %q:\n%s", format, want, source)
					}
				}
				unwanted := append(c.added, `"bundle":`, "bundle: ")
				for _, part := range c.parts {
					unwanted = append(unwanted, part+"<", `"name": "`+part+`"`, "name: "+part+"\n")
				}
				for _, added := range unwanted {
					if strings.Contains(source, added) {
						t.Errorf("written %s carries the bundle's %s:\n%s", format, added, source)
					}
				}
				if got := normalizeIR(t, writeAndReload(t, schema, format)); got != want {
					t.Errorf("IR mismatch after the %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
				}
			})
		}
	}
}

// TestAProjectsOwnBundleIsWrittenAsItsParts (D103, step 5): a bundle a
// project declares in its TypeScript, which the data forms cannot name, is
// written in every format as its parts, the table's own uses, and reads
// back to the same IR but for the parts' mark of the bundle.
func TestAProjectsOwnBundleIsWrittenAsItsParts(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-bundle-own-db")
	if err != nil {
		t.Fatal(err)
	}
	schema.Description = ""
	unmarked := func(s *ir.Schema) string {
		for _, td := range s.Types {
			for i := range td.Behaviors {
				td.Behaviors[i].Bundle = ""
			}
		}
		return normalizeIR(t, s)
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			source := writtenSource(t, schema, format)
			if !strings.Contains(source, "StateRules") || strings.Contains(source, "Reviewed") {
				t.Errorf("written %s, want its parts and not the bundle:\n%s", format, source)
			}
			got := unmarked(writeAndReload(t, schema, format))
			reloaded, err := loader.LoadService(tsFixtures + "/fixture-bundle-own-db")
			if err != nil {
				t.Fatal(err)
			}
			reloaded.Description = ""
			if want := unmarked(reloaded); got != want {
				t.Errorf("IR mismatch after the %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
			}
		})
	}
}
