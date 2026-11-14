package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fencedFixture is the fixture whose tables hold @fenced fields (D119).
const fencedFixture = "fixture-fenced-db"

// TestFencesOnPostgres generates the ORM module of fixture-fenced-db, then
// builds, vets and tests it. Its generated test, against the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (it skips without the database),
// writes through the repositories and reads each counter's exact value
// after every write (D119):
//
//   - an update of a column a fence covers advances that fence by one and
//     leaves the table's other fence as it was, a relation's key and a
//     value the row already holds among the columns; an update of
//     uncovered columns alone, which still stamps updated_at, advances
//     none; and one that sets columns of both advances both, once each;
//   - an update that sets a fence itself is written as it stands, and an
//     UpdateMany that filters on the fence is a compare-and-set;
//   - UpdateMany, UpsertOne's update, only if changed too, and UpsertMany's
//     advance as UpdateOne does, and an upsert that inserts, or sets no
//     field, advances nothing;
//   - a fence with no covers advances on every update; and
//   - Depot's soft deletes, one and many, advance the fence that covers
//     deleted_at.
//
// The fences' triggers are in create.sql, so every write above is one they
// allow, and a write that skips a value, or keeps the one it holds, is
// refused and changes nothing.
func TestFencesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, fencedFixture)
	if err := os.WriteFile(filepath.Join(ormDir, "fences_test.go"), []byte(fencesORMTest), 0o644); err != nil {
		t.Fatalf("write fences test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestFencesOnPostgres") {
		t.Fatalf("the generated ORM did not run TestFencesOnPostgres:\n%s", out)
	}
}

var fencesORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-fenced-db"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFenceBumps holds the rule an update's assignments are read by: a
// fence is advanced when one of them sets a column it covers, or when they
// set any column and it covers the row, and never when one sets the fence.
func TestFenceBumps(t *testing.T) {
	const plan, run = "plan_version = delivery.plan_version + 1", "run_version = delivery.run_version + 1"
	for _, tc := range []struct {
		fences []tableFence
		sets   []string
		want   []string
	}{
		{deliveryFences, []string{"address = $1", "updated_at = $2"}, []string{plan}},
		{deliveryFences, []string{"courier_id = NULL"}, []string{plan}},
		{deliveryFences, []string{"proof = EXCLUDED.proof", "address = EXCLUDED.address"}, []string{plan, run}},
		{deliveryFences, []string{"note = $1", "updated_at = $2"}, nil},
		{deliveryFences, []string{"address = $1", "plan_version = $2", "attempts = $3"}, []string{run}},
		{deliveryFences, []string{"plan_version = $1"}, nil},
		{deliveryFences, nil, nil},
		{parcelFences, []string{"note = NULL"}, []string{"version = parcel.version + 1"}},
		{parcelFences, []string{"note = $1", "version = $2"}, nil},
		{parcelFences, nil, nil},
		{depotFences, []string{"capacity = $1"}, nil},
		{depotFences, []string{"\"name\" = $1"}, []string{"revision = depot.revision + 1"}},
		// A column is read whole: name_note is no write of name.
		{depotFences, []string{"\"name_note\" = $1", "name = $2"}, nil},
	} {
		if got := fenceBumps(tc.fences, tc.sets); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("fenceBumps(%v) = %v, want %v", tc.sets, got, tc.want)
		}
	}
}

