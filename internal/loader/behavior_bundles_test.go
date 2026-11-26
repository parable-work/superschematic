package loader

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D103's step 5's bundles: one per core bundle (D103's
// Reservation, D104's Barrier, Mailbox and Assignment), each a table that
// implements it with its settings, and a bundle a project declares in its
// own schema file.
const (
	reservationDBFixture = "tsreader/testdata/services/fixture-reservation-db"
	barrierDBFixture     = "tsreader/testdata/services/fixture-barrier-db"
	mailboxDBFixture     = "tsreader/testdata/services/fixture-mailbox-db"
	assignmentDBFixture  = "tsreader/testdata/services/fixture-assignment-db"
	bundleOwnDBFixture   = "tsreader/testdata/services/fixture-bundle-own-db"
)

// bundleCase is what one fixture's table loads to: the bundles it lists,
// its uses in order (name@field#bundle), its owned fields, and the types
// the loader adds for its uses.
type bundleCase struct {
	fixture, table string
	bundles        []string
	uses           []string
	owned          []string
	added          []string
}

var bundleCases = []bundleCase{
	{
		fixture: reservationDBFixture, table: "Booking",
		bundles: []string{"Reservation"},
		uses:    []string{"Holds@#Reservation", "Payments@#Reservation", "Timers@#Reservation", "Workflow@status#Reservation"},
		owned: []string{"holdsState", "holdsTotal", "paymentsDue", "paymentsAuthorized", "paymentsCaptured", "paymentsRefunded",
			"paymentsOutstanding", "paymentsOverpaid", "paymentsCurrency", "paymentsState", "status", "statusEnteredAt"},
		added: []string{"BookingChange", "BookingDeadline", "BookingHoldsItems", "BookingPaymentsActions", "BookingPaymentsAttempts",
			"BookingPaymentsEvents", "BookingPaymentsIssues", "BookingPaymentsRefunds", "BookingTimersDeadlines"},
	},
	{
		fixture: barrierDBFixture, table: "Rollout",
		bundles: []string{"Barrier"},
		uses:    []string{"Rollups@#Barrier", "Timers@#Barrier", "Workflow@status#Barrier"},
		owned:   []string{"status", "statusEnteredAt"},
		added:   []string{"RolloutChange", "RolloutDeadline", "RolloutTimersDeadlines"},
	},
	{
		fixture: mailboxDBFixture, table: "Message",
		bundles: []string{"Mailbox"},
		uses:    []string{"StateRules@#Mailbox", "Lease@#Mailbox", "Workflow@status#Mailbox"},
		owned:   []string{"leaseHolder", "leaseToken", "leaseAcquiredAt", "leaseRenewedAt", "leaseExpiresAt", "leaseExpiries", "leaseEnded", "status", "statusEnteredAt"},
		added:   []string{"MessageDeadline", "MessageLeaseDirectives"},
	},
	{
		fixture: assignmentDBFixture, table: "Problem",
		bundles: []string{"Assignment"},
		uses:    []string{"Workflow@status#", "Seats@#Assignment"},
		owned:   []string{"status", "statusEnteredAt"},
		added:   []string{"ProblemSeatsOccupants"},
	},
	{
		fixture: assignmentDBFixture, table: "Chore",
		bundles: []string{"Assignment"},
		uses:    []string{"Seats@#Assignment"},
		added:   []string{"ChoreSeatsOccupants"},
	},
	{
		fixture: bundleOwnDBFixture, table: "Proposal",
		uses:  []string{"StateRules@#Reviewed", "Workflow@status#Reviewed"},
		owned: []string{"status", "statusEnteredAt"},
	},
}

// usesOf lists td's uses as name@field#bundle, in order.
func usesOf(td *ir.TypeDef) []string {
	var out []string
	for _, ref := range td.Behaviors {
		out = append(out, ref.Name+"@"+ref.Field+"#"+ref.Bundle)
	}
	return out
}

// ownedOf lists td's owned fields, in order.
func ownedOf(td *ir.TypeDef) []string {
	var out []string
	for _, fd := range td.Fields {
		if fd.Owned() {
			out = append(out, fd.Name)
		}
	}
	return out
}

// addedFor lists the types the loader added for table's uses, sorted.
func addedFor(schema *ir.Schema, table string) []string {
	var out []string
	for name, td := range schema.Types {
		if (td.BehaviorTable != nil && td.BehaviorTable.Row == table) || (td.BehaviorQueue != nil && td.BehaviorQueue.Row == table) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// TestBundleFixturesLoad (D103, step 5): each fixture loads with no
// warning and no error; its table stays a table, lists the bundle it
// implements with its settings, and holds the bundle's parts as its uses,
// after its own, each named as the use it is: its owned fields named for
// the part's behavior (holdsState, paymentsDue, leaseHolder) or for the
// field a part is anchored on (status, statusEnteredAt), and its
// sub-tables and queues for the table and the part's behavior
// (BookingHoldsItems, BookingDeadline), with no column, table or name of
// the bundle's; a bundle the project declares lists no bundle and holds
// its parts alone. The expanded IR is held to its golden.
func TestBundleFixturesLoad(t *testing.T) {
	loaded := map[string]*ir.Schema{}
	for _, c := range bundleCases {
		t.Run(c.table, func(t *testing.T) {
			db := loaded[c.fixture]
			if db == nil {
				var warnings []string
				warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
				var err error
				db, err = LoadService(filepath.FromSlash(c.fixture), warn)
				if err != nil {
					t.Fatal(err)
				}
				if len(warnings) != 0 {
					t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
				}
				loaded[c.fixture] = db
			}
			td := db.Types[c.table]
			if td == nil || td.IsEngineType() || !td.Optimistic {
				t.Fatalf("%s = %s", c.table, marshal(t, td))
			}
			var bundles []string
			for _, ref := range td.Bundles {
				bundles = append(bundles, ref.Name)
			}
			if !slices.Equal(bundles, c.bundles) {
				t.Errorf("%s's bundles = %v, want %v", c.table, bundles, c.bundles)
			}
			if got := usesOf(td); !slices.Equal(got, c.uses) {
				t.Errorf("%s's uses = %v, want %v", c.table, got, c.uses)
			}
			if got := ownedOf(td); !slices.Equal(got, c.owned) {
				t.Errorf("%s's owned fields = %v, want %v", c.table, got, c.owned)
			}
			if got := addedFor(db, c.table); !slices.Equal(got, c.added) {
				t.Errorf("the types added for %s = %v, want %v", c.table, got, c.added)
			}
			for _, name := range append(ownedOf(td), addedFor(db, c.table)...) {
				for _, bundle := range []string{"Reservation", "Barrier", "Mailbox", "Assignment", "Reviewed"} {
					if strings.Contains(strings.ToLower(name), strings.ToLower(bundle)) {
						t.Errorf("%s's %s is named for the bundle %s", c.table, name, bundle)
					}
				}
			}
		})
	}
	for fixture, golden := range map[string]string{
		reservationDBFixture: "fixture-reservation-db.expanded.json",
		barrierDBFixture:     "fixture-barrier-db.expanded.json",
		mailboxDBFixture:     "fixture-mailbox-db.expanded.json",
		assignmentDBFixture:  "fixture-assignment-db.expanded.json",
		bundleOwnDBFixture:   "fixture-bundle-own-db.expanded.json",
	} {
		if db := loaded[fixture]; db != nil {
			compareExpanded(t, db, golden)
		}
	}
}

// TestBundlesFromTheDataForms (D103, step 5): a JSON table's `bundles`,
// `{ "name": "Mailbox", "settings": { ... } }`, loads as the TypeScript
// form's does: the loader expands it from the registered declaration into
// the same parts, in the same order, with the same configs, and the same
// owned fields and added types follow.
func TestBundlesFromTheDataForms(t *testing.T) {
	for _, fixture := range []string{mailboxDBFixture, assignmentDBFixture} {
		ts, err := LoadService(filepath.FromSlash(fixture))
		if err != nil {
			t.Fatal(err)
		}
		types := map[string]any{}
		var tables []string
		for name, td := range ts.Types {
			if td.Origin != "" || len(td.Bundles) == 0 {
				continue
			}
			tables = append(tables, name)
			var behaviors []any
			for _, ref := range td.Behaviors {
				if ref.Bundle != "" {
					continue
				}
				behaviors = append(behaviors, map[string]any{"name": ref.Name, "field": ref.Field, "config": json.RawMessage(ref.Config)})
			}
			var fields []any
			for _, fd := range td.Fields {
				if fd.Owned() {
					continue
				}
				f := map[string]any{"name": fd.Name, "typeRef": map[string]any{"name": fd.TypeRef.Name}, "required": fd.Required}
				if fd.Key {
					f["key"], f["autoGenerated"] = true, true
				}
				fields = append(fields, f)
			}
			def := map[string]any{"name": name, "role": "DBTable", "fields": fields, "bundles": td.Bundles}
			if len(behaviors) > 0 {
				def["behaviors"] = behaviors
			}
			types[name] = def
		}
		doc := map[string]any{
			"name": ts.Name, "kind": "DB",
			"scalars": map[string]any{"Identity.UUID": map[string]any{"name": "Identity.UUID", "languagePrimitive": "string"}},
			"types":   types,
		}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		schema, err := LoadService(writeService(t, map[string]string{
			"schema.config.json":    `{"name": "` + ts.Name + `", "kind": "DB", "outputs": {}}`,
			"src/tables.schema.json": string(data),
		}))
		if err != nil {
			t.Fatalf("%s: %v\n%s", fixture, err, data)
		}
		for _, name := range tables {
			want, got := ts.Types[name], schema.Types[name]
			if marshal(t, want.Behaviors) != marshal(t, got.Behaviors) || marshal(t, want.Bundles) != marshal(t, got.Bundles) {
				t.Errorf("%s from JSON: uses %s, bundles %s\nwant uses %s, bundles %s", name, marshal(t, got.Behaviors), marshal(t, got.Bundles), marshal(t, want.Behaviors), marshal(t, want.Bundles))
			}
			if !slices.Equal(ownedOf(got), ownedOf(want)) || !slices.Equal(addedFor(schema, name), addedFor(ts, name)) {
				t.Errorf("%s from JSON: owned %v and added %v, want %v and %v", name, ownedOf(got), addedFor(schema, name), ownedOf(want), addedFor(ts, name))
			}
		}
	}
}

// TestBundleRefusals (D103, step 5): the loader refuses a data form's
// bundle on a type that is no DB table, one a table lists twice, a part
// whose behavior the table implements itself or whose field it declares,
// and settings a part's behavior does not take; the document's schema
// refuses a bundle the registry does not declare.
func TestBundleRefusals(t *testing.T) {
	const key = `{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true}`
	for _, c := range []struct{ name, types, want string }{
		{
			name:  "an embedded struct",
			types: `"Chore": {"name": "Chore", "role": "EmbeddedStruct", "fields": [` + key + `], "bundles": [{"name": "Assignment"}]}`,
			want:  "type Chore implements the bundle Assignment, and a bundle is implemented by a DB table, which Chore is not (D103)",
		},
		{
			name:  "a bundle listed twice",
			types: `"Chore": {"name": "Chore", "role": "DBTable", "fields": [` + key + `], "bundles": [{"name": "Assignment"}, {"name": "Assignment"}]}`,
			want:  "type Chore implements the bundle Assignment twice",
		},
		{
			name: "a part whose behavior the table implements",
			types: `"Chore": {"name": "Chore", "role": "DBTable", "fields": [` + key + `], "bundles": [{"name": "Assignment"}],
				"behaviors": [{"name": "Seats", "implements": true, "config": {"seats": {"owner": {}}}}]}`,
			want: "type Chore: bundle Assignment: part Seats: the type implements Seats already, and a table implements a behavior at most once (D103)",
		},
		{
			name:  "a part on a field the table declares",
			types: `"Message": {"name": "Message", "role": "DBTable", "fields": [` + key + `, {"name": "status", "typeRef": {"name": "string"}}], "bundles": [{"name": "Mailbox"}]}`,
			want:  "type Message: bundle Mailbox: part Workflow on field status anchors Workflow on field status, which the type declares; leave the field to the bundle (D103)",
		},
		{
			name:  "settings a part's behavior refuses",
			types: `"Rollout": {"name": "Rollout", "role": "DBTable", "fields": [` + key + `], "bundles": [{"name": "Barrier", "settings": {"rows": "9Agent", "link": "rollout", "timeoutMs": 0, "releasePermission": "rollouts.release"}}]}`,
			want:  "type Rollout: bundle Barrier: part Rollups: behavior Rollups config",
		},
		{
			name:  "an unregistered bundle",
			types: `"Chore": {"name": "Chore", "role": "DBTable", "fields": [` + key + `], "bundles": [{"name": "Rental"}]}`,
			want:  "value must be one of 'Assignment', 'Barrier', 'Mailbox', 'Reservation'",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadService(writeService(t, map[string]string{
				"schema.config.json":     `{"name": "bundles-db", "kind": "DB", "outputs": {}}`,
				"src/tables.schema.json": `{"name": "bundles-db", "kind": "DB", "types": {` + c.types + `}}`,
			}))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v\nwant it to contain %q", err, c.want)
			}
		})
	}
}
