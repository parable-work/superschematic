package ormgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedORMCompiles generates the types module and the ORM module for
// fixture-db into a temp tree mirroring the dist layout (orm/<name> resolves
// types via ../../types/go/<name>), wires the real superscalar module, and
// runs `go build`. This is the end-to-end compile check for the ormgen port.
func TestGeneratedORMCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}

	paths := testpaths.Local(t)

	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	extendFixtureForCompileCoverage(schema)

	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	typesModule := "example.com/schemas/types/go/fixture-db"

	tempRoot := t.TempDir()
	typesDir := filepath.Join(tempRoot, "types", "go", "fixture-db")
	ormDir := filepath.Join(tempRoot, "orm", "fixture-db")

	typesOutput, err := typegen.Generate(schema, typegen.Options{
		SchemaName: "fixture-db",
		ModulePath: typesModule,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("generate types: %v", err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatalf("set types replace paths: %v", err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("write types: %v", err)
	}

	ormOutput, err := Generate(schema, Options{
		SchemaName:  "fixture-db",
		ModulePath:  "example.com/schemas/orm/fixture-db",
		TypesModule: typesModule,
		Clock:       fixedClock,
	})
	if err != nil {
		t.Fatalf("generate orm: %v", err)
	}
	if ormOutput == nil {
		t.Fatal("expected ORM output for fixture-db")
	}
	if err := SetReplacePaths(ormOutput, paths, ormDir); err != nil {
		t.Fatalf("set orm replace paths: %v", err)
	}
	if err := WriteORM(ormOutput, ormDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	ddlOutput, err := sqlgen.Generate(schema, sqlgen.Options{
		SchemaName: "fixture-db",
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("generate ddl: %v", err)
	}
	ddlDir := filepath.Join(tempRoot, "ddl", "fixture-db")
	if err := sqlgen.WriteDDL(ddlOutput, ddlDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}
	createSQL, err := os.ReadFile(filepath.Join(ddlDir, "create.sql"))
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}

	historyDecoderTest := `package orm

import (
	"testing"

	types "example.com/schemas/types/go/fixture-db"
)

func TestDecodeTenantHistoryDataSnakeCaseRoundTrip(t *testing.T) {
	raw := []byte(` + "`" + `{
		"created_at": "2026-01-02T03:04:05Z",
		"updated_at": "2026-01-02T04:05:06Z",
		"id": "00000000-0000-0000-0000-000000000001",
		"name": "Acme Corp",
		"slug": "acme",
		"email": "admin@example.com",
		"status": "active",
		"is_active": true,
		"seat_count": 12,
		"metadata": {"tier": "pro"},
		"optional_metadata": null,
		"metadata_list": [null, true],
		"metadata_by_name": {"empty": null, "set": 42},
		"_version": 2
	}` + "`" + `)

	got, err := decodeTenantHistoryData(raw)
	if err != nil {
		t.Fatalf("decodeTenantHistoryData returned error: %v", err)
	}
	if got.Id.ToUUID().String() != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("Id = %s", got.Id.ToUUID())
	}
	if got.Status != types.TenantStatus_Active {
		t.Fatalf("Status = %q", got.Status)
	}
	if got.Version != 2 {
		t.Fatalf("Version = %d, want 2", got.Version)
	}
	if got.UpdatedAt == nil {
		t.Fatal("UpdatedAt was not decoded")
	}
	if len(got.Metadata) == 0 {
		t.Fatal("Metadata was not decoded")
	}
	// A history row is to_jsonb(row), where a SQL NULL column and a JSONB
	// null both read as null; a nullable field keeps the nil it had before.
	if got.OptionalMetadata != nil {
		t.Fatalf("OptionalMetadata = %#v, want nil for an ambiguous nullable history value", got.OptionalMetadata)
	}
	if len(got.MetadataList) != 2 || string(got.MetadataList[0]) != "null" || string(got.MetadataList[1]) != "true" {
		t.Fatalf("MetadataList = %#v, want the [null,true] tokens", got.MetadataList)
	}
	if string(got.MetadataByName["empty"]) != "null" || string(got.MetadataByName["set"]) != "42" {
		t.Fatalf("MetadataByName = %#v, want the null and 42 tokens", got.MetadataByName)
	}

	// A required Generic.JSON column cannot be SQL NULL, so null is the
	// JSON null value.
	nullRoot, err := decodeTenantHistoryData([]byte(` + "`" + `{"metadata":null}` + "`" + `))
	if err != nil {
		t.Fatalf("decode explicit-null history: %v", err)
	}
	if string(nullRoot.Metadata) != "null" {
		t.Fatalf("null-root Metadata = %q, want the JSON null token", string(nullRoot.Metadata))
	}
}
`
	if err := os.WriteFile(filepath.Join(ormDir, "history_decoder_test.go"), []byte(historyDecoderTest), 0o644); err != nil {
		t.Fatalf("write history decoder test: %v", err)
	}
	unionDecoderTest := `package orm

import (
	"testing"

	types "example.com/schemas/types/go/fixture-db"
)

func TestJSONUnionDecoders(t *testing.T) {
	created, err := decodeUnionRecordCreatedAgainstJSONUnion([]byte(` + "`" + `{"kind":"created","revision":7}` + "`" + `))
	if err != nil {
		t.Fatalf("decode required union: %v", err)
	}
	if value, ok := created.(types.CreatedRevision); !ok || value.Revision != 7 {
		t.Fatalf("required union = %#v, want CreatedRevision revision 7", created)
	}

	optional, err := decodeUnionRecordSupersededByJSONUnion([]byte("null"))
	if err != nil || optional != nil {
		t.Fatalf("nullable union = %#v, %v; want nil, nil", optional, err)
	}

	events, err := decodeUnionRecordEventsJSONUnion([]byte(` + "`" + `[
		{"kind":"created","revision":1},
		{"kind":"updated","revision":2}
	]` + "`" + `))
	if err != nil {
		t.Fatalf("decode union array: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("union array len = %d, want 2", len(events))
	}
	if _, ok := events[0].(types.CreatedRevision); !ok {
		t.Fatalf("events[0] = %#v, want CreatedRevision", events[0])
	}
	if _, ok := events[1].(types.UpdatedRevision); !ok {
		t.Fatalf("events[1] = %#v, want UpdatedRevision", events[1])
	}

	byName, err := decodeUnionRecordRevisionByNameJSONUnion([]byte(` + "`" + `{
		"first":{"kind":"created","revision":1},
		"last":{"kind":"updated","revision":2}
	}` + "`" + `))
	if err != nil {
		t.Fatalf("decode union map: %v", err)
	}
	if _, ok := byName["first"].(types.CreatedRevision); !ok {
		t.Fatalf("revisionByName[first] = %#v, want CreatedRevision", byName["first"])
	}
	if _, ok := byName["last"].(types.UpdatedRevision); !ok {
		t.Fatalf("revisionByName[last] = %#v, want UpdatedRevision", byName["last"])
	}
	if _, err := decodeUnionRecordCreatedAgainstJSONUnion([]byte(` + "`" + `{"kind":"deleted"}` + "`" + `)); err == nil {
		t.Fatal("an unknown union member was accepted")
	}

	// An optional map of a union holds the union interface values, like the
	// types module's field, so the update and ApplyTo paths line up.
	latest, err := decodeUnionRecordLatestByNameJSONUnion([]byte(` + "`" + `{"a":{"kind":"updated","revision":3}}` + "`" + `))
	if err != nil {
		t.Fatalf("decode optional union map: %v", err)
	}
	var row types.UnionRecord
	(&UnionRecordUpdate{LatestByName: &latest}).ApplyTo(&row)
	if value, ok := row.LatestByName["a"].(types.UpdatedRevision); !ok || value.Revision != 3 {
		t.Fatalf("latestByName[a] = %#v, want UpdatedRevision revision 3", row.LatestByName["a"])
	}
	if snapshot := NewUnionRecordSnapshotUpdate(&types.UnionRecord{}); !snapshot.LatestByNameSetNull {
		t.Fatal("a nil optional union map must snapshot as SetNull")
	}
}
`
	if err := os.WriteFile(filepath.Join(ormDir, "json_union_decoder_test.go"), []byte(unionDecoderTest), 0o644); err != nil {
		t.Fatalf("write JSON union decoder test: %v", err)
	}
	// ApplyTo covers the field shapes extendFixtureForCompileCoverage adds:
	// an optional array, a nullable enum, a required relation and updatedBy.
	applyToTest := `package orm

import (
	"testing"

	types "example.com/schemas/types/go/fixture-db"
)

func applyToUUID(t *testing.T, raw string) types.IdentityUUID {
	t.Helper()
	id, err := types.ParseIdentityUUID(raw)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", raw, err)
	}
	return id
}

func TestTenantUserUpdateApplyTo(t *testing.T) {
	var nilUpdate *TenantUserUpdate
	nilUpdate.ApplyTo(&types.TenantUser{})
	(&TenantUserUpdate{}).ApplyTo(nil)

	status := types.TenantStatus_Active
	invitedBy := applyToUUID(t, "00000000-0000-0000-0000-000000000003")
	row := types.TenantUser{
		DisplayName: "Alice",
		Roles:       []string{"admin"},
		LastStatus:  &status,
		InvitedBy:   &invitedBy,
	}
	name := "Alice Cooper"
	tenantID := applyToUUID(t, "00000000-0000-0000-0000-000000000001")
	actor := applyToUUID(t, "00000000-0000-0000-0000-000000000002")
	ignored := []string{"ignored"}
	update := &TenantUserUpdate{
		DisplayName:       &name,
		Roles:             &ignored,
		RolesSetNull:      true,
		LastStatusSetNull: true,
		TenantID:          &tenantID,
		UpdatedBy:         &actor,
	}
	update.ApplyTo(&row)

	if row.DisplayName != name {
		t.Fatalf("DisplayName = %q, want %q", row.DisplayName, name)
	}
	if row.Roles != nil {
		t.Fatalf("Roles = %v, want nil: SetNull wins over a value", row.Roles)
	}
	if row.LastStatus != nil {
		t.Fatalf("LastStatus = %v, want nil", *row.LastStatus)
	}
	if row.InvitedBy == nil || row.InvitedBy.ToUUID() != invitedBy.ToUUID() {
		t.Fatalf("InvitedBy changed without being set: %v", row.InvitedBy)
	}
	if row.Tenant.Id == nil || row.Tenant.Id.ToUUID() != tenantID.ToUUID() {
		t.Fatalf("Tenant.Id = %v, want %s", row.Tenant.Id, tenantID.ToUUID())
	}
	if row.UpdatedBy.ToUUID() != actor.ToUUID() {
		t.Fatalf("UpdatedBy = %s, want %s", row.UpdatedBy.ToUUID(), actor.ToUUID())
	}

	(&TenantUserUpdate{DisplayNameSetNull: true}).ApplyTo(&row)
	if row.DisplayName != "" {
		t.Fatalf("DisplayName = %q after SetNull, want empty", row.DisplayName)
	}
}

// Optional maps: a nil map is null, a snapshot carries a set map as a value
// and a nil one as SetNull, and ApplyTo writes both back.
func TestTenantUserOptionalMaps(t *testing.T) {
	admin := "admin"
	alias := "al"
	setting := types.GenericJSON(` + "`" + `{"on":true}` + "`" + `)
	source := types.TenantUser{
		Labels:          map[string]*string{"role": &admin, "unset": nil},
		AliasesByLocale: map[string][]*string{"en": {&alias}},
		SettingsByName:  map[string]*types.GenericJSON{"flags": &setting},
	}

	snapshot := NewTenantUserSnapshotUpdate(&source)
	if snapshot.LabelsSetNull || snapshot.Labels == nil || len(*snapshot.Labels) != 2 {
		t.Fatalf("snapshot Labels = %v (SetNull %t), want the two-key map", snapshot.Labels, snapshot.LabelsSetNull)
	}
	if snapshot.SettingsByNameSetNull || snapshot.SettingsByName == nil {
		t.Fatalf("snapshot SettingsByName = %v (SetNull %t), want the map", snapshot.SettingsByName, snapshot.SettingsByNameSetNull)
	}
	empty := NewTenantUserSnapshotUpdate(&types.TenantUser{})
	if !empty.LabelsSetNull || !empty.AliasesByLocaleSetNull || !empty.SettingsByNameSetNull {
		t.Fatal("a nil optional map must snapshot as SetNull")
	}

	var row types.TenantUser
	snapshot.ApplyTo(&row)
	if row.Labels["role"] == nil || *row.Labels["role"] != "admin" || row.Labels["unset"] != nil {
		t.Fatalf("Labels = %v, want role=admin and a null unset", row.Labels)
	}
	if len(row.AliasesByLocale["en"]) != 1 || *row.AliasesByLocale["en"][0] != "al" {
		t.Fatalf("AliasesByLocale = %v, want en=[al]", row.AliasesByLocale)
	}
	if row.SettingsByName["flags"] == nil || string(*row.SettingsByName["flags"]) != ` + "`" + `{"on":true}` + "`" + ` {
		t.Fatalf("SettingsByName = %v, want flags", row.SettingsByName)
	}

	empty.ApplyTo(&row)
	if row.Labels != nil || row.AliasesByLocale != nil || row.SettingsByName != nil {
		t.Fatalf("SetNull left maps = %v %v %v, want nil", row.Labels, row.AliasesByLocale, row.SettingsByName)
	}
}
`
	if err := os.WriteFile(filepath.Join(ormDir, "apply_to_test.go"), []byte(applyToTest), 0o644); err != nil {
		t.Fatalf("write apply to test: %v", err)
	}
	strategyATest := `package orm

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-db"
)

const strategyADDLSQL = ` + "`" + string(createSQL) + "`" + `

func TestStrategyACompositeHistoryAsOf(t *testing.T) {
	db, pool := openStrategyADatabase(t)

	tenantID := mustUUID(t, "00000000-0000-0000-0000-000000000001")
	tenantTwoID := mustUUID(t, "00000000-0000-0000-0000-000000000002")
	userOneID := mustUUID(t, "00000000-0000-0000-0000-000000000101")
	userTwoID := mustUUID(t, "00000000-0000-0000-0000-000000000102")
	userThreeID := mustUUID(t, "00000000-0000-0000-0000-000000000103")
	jsonTenantID := mustUUID(t, "00000000-0000-0000-0000-000000000003")
	// created_by / updated_by are NOT NULL on tenant_user (added by
	// extendFixtureForCompileCoverage); raw inserts must supply an actor.
	actorID := mustUUID(t, "00000000-0000-0000-0000-0000000000ff")

	execStrategyASQL(t, pool, ` + "`" + `INSERT INTO tenant (id, name, slug, email, status, is_active, seat_count, metadata, metadata_list, metadata_by_name)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10::jsonb)` + "`" + `,
		tenantID.ToUUID(), "Acme", "acme", "admin@example.com", "active", true, 5,
		` + "`" + `{"tier":"basic"}` + "`" + `, ` + "`" + `[]` + "`" + `, ` + "`" + `{}` + "`" + `)
	execStrategyASQL(t, pool, ` + "`" + `INSERT INTO tenant_user (id, tenant_id, display_name, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5)` + "`" + `, userOneID.ToUUID(), tenantID.ToUUID(), "Alice", actorID.ToUUID(), actorID.ToUUID())
	t0 := strategyADBTime(t, pool)

	execStrategyASQL(t, pool, ` + "`" + `UPDATE tenant_user SET display_name = $1 WHERE id = $2` + "`" + `, "Alice Cooper", userOneID.ToUUID())
	t1 := strategyADBTime(t, pool)

	execStrategyASQL(t, pool, ` + "`" + `UPDATE tenant SET name = $1 WHERE id = $2` + "`" + `, "Acme Labs", tenantID.ToUUID())
	execStrategyASQL(t, pool, ` + "`" + `INSERT INTO tenant_user (id, tenant_id, display_name, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5)` + "`" + `, userTwoID.ToUUID(), tenantID.ToUUID(), "Bob", actorID.ToUUID(), actorID.ToUUID())
	t2 := strategyADBTime(t, pool)

	execStrategyASQL(t, pool, ` + "`" + `DELETE FROM tenant_user WHERE id = $1` + "`" + `, userOneID.ToUUID())
	t3 := strategyADBTime(t, pool)

	execStrategyASQL(t, pool, ` + "`" + `INSERT INTO tenant (id, name, slug, email, status, is_active, seat_count, metadata, metadata_list, metadata_by_name)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10::jsonb)` + "`" + `,
		tenantTwoID.ToUUID(), "Other Corp", "other", "other@example.com", "active", true, 5,
		` + "`" + `{"tier":"basic"}` + "`" + `, ` + "`" + `[]` + "`" + `, ` + "`" + `{}` + "`" + `)
	execStrategyASQL(t, pool, ` + "`" + `UPDATE tenant_user SET tenant_id = $1 WHERE id = $2` + "`" + `, tenantTwoID.ToUUID(), userTwoID.ToUUID())
	t4 := strategyADBTime(t, pool)

	snapshot0 := reconstructTenantSnapshotAt(t, db, tenantID, t0)
	if snapshot0.Name != "Acme" || snapshot0.Version != 1 {
		t.Fatalf("t0 parent = (%q, version %d), want Acme version 1", snapshot0.Name, snapshot0.Version)
	}
	if len(snapshot0.Users) != 1 {
		t.Fatalf("t0 users len = %d, want 1", len(snapshot0.Users))
	}
	userOneAtT0 := findSnapshotUser(t, snapshot0.Users, userOneID)
	if userOneAtT0.DisplayName != "Alice" || userOneAtT0.Version != 1 {
		t.Fatalf("t0 user one = (%q, version %d), want Alice version 1", userOneAtT0.DisplayName, userOneAtT0.Version)
	}

	snapshot1 := reconstructTenantSnapshotAt(t, db, tenantID, t1)
	userOneAtT1 := findSnapshotUser(t, snapshot1.Users, userOneID)
	if snapshot1.Version != 1 || userOneAtT1.Version != 2 {
		t.Fatalf("t1 versions = parent %d child %d, want independent versions 1 and 2", snapshot1.Version, userOneAtT1.Version)
	}
	if userOneAtT1.DisplayName != "Alice Cooper" {
		t.Fatalf("t1 user one display name = %q, want Alice Cooper", userOneAtT1.DisplayName)
	}

	snapshot2 := reconstructTenantSnapshotAt(t, db, tenantID, t2)
	if snapshot2.Name != "Acme Labs" || snapshot2.Version != 2 {
		t.Fatalf("t2 parent = (%q, version %d), want Acme Labs version 2", snapshot2.Name, snapshot2.Version)
	}
	if len(snapshot2.Users) != 2 {
		t.Fatalf("t2 users len = %d, want 2", len(snapshot2.Users))
	}
	userTwoAtT2 := findSnapshotUser(t, snapshot2.Users, userTwoID)
	if userTwoAtT2.DisplayName != "Bob" || userTwoAtT2.Version != 1 {
		t.Fatalf("t2 user two = (%q, version %d), want Bob version 1", userTwoAtT2.DisplayName, userTwoAtT2.Version)
	}

	snapshot3 := reconstructTenantSnapshotAt(t, db, tenantID, t3)
	if len(snapshot3.Users) != 1 {
		t.Fatalf("t3 users len = %d, want deleted child excluded", len(snapshot3.Users))
	}
	remainingUser := findSnapshotUser(t, snapshot3.Users, userTwoID)
	if remainingUser.DisplayName != "Bob" {
		t.Fatalf("t3 remaining user = %q, want Bob", remainingUser.DisplayName)
	}

	snapshot4 := reconstructTenantSnapshotAt(t, db, tenantID, t4)
	if len(snapshot4.Users) != 0 {
		t.Fatalf("t4 users len = %d, want child moved to another tenant excluded", len(snapshot4.Users))
	}

	// Generic.JSON keeps the JSON null token as a value, apart from SQL NULL,
	// through the full scan, a selected-field scan, the map result and the
	// history decoder.
	sqlNullTenant, err := db.Tenant.GetOne(context.Background(), tenantID, nil)
	if err != nil {
		t.Fatalf("get SQL-NULL tenant: %v", err)
	}
	if sqlNullTenant.OptionalMetadata != nil {
		t.Fatalf("SQL-NULL OptionalMetadata = %#v, want nil", sqlNullTenant.OptionalMetadata)
	}
	execStrategyASQL(t, pool, ` + "`" + `INSERT INTO tenant (id, name, slug, email, status, is_active, seat_count, metadata, optional_metadata, metadata_list, metadata_by_name)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10::jsonb, $11::jsonb)` + "`" + `,
		jsonTenantID.ToUUID(), "JSON Corp", "json", "json@example.com", "active", true, 5,
		"null", "null", ` + "`" + `[null,true]` + "`" + `, ` + "`" + `{"empty":null,"set":42}` + "`" + `)

	assertJSONNulls := func(label string, got *types.Tenant, wantOptionalNull bool) {
		t.Helper()
		if got == nil || string(got.Metadata) != "null" {
			t.Fatalf("%s required Metadata = %#v, want JSON null", label, got)
		}
		if wantOptionalNull {
			if got.OptionalMetadata == nil || string(*got.OptionalMetadata) != "null" {
				t.Fatalf("%s OptionalMetadata = %#v, want a present JSON null", label, got.OptionalMetadata)
			}
		} else if got.OptionalMetadata != nil {
			t.Fatalf("%s OptionalMetadata = %#v, want nil for an ambiguous nullable history value", label, got.OptionalMetadata)
		}
		if len(got.MetadataList) != 2 || string(got.MetadataList[0]) != "null" || string(got.MetadataList[1]) != "true" {
			t.Fatalf("%s MetadataList = %#v, want [null,true]", label, got.MetadataList)
		}
		if string(got.MetadataByName["empty"]) != "null" || string(got.MetadataByName["set"]) != "42" {
			t.Fatalf("%s MetadataByName = %#v, want null/42", label, got.MetadataByName)
		}
	}

	jsonTenant, err := db.Tenant.GetOne(context.Background(), jsonTenantID, nil)
	if err != nil {
		t.Fatalf("get JSON tenant: %v", err)
	}
	assertJSONNulls("GetOne", jsonTenant, true)

	selectedJSONTenant, err := db.Tenant.GetOne(context.Background(), jsonTenantID, &TenantGetOptions{
		Fields: TenantFields{Metadata: true, OptionalMetadata: true, MetadataList: true, MetadataByName: true},
	})
	if err != nil {
		t.Fatalf("get selected JSON fields: %v", err)
	}
	assertJSONNulls("selected GetOne", selectedJSONTenant, true)

	jsonTenantsByID, err := db.Tenant.GetManyByIDs(context.Background(), []types.IdentityUUID{jsonTenantID})
	if err != nil {
		t.Fatalf("get JSON tenant map: %v", err)
	}
	assertJSONNulls("GetManyByIDs", jsonTenantsByID[jsonTenantID], true)

	jsonHistory, err := db.Tenant.ListVersions(context.Background(), jsonTenantID, nil)
	if err != nil {
		t.Fatalf("list JSON tenant history: %v", err)
	}
	if len(jsonHistory) != 1 || jsonHistory[0].Value == nil {
		t.Fatalf("JSON tenant history = %#v, want one value", jsonHistory)
	}
	assertJSONNulls("history", jsonHistory[0].Value, false)

	// Regression: hard delete of a versioned row must succeed and
	// record a tombstone at OLD._version + 1. The buggy trigger wrote the
	// tombstone at OLD._version, colliding on the unique (id, _version) history
	// index and aborting every hard delete with 23505 -- so the DELETE at t3
	// above already fails on the old template. These assertions pin the tombstone
	// version, operation, and pre-delete image.
	//
	// userOneID: insert -> update -> hard delete. History ends (v3, DELETE) with
	// the last-known image (Alice Cooper).
	userOneHistory, err := db.TenantUser.ListVersions(context.Background(), userOneID, nil)
	if err != nil {
		t.Fatalf("list versions user one: %v", err)
	}
	if len(userOneHistory) != 3 {
		t.Fatalf("user one history len = %d, want 3 (INSERT, UPDATE, DELETE)", len(userOneHistory))
	}
	userOneTombstone := userOneHistory[len(userOneHistory)-1]
	if userOneTombstone.Operation != "DELETE" || userOneTombstone.Version != 3 {
		t.Fatalf("user one tombstone = (op %q, version %d), want (DELETE, 3)", userOneTombstone.Operation, userOneTombstone.Version)
	}
	if userOneTombstone.Value == nil || userOneTombstone.Value.DisplayName != "Alice Cooper" {
		t.Fatalf("user one tombstone image = %+v, want pre-delete DisplayName Alice Cooper", userOneTombstone.Value)
	}

	// never-updated row: insert then hard delete. History = (v1 INSERT), (v2 DELETE).
	// Inserted after t4 so the as-of snapshots above are unaffected.
	execStrategyASQL(t, pool, ` + "`" + `INSERT INTO tenant_user (id, tenant_id, display_name, created_by, updated_by)
VALUES ($1, $2, $3, $4, $5)` + "`" + `, userThreeID.ToUUID(), tenantID.ToUUID(), "Carol", actorID.ToUUID(), actorID.ToUUID())
	execStrategyASQL(t, pool, ` + "`" + `DELETE FROM tenant_user WHERE id = $1` + "`" + `, userThreeID.ToUUID())

	userThreeHistory, err := db.TenantUser.ListVersions(context.Background(), userThreeID, nil)
	if err != nil {
		t.Fatalf("list versions user three: %v", err)
	}
	if len(userThreeHistory) != 2 {
		t.Fatalf("user three history len = %d, want 2 (INSERT, DELETE)", len(userThreeHistory))
	}
	if userThreeHistory[0].Operation != "INSERT" || userThreeHistory[0].Version != 1 {
		t.Fatalf("user three row 0 = (op %q, version %d), want (INSERT, 1)", userThreeHistory[0].Operation, userThreeHistory[0].Version)
	}
	if userThreeHistory[1].Operation != "DELETE" || userThreeHistory[1].Version != 2 {
		t.Fatalf("user three row 1 = (op %q, version %d), want (DELETE, 2)", userThreeHistory[1].Operation, userThreeHistory[1].Version)
	}
	if userThreeHistory[1].Value == nil || userThreeHistory[1].Value.DisplayName != "Carol" {
		t.Fatalf("user three tombstone image = %+v, want pre-delete DisplayName Carol", userThreeHistory[1].Value)
	}

	// as-of exclusion: a snapshot after the delete must not include userThreeID.
	tsAfterDelete := strategyADBTime(t, pool)
	snapshotAfterDelete := reconstructTenantSnapshotAt(t, db, tenantID, tsAfterDelete)
	for _, u := range snapshotAfterDelete.Users {
		if u.Id != nil && u.Id.ToUUID() == userThreeID.ToUUID() {
			t.Fatalf("userThreeID present in snapshot after hard delete, want excluded")
		}
	}
}

// GetManyByIDs takes and keys on each table's own primary key: a UUID column
// other than id, and a string id.
func TestGetManyByIDsKeysOnPrimaryKey(t *testing.T) {
	db, pool := openStrategyADatabase(t)
	ctx := context.Background()

	orderID := mustUUID(t, "00000000-0000-0000-0000-000000000201")
	execStrategyASQL(t, pool, "INSERT INTO shipment (order_id, carrier) VALUES ($1, $2)", orderID.ToUUID(), "carrier-a")
	shipments, err := db.Shipment.GetManyByIDs(ctx, []types.IdentityUUID{orderID})
	if err != nil {
		t.Fatalf("get shipments: %v", err)
	}
	if len(shipments) != 1 || shipments[orderID] == nil || shipments[orderID].Carrier != "carrier-a" {
		t.Fatalf("shipments = %v, want carrier-a under %s", shipments, orderID.ToUUID())
	}

	execStrategyASQL(t, pool, "INSERT INTO coupon (id, label) VALUES ($1, $2)", "SPRING", "Spring sale")
	coupons, err := db.Coupon.GetManyByIDs(ctx, []string{"SPRING", "MISSING"})
	if err != nil {
		t.Fatalf("get coupons: %v", err)
	}
	if len(coupons) != 1 || coupons["SPRING"] == nil || coupons["SPRING"].Label != "Spring sale" {
		t.Fatalf("coupons = %v, want Spring sale under SPRING", coupons)
	}
}

// openStrategyADatabase applies the generated DDL in a fresh schema and
// connects the generated ORM to it.
func openStrategyADatabase(t *testing.T) (*Database, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM Strategy A integration test")
	}

	ctx := context.Background()
	basePool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect base pool: %v", err)
	}
	t.Cleanup(basePool.Close)

	schemaName := fmt.Sprintf("superschematic_strategy_a_%d", time.Now().UnixNano())
	if _, err := basePool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", schemaName)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = basePool.Exec(context.Background(), fmt.Sprintf("DROP SCHEMA %s CASCADE", schemaName))
	})

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	// Generated DDL can use extension types installed in public (citext);
	// keep the test schema first and public after it.
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName + ",public"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		t.Fatalf("acquire ddl connection: %v", err)
	}
	if _, err := conn.Conn().PgConn().Exec(ctx, strategyADDLSQL).ReadAll(); err != nil {
		conn.Release()
		pool.Close()
		t.Fatalf("apply generated ddl: %v", err)
	}
	conn.Release()

	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	t.Cleanup(db.Close)
	return db, pool
}

func mustUUID(t *testing.T, raw string) types.IdentityUUID {
	t.Helper()
	id, err := types.ParseIdentityUUID(raw)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", raw, err)
	}
	return id
}

func execStrategyASQL(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec sql %q: %v", sql, err)
	}
}

func strategyADBTime(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var ts time.Time
	if err := pool.QueryRow(context.Background(), "SELECT clock_timestamp()").Scan(&ts); err != nil {
		t.Fatalf("read db timestamp: %v", err)
	}
	return ts
}

func reconstructTenantSnapshotAt(t *testing.T, db *Database, tenantID types.IdentityUUID, ts time.Time) *types.Tenant {
	t.Helper()
	tenant, err := db.Tenant.GetAsOf(context.Background(), tenantID, ts)
	if err != nil {
		t.Fatalf("get tenant as of %s: %v", ts.Format(time.RFC3339Nano), err)
	}
	childRecords, err := db.TenantUser.ListAsOfByTenantID(context.Background(), tenantID, ts, nil)
	if err != nil {
		t.Fatalf("list tenant users as of %s: %v", ts.Format(time.RFC3339Nano), err)
	}
	tenant.Users = make([]types.TenantUser, 0, len(childRecords))
	for _, record := range childRecords {
		tenant.Users = append(tenant.Users, *record.Value)
	}
	return tenant
}

func findSnapshotUser(t *testing.T, users []types.TenantUser, id types.IdentityUUID) types.TenantUser {
	t.Helper()
	for _, user := range users {
		if user.Id != nil && user.Id.ToUUID() == id.ToUUID() {
			return user
		}
	}
	t.Fatalf("user %s not found in snapshot", id.ToUUID())
	return types.TenantUser{}
}
`
	if err := os.WriteFile(filepath.Join(ormDir, "strategy_a_history_test.go"), []byte(strategyATest), 0o644); err != nil {
		t.Fatalf("write strategy a history test: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "enum_default_test.go"), []byte(enumDefaultTest), 0o644); err != nil {
		t.Fatalf("write enum default test: %v", err)
	}

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = ormDir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = ormDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Errorf("generated ORM module does not compile: %v\n%s", err, out)
	}

	test := exec.Command("go", "test", "./...")
	test.Dir = ormDir
	if out, err := test.CombinedOutput(); err != nil {
		t.Errorf("generated ORM module tests fail: %v\n%s", err, out)
	}

	vet := exec.Command("go", "vet", "./...")
	vet.Dir = ormDir
	if out, err := vet.CombinedOutput(); err != nil {
		t.Errorf("generated ORM module fails go vet: %v\n%s", err, out)
	}
}

// enumDefaultTest runs in the generated ORM module. Its transaction records
// the arguments of the INSERT that CreateOne and CreateMany send and fails
// the statement, so the test reads what would reach Postgres without one.
// The fixture's status is Default<TenantStatus, TenantStatus.Active>.
const enumDefaultTest = `package orm

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	types "example.com/schemas/types/go/fixture-db"
)

var errCaptured = errors.New("statement captured")

type captureTx struct {
	pgx.Tx
	args []any
}

func (tx *captureTx) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	tx.args = args
	return capturedRow{}
}

func (tx *captureTx) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	tx.args = args
	return nil, errCaptured
}

type capturedRow struct{}

func (capturedRow) Scan(...any) error { return errCaptured }

func boundStatuses(args []any) []types.TenantStatus {
	var statuses []types.TenantStatus
	for _, arg := range args {
		if status, ok := arg.(types.TenantStatus); ok {
			statuses = append(statuses, status)
		}
	}
	return statuses
}

// A required enum left at "" is inserted as its schema default; "" is
// never a member and fails the column's CHECK. A set value is kept.
func TestCreateBindsTheEnumDefaultWhenUnset(t *testing.T) {
	ctx := context.Background()
	tx := &captureTx{}
	repo := &TenantRepository{tx: tx}

	for _, tc := range []struct {
		input *types.Tenant
		want  types.TenantStatus
	}{
		{&types.Tenant{}, types.TenantStatus_Active},
		{&types.Tenant{Status: types.TenantStatus_Suspended}, types.TenantStatus_Suspended},
	} {
		if _, err := repo.CreateOne(ctx, tc.input); !errors.Is(err, errCaptured) {
			t.Fatalf("CreateOne err = %v, want the captured statement", err)
		}
		if got := boundStatuses(tx.args); !reflect.DeepEqual(got, []types.TenantStatus{tc.want}) {
			t.Errorf("CreateOne(status %q) bound %q, want [%q]", tc.input.Status, got, tc.want)
		}
	}

	inputs := []*types.Tenant{{}, {Status: types.TenantStatus_Suspended}}
	if _, err := repo.CreateMany(ctx, inputs); !errors.Is(err, errCaptured) {
		t.Fatalf("CreateMany err = %v, want the captured statement", err)
	}
	want := []types.TenantStatus{types.TenantStatus_Active, types.TenantStatus_Suspended}
	if got := boundStatuses(tx.args); !reflect.DeepEqual(got, want) {
		t.Errorf("CreateMany bound %q, want %q", got, want)
	}
}
`

// extendFixtureForCompileCoverage mutates the loaded fixture-db IR to
// exercise template branches the fixture schema does not reach: nullable,
// list and map Generic.JSON columns, a table of closed-union JSON columns
// (single, nullable, list, map and nullable map), createdBy/updatedBy user
// audit fields, scalar arrays, optional enums, optional non-audit datetime
// scalars, and optional maps.
// Soft-delete fields (deletedAt/deletedBy) live on the fixture's TenantUser
// itself. The mutation reuses scalars the fixture already resolves so the
// generated types module stays compilable.
func extendFixtureForCompileCoverage(schema *ir.Schema) {
	tenant := schema.Types["Tenant"]
	tenant.Fields = append(tenant.Fields,
		&ir.FieldDef{Name: "optionalMetadata", TypeRef: ir.TypeRef{Name: "Generic.JSON"}},
		&ir.FieldDef{Name: "metadataList", TypeRef: ir.TypeRef{Name: "Generic.JSON", IsArray: true}, Required: true, JsonField: true},
		&ir.FieldDef{Name: "metadataByName", TypeRef: ir.TypeRef{Name: "Generic.JSON", IsMap: true}, Required: true, JsonField: true},
	)

	createdKind, updatedKind := "created", "updated"
	schema.Types["CreatedRevision"] = &ir.TypeDef{
		Name: "CreatedRevision",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &createdKind},
			{Name: "revision", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
		},
	}
	schema.Types["UpdatedRevision"] = &ir.TypeDef{
		Name: "UpdatedRevision",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &updatedKind},
			{Name: "revision", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
		},
	}
	schema.Unions["RevisionRef"] = &ir.UnionDef{Name: "RevisionRef", Types: []string{"CreatedRevision", "UpdatedRevision"}}

	// Tables keyed on a UUID column other than id and on a string id.
	// GetManyByIDs read a hard-coded entity.Id and took UUID keys, so the
	// first did not compile and the second always returned an empty map.
	schema.Types["Shipment"] = &ir.TypeDef{
		Name: "Shipment",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "orderId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "carrier", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Types["Coupon"] = &ir.TypeDef{
		Name: "Coupon",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Types["UnionRecord"] = &ir.TypeDef{
		Name: "UnionRecord",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "createdAgainst", TypeRef: ir.TypeRef{Name: "RevisionRef"}, Required: true, JsonField: true},
			{Name: "supersededBy", TypeRef: ir.TypeRef{Name: "RevisionRef"}, JsonField: true},
			{Name: "events", TypeRef: ir.TypeRef{Name: "RevisionRef", IsArray: true}, Required: true, JsonField: true},
			{Name: "revisionByName", TypeRef: ir.TypeRef{Name: "RevisionRef", IsMap: true}, Required: true, JsonField: true},
			{Name: "latestByName", TypeRef: ir.TypeRef{Name: "RevisionRef", IsMap: true}, JsonField: true},
		},
	}

	tenantUser := schema.Types["TenantUser"]
	tenantUser.Fields = append(tenantUser.Fields,
		&ir.FieldDef{Name: "createdBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		&ir.FieldDef{Name: "updatedBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		&ir.FieldDef{Name: "roles", TypeRef: ir.TypeRef{Name: "string", IsArray: true}},
		&ir.FieldDef{Name: "lastStatus", TypeRef: ir.TypeRef{Name: "TenantStatus"}},
		&ir.FieldDef{Name: "lastSeenAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}},
		&ir.FieldDef{Name: "invitedBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}},
		// Optional maps: typegen emits map[string]*T, and the update, snapshot
		// and ApplyTo paths treat the map itself as the nilable value.
		&ir.FieldDef{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsMap: true}},
		&ir.FieldDef{Name: "aliasesByLocale", TypeRef: ir.TypeRef{Name: "string", IsMap: true, IsArray: true}},
		&ir.FieldDef{Name: "settingsByName", TypeRef: ir.TypeRef{Name: "Generic.JSON", IsMap: true}},
	)

	// A table whose only optional string-typed field is a map.
	schema.Types["LabelSet"] = &ir.TypeDef{
		Name: "LabelSet",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsMap: true}},
		},
	}
}