func TestFencesOnPostgres(t *testing.T) {` + strings.Replace(pageTestSetup, "PREFIX", "fences_orm", 1) + `
	ctx = WithUserID(ctx, types.NewUUID())
	text := func(s string) *string { return &s }
	count := func(n int64) *types.GenericInt64 { v := types.GenericInt64(n); return &v }

	// The fences' triggers refuse a write that does not advance a counter
	// by one, so each write below is one they allow.
	var triggers int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND NOT t.tgisinternal AND t.tgname LIKE '%_fence'", schema).Scan(&triggers); err != nil {
		t.Fatalf("read the fences' triggers: %v", err)
	}
	if triggers != 4 {
		t.Fatalf("create.sql holds %d fence triggers, want one for each of the 4 fences", triggers)
	}

	ada, err := db.Courier.CreateOne(ctx, &types.Courier{Name: "Ada"})
	if err != nil {
		t.Fatalf("create courier: %v", err)
	}
	first, err := db.Delivery.CreateOne(ctx, &types.Delivery{Address: "1 Main St"})
	if err != nil {
		t.Fatalf("create delivery: %v", err)
	}
	id := *first.Id
	// counters fails the test unless the delivery id names holds exactly
	// plan and run.
	counters := func(label string, id types.IdentityUUID, plan, run int64) *types.Delivery {
		t.Helper()
		row, err := db.Delivery.GetOne(ctx, id, nil)
		if err != nil {
			t.Fatalf("%s: read the delivery: %v", label, err)
		}
		if int64(row.PlanVersion) != plan || int64(row.RunVersion) != run {
			t.Fatalf("%s: planVersion %d, runVersion %d; want %d and %d", label, row.PlanVersion, row.RunVersion, plan, run)
		}
		return row
	}
	update := func(label string, change *DeliveryUpdate, plan, run int64) *types.Delivery {
		t.Helper()
		row, err := db.Delivery.UpdateOne(ctx, id, change)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if int64(row.PlanVersion) != plan || int64(row.RunVersion) != run {
			t.Fatalf("%s returned planVersion %d, runVersion %d; want %d and %d", label, row.PlanVersion, row.RunVersion, plan, run)
		}
		return counters(label, id, plan, run)
	}
	counters("a create", id, 0, 0)

	// A covered column advances its fence by one, and the other stays.
	update("an update of address", &DeliveryUpdate{Address: text("2 Main St")}, 1, 0)
	stamped := update("an update of note", &DeliveryUpdate{Note: text("ring twice")}, 1, 0)
	update("an update that clears note", &DeliveryUpdate{NoteSetNull: true}, 1, 0)
	if again := update("a second update of note", &DeliveryUpdate{Note: text("ring once")}, 1, 0); !time.Time(again.UpdatedAt).After(time.Time(stamped.UpdatedAt)) {
		t.Fatalf("an update of note alone stamped updated_at %v after %v; want a later stamp and no bump", again.UpdatedAt, stamped.UpdatedAt)
	}
	update("an update of attempts", &DeliveryUpdate{Attempts: count(1)}, 1, 1)
	update("an update of address and proof", &DeliveryUpdate{Address: text("3 Main St"), Proof: text("signed")}, 2, 2)
	// Two covered columns of one fence advance it once.
	update("an update of attempts and proof", &DeliveryUpdate{Attempts: count(2), ProofSetNull: true}, 2, 3)
	// A value the row already holds counts: the statement cannot know.
	update("an update of address to the address it holds", &DeliveryUpdate{Address: text("3 Main St")}, 3, 3)
	// A relation is its key's column.
	update("an update of courier", &DeliveryUpdate{CourierID: ada.Id}, 4, 3)
	update("an update that clears courier", &DeliveryUpdate{CourierIDSetNull: true}, 5, 3)

	// A write that sets the fence itself is written as it stands: the
	// caller read 5 and writes 6, and runVersion is still advanced for it.
	update("an update that sets planVersion", &DeliveryUpdate{Address: text("4 Main St"), PlanVersion: count(6)}, 6, 3)
	update("an update that sets planVersion beside attempts", &DeliveryUpdate{PlanVersion: count(7), Attempts: count(3)}, 7, 4)
	// A value that skips one is written as it stands too, and refused.
	var refused *pgconn.PgError
	if _, err := db.Delivery.UpdateOne(ctx, id, &DeliveryUpdate{Address: text("5 Main St"), PlanVersion: count(9)}); !errors.As(err, &refused) || refused.ConstraintName != "delivery_plan_version_fence" {
		t.Fatalf("an update that sets planVersion to 9 over 7 = %v; want the refusal of delivery_plan_version_fence", err)
	}
	if row := counters("a refused update", id, 7, 4); row.Address != "4 Main St" {
		t.Fatalf("a refused update left address %q", row.Address)
	}

	// UpdateMany advances each row it updates, and a filter on the fence is
	// a compare-and-set: the second, at the version the first read, finds
	// no row.
	other, err := db.Delivery.CreateOne(ctx, &types.Delivery{Address: "9 Side St"})
	if err != nil {
		t.Fatalf("create another delivery: %v", err)
	}
	seven := int64(7)
	at := &DeliveryFilter{Id: &UUIDFilter{Eq: &id}, PlanVersion: &Int64Filter{Eq: &seven}}
	if n, err := db.Delivery.UpdateMany(ctx, at, &DeliveryUpdate{Address: text("6 Main St"), PlanVersion: count(8)}); err != nil || n != 1 {
		t.Fatalf("a compare-and-set at planVersion 7 updated %d rows, %v; want 1", n, err)
	}
	counters("a compare-and-set", id, 8, 4)
	if n, err := db.Delivery.UpdateMany(ctx, at, &DeliveryUpdate{Address: text("7 Main St"), PlanVersion: count(8)}); err != nil || n != 0 {
		t.Fatalf("a second compare-and-set at planVersion 7 updated %d rows, %v; want none", n, err)
	}
	counters("a stale compare-and-set", id, 8, 4)
	if n, err := db.Delivery.UpdateMany(ctx, nil, &DeliveryUpdate{Note: text("fragile")}); err != nil || n != 2 {
		t.Fatalf("an UpdateMany of note updated %d rows, %v; want 2", n, err)
	}
	counters("an UpdateMany of note", id, 8, 4)
	counters("an UpdateMany of note", *other.Id, 0, 0)
	if n, err := db.Delivery.UpdateMany(ctx, nil, &DeliveryUpdate{Attempts: count(5)}); err != nil || n != 2 {
		t.Fatalf("an UpdateMany of attempts updated %d rows, %v; want 2", n, err)
	}
	counters("an UpdateMany of attempts", id, 8, 5)
	counters("an UpdateMany of attempts", *other.Id, 0, 1)
	if n, err := db.Delivery.UpdateMany(ctx, nil, &DeliveryUpdate{CourierID: ada.Id, Proof: text("left at door")}); err != nil || n != 2 {
		t.Fatalf("an UpdateMany of courier and proof updated %d rows, %v; want 2", n, err)
	}
	counters("an UpdateMany of courier and proof", id, 9, 6)
	counters("an UpdateMany of courier and proof", *other.Id, 1, 2)

	// An upsert's update of the row its key holds, as UpdateOne.
	held := &types.Delivery{Id: &id, Address: "ignored"}
	upsert := func(label string, change *DeliveryUpdate, opts *UpsertOptions, status UpsertStatus, plan, run int64) {
		t.Helper()
		result, err := db.Delivery.UpsertOneResult(ctx, held, DeliveryUniqueById, change, opts)
		if err != nil || result.Status != status {
			t.Fatalf("%s = %+v, %v; want %s", label, result, err, status)
		}
		if int64(result.Row.PlanVersion) != plan || int64(result.Row.RunVersion) != run {
			t.Fatalf("%s returned planVersion %d, runVersion %d; want %d and %d", label, result.Row.PlanVersion, result.Row.RunVersion, plan, run)
		}
		counters(label, id, plan, run)
	}
	upsert("an upsert that sets note", &DeliveryUpdate{Note: text("gate code 12")}, nil, UpsertUpdated, 9, 6)
	upsert("an upsert that sets proof", &DeliveryUpdate{Proof: text("photo")}, nil, UpsertUpdated, 9, 7)
	upsert("an upsert that sets address and attempts", &DeliveryUpdate{Address: text("8 Main St"), Attempts: count(6)}, nil, UpsertUpdated, 10, 8)
	upsert("an upsert that sets no field", &DeliveryUpdate{}, nil, UpsertUnchanged, 10, 8)
	changed := &UpsertOptions{OnlyIfChanged: true}
	upsert("an upsert only if changed of the address it holds", &DeliveryUpdate{Address: text("8 Main St")}, changed, UpsertUnchanged, 10, 8)
	upsert("an upsert only if changed of another address", &DeliveryUpdate{Address: text("9 Main St")}, changed, UpsertUpdated, 11, 8)
	upsert("an upsert that sets runVersion", &DeliveryUpdate{Proof: text("signed"), RunVersion: count(9)}, nil, UpsertUpdated, 11, 9)

	// UpsertMany sets the fields it names of each row a key holds, and
	// inserts the others as given.
	fresh := types.NewUUID()
	inputs := func(address string) []*types.Delivery {
		return []*types.Delivery{{Id: &id, Address: address, Note: "bell broken"}, {Id: &fresh, Address: "1 New St", Note: "new"}}
	}
	many, err := db.Delivery.UpsertMany(ctx, inputs("10 Main St"), DeliveryUniqueById, DeliveryFields{Address: true}, nil)
	if err != nil || len(many) != 2 || many[0].Status != UpsertUpdated || many[1].Status != UpsertInserted {
		t.Fatalf("an UpsertMany of address = %+v, %v; want an update and an insert", many, err)
	}
	if many[0].Row.PlanVersion != 12 || many[0].Row.RunVersion != 9 || many[0].Row.Address != "10 Main St" {
		t.Fatalf("an UpsertMany of address returned %+v; want planVersion 12 and runVersion 9", many[0].Row)
	}
	counters("an UpsertMany of address", id, 12, 9)
	counters("an UpsertMany's insert", fresh, 0, 0)
	if _, err := db.Delivery.UpsertMany(ctx, inputs("ignored"), DeliveryUniqueById, DeliveryFields{Note: true}, nil); err != nil {
		t.Fatalf("an UpsertMany of note: %v", err)
	}
	counters("an UpsertMany of note", id, 12, 9)
	counters("an UpsertMany of note", fresh, 0, 0)
	if _, err := db.Delivery.UpsertMany(ctx, inputs("11 Main St"), DeliveryUniqueById, DeliveryFields{Address: true, Proof: true, Courier: true}, &UpsertOptions{OnlyIfChanged: true}); err != nil {
		t.Fatalf("an UpsertMany of address, proof and courier: %v", err)
	}
	counters("an UpsertMany of address, proof and courier", id, 13, 10)
	// The new row's address is the input's already, and its proof and
	// courier unset as the input's, so only if changed leaves it.
	counters("an UpsertMany only if changed", fresh, 0, 0)
	// A mask that names the fence writes the input's value of it.
	driven := []*types.Delivery{{Id: &id, Address: "12 Main St", PlanVersion: 14}}
	if _, err := db.Delivery.UpsertMany(ctx, driven, DeliveryUniqueById, DeliveryFields{Address: true, PlanVersion: true}, nil); err != nil {
		t.Fatalf("an UpsertMany that names planVersion: %v", err)
	}
	counters("an UpsertMany that names planVersion", id, 14, 10)

	// A fence with no covers advances on every update.
	parcel, err := db.Parcel.CreateOne(ctx, &types.Parcel{TrackingCode: "TRK-1", WeightGrams: 500})
	if err != nil {
		t.Fatalf("create parcel: %v", err)
	}
	version := func(label string, want int64) {
		t.Helper()
		row, err := db.Parcel.GetOne(ctx, *parcel.Id, nil)
		if err != nil || int64(row.Version) != want {
			t.Fatalf("%s: the parcel = %+v, %v; want version %d", label, row, err, want)
		}
	}
	version("a create", 0)
	if _, err := db.Parcel.UpdateOne(ctx, *parcel.Id, &ParcelUpdate{Note: text("fragile")}); err != nil {
		t.Fatalf("update the parcel's note: %v", err)
	}
	version("an update of note", 1)
	if _, err := db.Parcel.UpdateOne(ctx, *parcel.Id, &ParcelUpdate{WeightGrams: count(510), NoteSetNull: true}); err != nil {
		t.Fatalf("update the parcel's weight: %v", err)
	}
	version("an update of weightGrams", 2)
	byCode := &types.Parcel{TrackingCode: "TRK-1", WeightGrams: 520}
	if result, err := db.Parcel.UpsertOneResult(ctx, byCode, ParcelUniqueByTrackingCode, &ParcelUpdate{WeightGrams: count(520)}, nil); err != nil || result.Status != UpsertUpdated || result.Row.Version != 3 {
		t.Fatalf("an upsert by trackingCode = %+v, %v; want updated at version 3", result, err)
	}
	version("an upsert by trackingCode", 3)
	if result, err := db.Parcel.UpsertOneResult(ctx, byCode, ParcelUniqueByTrackingCode, nil, nil); err != nil || result.Status != UpsertUnchanged {
		t.Fatalf("an upsert that sets no field = %+v, %v; want unchanged", result, err)
	}
	version("an upsert that sets no field", 3)
	if n, err := db.Parcel.UpdateMany(ctx, nil, &ParcelUpdate{Note: text("this side up")}); err != nil || n != 1 {
		t.Fatalf("an UpdateMany of the parcels updated %d rows, %v; want 1", n, err)
	}
	version("an UpdateMany", 4)
	if _, err := db.Parcel.UpdateOne(ctx, *parcel.Id, &ParcelUpdate{Note: text("heavy"), Version: count(5)}); err != nil {
		t.Fatalf("an update that sets version: %v", err)
	}
	version("an update that sets version", 5)
	batch, err := db.Parcel.UpsertMany(ctx, []*types.Parcel{{TrackingCode: "TRK-1", WeightGrams: 530}, {TrackingCode: "TRK-2", WeightGrams: 90}}, ParcelUniqueByTrackingCode, ParcelFields{WeightGrams: true}, nil)
	if err != nil || len(batch) != 2 || batch[0].Row.Version != 6 || batch[1].Status != UpsertInserted || batch[1].Row.Version != 0 {
		t.Fatalf("an UpsertMany of parcels = %+v, %v; want TRK-1 at version 6 and TRK-2 inserted at 0", batch, err)
	}
	version("an UpsertMany", 6)
	if _, err := db.Parcel.UpdateOne(ctx, *parcel.Id, &ParcelUpdate{Note: text("light"), Version: count(6)}); !errors.As(err, &refused) || refused.ConstraintName != "parcel_version_fence" {
		t.Fatalf("an update that sets version to the 6 it holds = %v; want the refusal of parcel_version_fence", err)
	}
	version("a refused update", 6)

	// A soft delete stamps deleted_at, which Depot's fence covers.
	revision := func(label string, id types.IdentityUUID, want int64, deleted bool) {
		t.Helper()
		var got int64
		var at *time.Time
		if err := pool.QueryRow(ctx, "SELECT revision, deleted_at FROM depot WHERE id = $1", id.ToUUID()).Scan(&got, &at); err != nil {
			t.Fatalf("%s: read the depot: %v", label, err)
		}
		if got != want || (at != nil) != deleted {
			t.Fatalf("%s: revision %d, deleted_at %v; want revision %d, deleted %t", label, got, at, want, deleted)
		}
	}
	var depots []types.IdentityUUID
	for _, name := range []string{"North", "South", "East"} {
		depot, err := db.Depot.CreateOne(ctx, &types.Depot{Name: name, Capacity: 10})
		if err != nil {
			t.Fatalf("create depot %s: %v", name, err)
		}
		depots = append(depots, *depot.Id)
	}
	north, south, east := depots[0], depots[1], depots[2]
	if _, err := db.Depot.UpdateOne(ctx, north, &DepotUpdate{Capacity: count(12)}); err != nil {
		t.Fatalf("update the depot's capacity: %v", err)
	}
	revision("an update of capacity", north, 0, false)
	if _, err := db.Depot.UpdateOne(ctx, north, &DepotUpdate{Name: text("North Yard"), Capacity: count(14)}); err != nil {
		t.Fatalf("update the depot's name: %v", err)
	}
	revision("an update of name", north, 1, false)
	if err := db.Depot.DeleteOne(ctx, north); err != nil {
		t.Fatalf("delete the depot: %v", err)
	}
	revision("a soft delete", north, 2, true)
	if err := db.Depot.DeleteOne(ctx, north); err != ErrNotFound {
		t.Fatalf("a second delete of the depot = %v; want ErrNotFound", err)
	}
	revision("a second soft delete", north, 2, true)
	if n, err := db.Depot.DeleteMany(ctx, &DepotFilter{Name: &StringFilter{In: []string{"South", "North Yard"}}}); err != nil || n != 1 {
		t.Fatalf("a DeleteMany of depots deleted %d rows, %v; want South alone", n, err)
	}
	revision("a DeleteMany", south, 1, true)
	revision("a DeleteMany", north, 2, true)
	revision("a DeleteMany of others", east, 0, false)

	// A table with no fence is written as before.
	if _, err := db.Courier.UpdateOne(ctx, *ada.Id, &CourierUpdate{Name: text("Ada L.")}); err != nil {
		t.Fatalf("update the courier: %v", err)
	}
}
`
