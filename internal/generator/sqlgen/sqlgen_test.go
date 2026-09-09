package sqlgen

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixturesDir = "../../loader/tsreader/testdata/services"

// TestWriteDDLGolden generates DDL for the fixture-db service and compares
// create.sql / drop.sql against their golden copies. Regenerate with:
// go test ./internal/generator/sqlgen -run TestWriteDDLGolden -update
func TestWriteDDLGolden(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}

	output, err := Generate(schema, Options{
		SchemaName: "fixture-db",
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil {
		t.Fatal("expected DDL output for fixture-db")
	}

	outDir := t.TempDir()
	if err := WriteDDL(output, outDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}

	goldenDir := filepath.Join("testdata", "golden", "fixture-db")
	for _, name := range []string{"create.sql", "drop.sql"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}

		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				t.Fatalf("create golden dir: %v", err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", name, err)
			}
			continue
		}

		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept):\n--- got ---\n%s", name, got)
		}
	}
}

// TestGenerateSkipsNonTableSchemas verifies that schemas without DBTable
// types produce no DDL output.
func TestGenerateSkipsNonTableSchemas(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}

	output, err := Generate(schema, Options{SchemaName: "fixture-api"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output != nil {
		t.Fatalf("expected nil output for API schema, got %d tables", len(output.Tables))
	}
}

// TestGenerateRelationships exercises @hasMany FK injection, @manyToMany
// join tables, @relation constraints, automatic primary keys, and array
// column defaults on a hand-built IR.
func TestGenerateRelationships(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)

	schema.Types["Person"] = &ir.TypeDef{
		Name: "Person",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "nicknames", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true},
			{Name: "pets", TypeRef: ir.TypeRef{Name: "Pet", IsArray: true}, Required: true, HasMany: true},
			{Name: "tags", TypeRef: ir.TypeRef{Name: "Tag", IsArray: true}, Required: true, ManyToMany: true},
			{Name: "managerId", TypeRef: ir.TypeRef{Name: "string"}, Relation: &ir.RelationDef{Type: "Person"}},
		},
	}
	schema.Types["Pet"] = &ir.TypeDef{
		Name: "Pet",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "owner", TypeRef: ir.TypeRef{Name: "Person"}, Required: true},
		},
	}
	schema.Types["Tag"] = &ir.TypeDef{
		Name: "Tag",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	// API-facing and JSON payload types must not become tables.
	schema.Types["PersonView"] = &ir.TypeDef{
		Name:   "PersonView",
		Role:   ir.RoleAPIView,
		Fields: []*ir.FieldDef{{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
	}
	schema.Types["Payload"] = &ir.TypeDef{
		Name:      "Payload",
		Role:      ir.RoleDBTable,
		JsonField: true,
		Fields:    []*ir.FieldDef{{Name: "data", TypeRef: ir.TypeRef{Name: "string"}}},
	}

	output, err := Generate(schema, Options{
		SchemaName: "synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if len(output.Tables) != 3 {
		names := make([]string, 0, len(output.Tables))
		for _, tbl := range output.Tables {
			names = append(names, tbl.Name)
		}
		t.Fatalf("expected 3 tables, got %v", names)
	}

	tables := map[string]*Table{}
	for i := range output.Tables {
		tables[output.Tables[i].Name] = &output.Tables[i]
	}

	// Pet gets a person_id FK column from Person.pets @hasMany, plus the
	// owner_id FK from its own object reference.
	pet := tables["pet"]
	if pet == nil {
		t.Fatal("pet table missing")
	}
	colNames := map[string]bool{}
	for _, col := range pet.Columns {
		colNames[col.Name] = true
	}
	if !colNames["owner_id"] || !colNames["person_id"] {
		t.Errorf("pet columns missing FK columns: %v", colNames)
	}
	if !colNames["id"] {
		t.Error("pet table missing auto-injected id primary key")
	}

	// Person.managerId @relation adds a self-referencing FK.
	person := tables["person"]
	foundManagerFK := false
	for _, fk := range person.ForeignKeys {
		if fk.Column == "manager_id" && fk.RefTable == "person" {
			foundManagerFK = true
		}
	}
	if !foundManagerFK {
		t.Errorf("person missing manager_id self FK: %+v", person.ForeignKeys)
	}

	// Person.tags @manyToMany creates a person_tag join table.
	if len(output.JoinTables) != 1 || output.JoinTables[0].Name != "person_tag" {
		t.Fatalf("expected person_tag join table, got %+v", output.JoinTables)
	}

	outDir := t.TempDir()
	if err := WriteDDL(output, outDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}

	createSQL, err := os.ReadFile(filepath.Join(outDir, "create.sql"))
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	for _, want := range []string{
		"CREATE TABLE person_tag (",
		"nicknames TEXT[] DEFAULT '{}' NOT NULL",
		"ADD CONSTRAINT fk_person_manager_id",
		"ADD CONSTRAINT fk_pet_owner_id",
		"ADD CONSTRAINT fk_pet_person_id",
		"CREATE EXTENSION IF NOT EXISTS pgcrypto;",
	} {
		if !strings.Contains(string(createSQL), want) {
			t.Errorf("create.sql missing %q:\n%s", want, createSQL)
		}
	}
	for _, banned := range []string{"person_view", "payload"} {
		if strings.Contains(string(createSQL), banned) {
			t.Errorf("create.sql must not contain %q:\n%s", banned, createSQL)
		}
	}

	dropSQL, err := os.ReadFile(filepath.Join(outDir, "drop.sql"))
	if err != nil {
		t.Fatalf("read drop.sql: %v", err)
	}
	if !strings.Contains(string(dropSQL), "DROP TABLE IF EXISTS person_tag CASCADE;") {
		t.Errorf("drop.sql missing join table drop:\n%s", dropSQL)
	}
}

func TestGenerateCompositeJSONBDefaultCanonicalizesAndEscapes(t *testing.T) {
	serviceDir := t.TempDir()
	files := map[string]string{
		"schema.config.json": `{"name":"fixture-default-db","kind":"DB","outputs":{}}`,
		"src/default.schema.json": `{
			"types": {
				"DefaultPayload": {
					"name": "DefaultPayload",
					"role": "EmbeddedStruct",
					"jsonField": true,
					"fields": [
						{"name": "label", "typeRef": {"name": "string"}, "required": true},
						{"name": "values", "typeRef": {"name": "number", "isArray": true}, "required": true}
					]
				},
				"FixtureRow": {
					"name": "FixtureRow",
					"role": "DBTable",
					"fields": [
						{"name": "payload", "typeRef": {"name": "DefaultPayload"}, "jsonField": true, "platformDefault": "DefaultPayload"}
					]
				}
			}
		}`,
		"src/default-payload.platform-default.json": `{
			"type": "DefaultPayload",
			"value": {"values": [3, 1], "label": "O'Reilly"}
		}`,
	}
	for rel, contents := range files {
		path := filepath.Join(serviceDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	schema, err := loader.LoadService(serviceDir)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-default-db"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteDDL(output, outDir); err != nil {
		t.Fatalf("write DDL: %v", err)
	}
	createSQL, err := os.ReadFile(filepath.Join(outDir, "create.sql"))
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	const want = `payload JSONB DEFAULT '{"label":"O''Reilly","values":[3,1]}'::jsonb`
	if !strings.Contains(string(createSQL), want) {
		t.Fatalf("create.sql missing canonical escaped JSONB default %q:\n%s", want, createSQL)
	}
}

func TestGenerateVersionedTableHistoryDDL(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Types["EventLog"] = &ir.TypeDef{
		Name:      "EventLog",
		Role:      ir.RoleDBTable,
		Versioned: true,
		Fields: []*ir.FieldDef{
			{Name: "eventId", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "message", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(output.HistoryTables) != 1 {
		t.Fatalf("expected one history table, got %+v", output.HistoryTables)
	}
	history := output.HistoryTables[0]
	if history.Name != "event_log_history" || history.KeyColumn != "event_id" || history.KeyColumnType != "TEXT" {
		t.Fatalf("unexpected history metadata: %+v", history)
	}

	outDir := t.TempDir()
	if err := WriteDDL(output, outDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}

	createSQL, err := os.ReadFile(filepath.Join(outDir, "create.sql"))
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	for _, want := range []string{
		"CREATE EXTENSION IF NOT EXISTS pgcrypto;",
		"_version BIGINT DEFAULT 1 NOT NULL",
		"CREATE TABLE event_log_history (",
		"event_id TEXT NOT NULL",
		"CREATE UNIQUE INDEX uq_event_log_history_event_id_version ON event_log_history (event_id, _version);",
		"CREATE INDEX idx_event_log_history_event_id_recorded ON event_log_history (event_id, recorded_at);",
		"CREATE OR REPLACE FUNCTION event_log_capture_history() RETURNS trigger AS $$",
		"VALUES (OLD.event_id, OLD._version + 1, 'DELETE', to_jsonb(OLD));",
		"NEW._version := OLD._version + 1;",
		"VALUES (NEW.event_id, NEW._version, TG_OP, to_jsonb(NEW));",
		"CREATE TRIGGER trg_event_log_capture_history_write",
		"BEFORE INSERT OR UPDATE ON event_log",
		"CREATE TRIGGER trg_event_log_capture_history_delete",
		"AFTER DELETE ON event_log",
	} {
		if !strings.Contains(string(createSQL), want) {
			t.Errorf("create.sql missing %q:\n%s", want, createSQL)
		}
	}

	dropSQL, err := os.ReadFile(filepath.Join(outDir, "drop.sql"))
	if err != nil {
		t.Fatalf("read drop.sql: %v", err)
	}
	for _, want := range []string{
		"DROP TRIGGER IF EXISTS trg_event_log_capture_history_write ON event_log;",
		"DROP TRIGGER IF EXISTS trg_event_log_capture_history_delete ON event_log;",
		"DROP FUNCTION IF EXISTS event_log_capture_history();",
		"DROP INDEX IF EXISTS idx_event_log_history_event_id_recorded CASCADE;",
		"DROP INDEX IF EXISTS uq_event_log_history_event_id_version CASCADE;",
		"DROP TABLE IF EXISTS event_log_history CASCADE;",
		"DROP TABLE IF EXISTS event_log CASCADE;",
	} {
		if !strings.Contains(string(dropSQL), want) {
			t.Errorf("drop.sql missing %q:\n%s", want, dropSQL)
		}
	}
}

// TestGeneratePruneKeepReferencedByDDL verifies that a versioned table with
// pruneKeepReferencedBy excludes externally pinned (key, _version) pairs from
// the generated prune function's delete candidates.
func TestGeneratePruneKeepReferencedByDDL(t *testing.T) {
	retentionDays := 90
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Types["EventLog"] = &ir.TypeDef{
		Name:      "EventLog",
		Role:      ir.RoleDBTable,
		Versioned: true,
		VersionedConfig: &ir.VersionedConfig{
			RetentionDays: &retentionDays,
			PruneKeepReferencedBy: []*ir.PruneReference{{
				Table:         "commit_entry",
				KeyColumn:     "entity_id",
				VersionColumn: "entity_version",
			}},
		},
		Fields: []*ir.FieldDef{
			{Name: "eventId", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "message", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	outDir := t.TempDir()
	if err := WriteDDL(output, outDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}

	createSQL, err := os.ReadFile(filepath.Join(outDir, "create.sql"))
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	for _, want := range []string{
		"CREATE OR REPLACE FUNCTION event_log_prune_history(retention_days INTEGER DEFAULT 90, max_rows INTEGER DEFAULT NULL) RETURNS BIGINT AS $$",
		"AND NOT EXISTS (",
		"FROM commit_entry pin_1",
		"WHERE pin_1.entity_id = h.event_id",
		"AND pin_1.entity_version = h._version",
	} {
		if !strings.Contains(string(createSQL), want) {
			t.Errorf("create.sql missing %q:\n%s", want, createSQL)
		}
	}
}

// TestGenerateMultiplePruneKeepReferencedByDDL: a versioned table can be
// pinned by more than one reader of its historical rows. Each declared
// reference gets its own aliased NOT EXISTS, so declaring a second pin never
// displaces the first.
func TestGenerateMultiplePruneKeepReferencedByDDL(t *testing.T) {
	retentionDays := 90
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Types["EventLog"] = &ir.TypeDef{
		Name:      "EventLog",
		Role:      ir.RoleDBTable,
		Versioned: true,
		VersionedConfig: &ir.VersionedConfig{
			RetentionDays: &retentionDays,
			PruneKeepReferencedBy: []*ir.PruneReference{
				{Table: "commit_entry", KeyColumn: "entity_id", VersionColumn: "entity_version"},
				{Table: "proposal_resolution", KeyColumn: "stash_id", VersionColumn: "stash_version"},
			},
		},
		Fields: []*ir.FieldDef{
			{Name: "eventId", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "message", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	outDir := t.TempDir()
	if err := WriteDDL(output, outDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}

	createSQL, err := os.ReadFile(filepath.Join(outDir, "create.sql"))
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	for _, want := range []string{
		"FROM commit_entry pin_1",
		"WHERE pin_1.entity_id = h.event_id",
		"AND pin_1.entity_version = h._version",
		"FROM proposal_resolution pin_2",
		"WHERE pin_2.stash_id = h.event_id",
		"AND pin_2.stash_version = h._version",
	} {
		if !strings.Contains(string(createSQL), want) {
			t.Errorf("create.sql missing %q:\n%s", want, createSQL)
		}
	}
	if got := strings.Count(string(createSQL), "AND NOT EXISTS ("); got != 2 {
		t.Errorf("create.sql has %d pin exclusions, want one per declared reference (2):\n%s", got, createSQL)
	}
}

func TestGenerateConfiguredVersionedTableHistoryDDL(t *testing.T) {
	retentionDays := 90
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Types["EventLog"] = &ir.TypeDef{
		Name:      "EventLog",
		Role:      ir.RoleDBTable,
		Versioned: true,
		VersionedConfig: &ir.VersionedConfig{
			RetentionDays: &retentionDays,
			PartitionBy:   "month",
		},
		Fields: []*ir.FieldDef{
			{Name: "eventId", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "message", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(output.HistoryTables) != 1 {
		t.Fatalf("expected one history table, got %+v", output.HistoryTables)
	}
	history := output.HistoryTables[0]
	if history.RetentionDays != 90 || history.PartitionBy != "month" {
		t.Fatalf("unexpected configured history metadata: %+v", history)
	}

	outDir := t.TempDir()
	if err := WriteDDL(output, outDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}

	createSQL, err := os.ReadFile(filepath.Join(outDir, "create.sql"))
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	for _, want := range []string{
		"CREATE TABLE event_log_history (",
		"history_id UUID DEFAULT gen_random_uuid() NOT NULL,",
		") PARTITION BY RANGE (recorded_at);",
		"CREATE TABLE event_log_history_default PARTITION OF event_log_history DEFAULT;",
		"CREATE INDEX idx_event_log_history_event_id_version ON event_log_history (event_id, _version);",
		"CREATE OR REPLACE FUNCTION event_log_prune_history(retention_days INTEGER DEFAULT 90, max_rows INTEGER DEFAULT NULL) RETURNS BIGINT AS $$",
		// now() is STABLE, so the planner folds it to a constant and can use
		// the retention index; clock_timestamp() is VOLATILE and is
		// re-evaluated per row (D5).
		"WHERE h.recorded_at < now() - make_interval(days => retention_days)",
		// The caller chunks by calling repeatedly, so one pass never issues a
		// single unbounded DELETE over a whole backlog.
		"LIMIT max_rows",
		"newer.event_id = h.event_id",
		// Supersession keys on _version, not recorded_at: clock skew must not
		// let an older row look newer than the true latest history row (D4).
		"newer._version > h._version",
	} {
		if !strings.Contains(string(createSQL), want) {
			t.Errorf("create.sql missing %q:\n%s", want, createSQL)
		}
	}
	if strings.Contains(string(createSQL), "CREATE UNIQUE INDEX uq_event_log_history_event_id_version") {
		t.Errorf("partitioned history table must not emit unique index without recorded_at:\n%s", createSQL)
	}
	if strings.Contains(string(createSQL), "newer.recorded_at > h.recorded_at") {
		t.Errorf("prune supersession must not key on recorded_at (clock-skew history loss, D4):\n%s", createSQL)
	}
	if strings.Contains(string(createSQL), "h.recorded_at < clock_timestamp()") {
		t.Errorf("retention filter must not be VOLATILE (unindexable, re-evaluated per row):\n%s", createSQL)
	}
	// Without a recorded_at-leading index the retention filter seq-scans the
	// whole history table on every pass; the (key, recorded_at) index cannot
	// serve it.
	if !strings.Contains(string(createSQL), "CREATE INDEX idx_event_log_history_recorded ON event_log_history (recorded_at);") {
		t.Errorf("retention-declaring history table missing its recorded_at index:\n%s", createSQL)
	}

	dropSQL, err := os.ReadFile(filepath.Join(outDir, "drop.sql"))
	if err != nil {
		t.Fatalf("read drop.sql: %v", err)
	}
	for _, want := range []string{
		"DROP FUNCTION IF EXISTS event_log_prune_history(INTEGER);",
		"DROP INDEX IF EXISTS idx_event_log_history_event_id_version CASCADE;",
		"DROP TABLE IF EXISTS event_log_history_default CASCADE;",
	} {
		if !strings.Contains(string(dropSQL), want) {
			t.Errorf("drop.sql missing %q:\n%s", want, dropSQL)
		}
	}
}

// TestSoftDeleteUniqueIndexPredicate verifies that unique @index
// declarations on soft-deletable tables (those with a deleted_at column)
// get a WHERE deleted_at IS NULL predicate, so tombstone rows do not
// occupy the unique slot. Non-unique indexes, tables without deleted_at,
// and indexes that key on deleted_at itself are unchanged.
func TestSoftDeleteUniqueIndexPredicate(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Types["AuthStrategy"] = &ir.TypeDef{
		Name: "AuthStrategy",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "connectorId", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "verificationType", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "deletedAt", TypeRef: ir.TypeRef{Name: "string"}},
		},
		Indexes: []ir.IndexDef{
			{Keys: []string{"connectorId", "verificationType"}, Unique: true},
			{Keys: []string{"connectorId"}},
			{Keys: []string{"verificationType", "deletedAt"}, Unique: true},
		},
	}
	schema.Types["HardTable"] = &ir.TypeDef{
		Name: "HardTable",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "slug", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
		Indexes: []ir.IndexDef{
			{Keys: []string{"slug"}, Unique: true},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "synthetic",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	outDir := t.TempDir()
	if err := WriteDDL(output, outDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}

	createSQL, err := os.ReadFile(filepath.Join(outDir, "create.sql"))
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}

	for _, want := range []string{
		// Unique index on a soft-deletable table gets the predicate.
		"CREATE UNIQUE INDEX idx_auth_strategy_connector_id_verification_type ON auth_strategy USING BTREE (connector_id, verification_type) WHERE deleted_at IS NULL;",
		// Non-unique index on the same table stays predicate-less.
		"CREATE INDEX idx_auth_strategy_connector_id ON auth_strategy USING BTREE (connector_id);",
		// Unique index that keys on deleted_at itself stays predicate-less.
		"CREATE UNIQUE INDEX idx_auth_strategy_verification_type_deleted_at ON auth_strategy USING BTREE (verification_type, deleted_at);",
		// Unique index on a table without deleted_at stays predicate-less.
		"CREATE UNIQUE INDEX idx_hard_table_slug ON hard_table USING BTREE (slug);",
	} {
		if !strings.Contains(string(createSQL), want) {
			t.Errorf("create.sql missing %q:\n%s", want, createSQL)
		}
	}
}

// TestHasManyWithoutDirectiveFails verifies that a list of table types
// without @hasMany or @manyToMany is rejected.
func TestHasManyWithoutDirectiveFails(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Types["A"] = &ir.TypeDef{
		Name: "A",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "items", TypeRef: ir.TypeRef{Name: "B", IsArray: true}, Required: true},
		},
	}
	schema.Types["B"] = &ir.TypeDef{
		Name:   "B",
		Role:   ir.RoleDBTable,
		Fields: []*ir.FieldDef{{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
	}

	if _, err := Generate(schema, Options{SchemaName: "synthetic"}); err == nil {
		t.Fatal("expected error for list field without @hasMany/@manyToMany")
	}
}

// TestDetermineIndexType verifies index access method selection.
func TestDetermineIndexType(t *testing.T) {
	cases := []struct {
		types []string
		want  string
	}{
		{[]string{"TEXT"}, "BTREE"},
		{[]string{"UUID", "TIMESTAMPTZ"}, "BTREE"},
		{[]string{"ltree"}, "GIST"},
		{[]string{"JSONB"}, "GIN"},
		{[]string{"TEXT[]"}, "GIN"},
		{[]string{"geography"}, "GIST"},
	}
	for _, tc := range cases {
		if got := determineIndexType(tc.types); got != tc.want {
			t.Errorf("determineIndexType(%v) = %q, want %q", tc.types, got, tc.want)
		}
	}
}

// TestIndexNameKeepsDerivedNamesUnchanged locks the historical
// idx_{table}_{columns} form for every name that already fits, so no index in a
// deployed database is renamed by the overlong-name guard.
func TestIndexNameKeepsDerivedNamesUnchanged(t *testing.T) {
	tests := []struct {
		name      string
		tableName string
		keys      []string
		unique    bool
		want      string
	}{
		{
			name:      "single column",
			tableName: "jobs",
			keys:      []string{"tenantId"},
			want:      "idx_jobs_tenant_id",
		},
		{
			name:      "unique index keeps the idx_ prefix when derived",
			tableName: "tenant",
			keys:      []string{"slug"},
			unique:    true,
			want:      "idx_tenant_slug",
		},
		{
			name:      "longest name that still fits",
			tableName: "tap_schema_versions",
			keys:      []string{"tenantConnectorTapId", "schemaVersion"},
			want:      "idx_tap_schema_versions_tenant_connector_tap_id_schema_version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := indexName(tt.tableName, pendingIndex{keys: tt.keys, index: Index{Unique: tt.unique}})
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("indexName(%q, %v) = %q, want %q", tt.tableName, tt.keys, got, tt.want)
			}
			if len(got) > postgresIdentifierLimit {
				t.Fatalf("index name is %d bytes, want at most %d: %q", len(got), postgresIdentifierLimit, got)
			}
		})
	}
}

// TestIndexNameRejectsOverlongDerivedName proves the generator stops instead of
// packing or hashing, and that the failure names the remedy.
func TestIndexNameRejectsOverlongDerivedName(t *testing.T) {
	_, err := indexName("ingestion_parent_output_groups", pendingIndex{
		keys:  []string{"workUnitId", "parentTap", "childTap", "groupDigest"},
		index: Index{Unique: true},
	})
	if err == nil {
		t.Fatal("expected an overlong derived name to fail the build")
	}
	if !strings.Contains(err.Error(), "name: 'purpose'") {
		t.Fatalf("error does not point at the purpose-name remedy: %v", err)
	}
}

func TestIndexNameUsesPurposeToken(t *testing.T) {
	tests := []struct {
		purpose string
		unique  bool
		want    string
	}{
		{purpose: "digest", unique: true, want: "uq_ingestion_parent_output_groups_digest"},
		{purpose: "edge", want: "idx_ingestion_parent_output_groups_edge"},
		{purpose: "child_identity", unique: true, want: "uq_ingestion_parent_output_groups_child_identity"},
	}

	for _, tt := range tests {
		t.Run(tt.purpose, func(t *testing.T) {
			got, err := indexName("ingestion_parent_output_groups", pendingIndex{
				name:  tt.purpose,
				index: Index{Unique: tt.unique},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("indexName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIndexNameRejectsUnusablePurposeNames(t *testing.T) {
	tests := []struct {
		name    string
		purpose string
	}{
		{name: "overlong", purpose: strings.Repeat("x", 40)},
		{name: "uppercase", purpose: "childIdentity"},
		{name: "leading digit", purpose: "2nd_edge"},
		{name: "punctuation", purpose: "child-identity"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := indexName("ingestion_parent_output_groups", pendingIndex{
				name:  tt.purpose,
				index: Index{Unique: true},
			}); err == nil {
				t.Fatalf("expected purpose name %q to be rejected", tt.purpose)
			}
		})
	}
}

// TestAssignIndexNamesRejectsCollision covers two indexes whose derived names
// are equal because the derived form ignores uniqueness.
func TestAssignIndexNamesRejectsCollision(t *testing.T) {
	_, err := assignIndexNames("jobs", []pendingIndex{
		{keys: []string{"tenantId"}, index: Index{}},
		{keys: []string{"tenantId"}, index: Index{Unique: true}},
	})
	if err == nil {
		t.Fatal("expected colliding index names to fail the build")
	}
	if !strings.Contains(err.Error(), "idx_jobs_tenant_id") {
		t.Fatalf("error does not name the collision: %v", err)
	}
}

// TestAssignIndexNamesOnIngestionParentOutputGroups is the case that started
// Regression: four indexes on a 30-byte table name, all readable and distinct
// once each one carries a purpose.
func TestAssignIndexNamesOnIngestionParentOutputGroups(t *testing.T) {
	indexes, err := assignIndexNames("ingestion_parent_output_groups", []pendingIndex{
		{keys: []string{"workUnitId", "parentTap", "childTap", "groupDigest"}, index: Index{Unique: true}, name: "digest"},
		{keys: []string{"workUnitId", "parentTap", "childTap"}, index: Index{}, name: "edge"},
		{keys: []string{"id", "workUnitId", "tenantId", "childTap"}, index: Index{Unique: true}, name: "child_identity"},
		{keys: []string{"id", "workUnitId", "tenantId", "parentTap", "childTap"}, index: Index{Unique: true}, name: "identity"},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"uq_ingestion_parent_output_groups_digest",
		"idx_ingestion_parent_output_groups_edge",
		"uq_ingestion_parent_output_groups_child_identity",
		"uq_ingestion_parent_output_groups_identity",
	}
	seen := make(map[string]bool, len(indexes))
	for i, idx := range indexes {
		if idx.Name != want[i] {
			t.Fatalf("index %d name = %q, want %q", i, idx.Name, want[i])
		}
		if len(idx.Name) > postgresIdentifierLimit {
			t.Fatalf("index %d name is %d bytes, want at most %d: %q", i, len(idx.Name), postgresIdentifierLimit, idx.Name)
		}
		if seen[idx.Name] {
			t.Fatalf("index %d name %q is not unique", i, idx.Name)
		}
		seen[idx.Name] = true
	}
}

// TestNeedsBtreeGist verifies btree_gist detection for GIST index columns.
func TestNeedsBtreeGist(t *testing.T) {
	if needsBtreeGist("ltree") {
		t.Error("ltree should not need btree_gist")
	}
	if !needsBtreeGist("UUID") {
		t.Error("UUID should need btree_gist")
	}
	if needsBtreeGist("") {
		t.Error("empty type should not need btree_gist")
	}
}

// TestServerConstraintNameMatchesPostgres locks the emitted foreign key name to
// what the server itself would create, so the DDL never names a constraint that
// ends up in the database under a different identifier.
func TestServerConstraintNameMatchesPostgres(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "short name is untouched",
			in:   "fk_jobs_tenant_id",
			want: "fk_jobs_tenant_id",
		},
		{
			name: "name at the limit is untouched",
			in:   "fk_" + strings.Repeat("x", postgresIdentifierLimit-3),
			want: "fk_" + strings.Repeat("x", postgresIdentifierLimit-3),
		},
		{
			// Verified against PostgreSQL 14: this exact pair was observed on a production database.
			name: "overlong name truncates the way the server does",
			in:   "fk_acme_org_custom_registration_operation_receipt_result_revision_id",
			want: "fk_acme_org_custom_registration_operation_receipt_result_revisi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := serverConstraintName(tt.in)
			if got != tt.want {
				t.Fatalf("serverConstraintName(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if len(got) > postgresIdentifierLimit {
				t.Fatalf("constraint name is %d bytes, want at most %d: %q", len(got), postgresIdentifierLimit, got)
			}
		})
	}
}

func TestServerConstraintNamePreservesUTF8Boundary(t *testing.T) {
	got := serverConstraintName("fk_x" + strings.Repeat("é", 40))
	if !utf8.ValidString(got) {
		t.Fatalf("truncated constraint name is not valid UTF-8: %q", got)
	}
	if len(got) > postgresIdentifierLimit {
		t.Fatalf("constraint name is %d bytes, want at most %d: %q", len(got), postgresIdentifierLimit, got)
	}
}

func TestAssignForeignKeyNamesStampsEveryConstraint(t *testing.T) {
	table := &Table{
		Name: "tenant_connector_configuration_history",
		ForeignKeys: []ForeignKey{
			{Column: "tenant_id"},
			{Column: "authentication_strategy_id"},
		},
	}
	if err := assignForeignKeyNames(table); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"fk_tenant_connector_configuration_history_tenant_id",
		"fk_tenant_connector_configuration_history_authentication_strate",
	}
	for i, fk := range table.ForeignKeys {
		if fk.ConstraintName != want[i] {
			t.Fatalf("foreign key %d name = %q, want %q", i, fk.ConstraintName, want[i])
		}
	}
}

// TestAssignForeignKeyNamesRejectsTruncatedCollision covers the failure the
// truncation hides: two distinct columns whose constraint names are equal once
// PostgreSQL cuts them to 63 bytes.
func TestAssignForeignKeyNamesRejectsTruncatedCollision(t *testing.T) {
	table := &Table{
		Name: "acme_deployment_rollout_delivery_projection",
		ForeignKeys: []ForeignKey{
			{Column: "delivery_applied_commit_id"},
			{Column: "delivery_applied_commit_ref"},
		},
	}
	err := assignForeignKeyNames(table)
	if err == nil {
		t.Fatal("expected colliding truncated constraint names to fail the build")
	}
	if !strings.Contains(err.Error(), "delivery_applied_commit_id") {
		t.Fatalf("error does not name the colliding columns: %v", err)
	}
}
