package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// repositoryWrites are the writes a repository may have, and writeHelpers
// the unexported methods only they call.
var (
	repositoryWrites = []string{
		"CreateOne", "CreateMany", "UpsertOne", "UpsertOneResult", "UpsertMany",
		"UpdateOne", "UpdateOneIfVersion", "UpdateMany",
		"DeleteOne", "DeleteOneIfVersion", "DeleteMany", "HardDeleteOne", "Reencrypt",
	}
	writeHelpers = []string{
		"scanReturned", "upsertRow", "upsertBatches", "upsertReturned", "rowsOfKeys", "heldOfKey",
		"resealed", "updateOne", "versionMiss", "seal", "sealUpdate",
	}
)

// generatedFile is the text of the file named name of the module in dir.
func generatedFile(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// TestReleasedRepositoryOnlyReads: fixture-version-graph-db's Part is
// @graphMember({ released: true }), so the loader adds PartReleased, the
// table of its released rows (D139). Its repository has every read an
// ordinary table's has and no write, in its file, its interface and its
// no-op, and query.go leaves out the types only its writes would take.
// Part's own repository keeps its writes.
func TestReleasedRepositoryOnlyReads(t *testing.T) {
	output := generateFixture(t, versionGraphFixture)
	for _, repo := range output.Repositories {
		wantReleased, wantOf := repo.TypeName == "PartReleased", ""
		if wantReleased {
			wantOf = "Part"
		}
		if repo.Released != wantReleased || repo.ReleasedOf != wantOf {
			t.Errorf("%s: Released = %v, ReleasedOf = %q; want %v, %q", repo.TypeName, repo.Released, repo.ReleasedOf, wantReleased, wantOf)
		}
	}
	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	released := generatedFile(t, outDir, "repository_part_released.go")
	for _, read := range []string{"GetOne", "GetManyByIDs", "FindOne", "FindMany", "FindPage", "findRelated", "findRows", "pageKeyExists", "batchRecipeByIDs", "batchCommitByIDs"} {
		if !strings.Contains(released, "func (r *PartReleasedRepository) "+read+"(") {
			t.Errorf("repository_part_released.go has no %s", read)
		}
	}
	for _, write := range append(append([]string{"database"}, repositoryWrites...), writeHelpers...) {
		if strings.Contains(released, "func (r *PartReleasedRepository) "+write+"(") {
			t.Errorf("repository_part_released.go has %s, a write or what only a write uses", write)
		}
	}
	for _, text := range []string{"INSERT INTO", "UPDATE part_released", "DELETE FROM", "pgconn", "partReleasedUniqueKeys", "partReleasedKeyOf", "PartReleasedUpdate", "PartReleasedUniqueKey"} {
		if strings.Contains(released, text) {
			t.Errorf("repository_part_released.go holds %q, which only a write uses", text)
		}
	}
	if !strings.Contains(released, "// PartReleasedRepository reads PartReleased, the released rows of Part.") || !strings.Contains(released, "(D139)") {
		t.Error("PartReleasedRepository's doc comment does not say the graph's Release owns its rows")
	}

	// Part, the member, is an ordinary versioned table.
	member := generatedFile(t, outDir, "repository_part.go")
	for _, method := range []string{"GetOne", "FindMany", "FindPage", "CreateOne", "CreateMany", "UpsertOne", "UpsertOneResult", "UpsertMany", "UpdateOne", "UpdateOneIfVersion", "UpdateMany", "DeleteOne", "DeleteOneIfVersion", "DeleteMany", "database", "scanReturned"} {
		if !strings.Contains(member, "func (r *PartRepository) "+method+"(") {
			t.Errorf("repository_part.go has no %s", method)
		}
	}
	if !strings.Contains(member, "// PartRepository provides database operations for Part\n") {
		t.Error("PartRepository's doc comment is not an ordinary table's")
	}

	interfaces := generatedFile(t, outDir, "interfaces.go")
	start := strings.Index(interfaces, "type PartReleasedRepositoryInterface interface {")
	if start < 0 {
		t.Fatal("interfaces.go has no PartReleasedRepositoryInterface")
	}
	contract := interfaces[start:]
	contract = contract[:strings.Index(contract, "\n}\n")]
	var methods []string
	for _, line := range strings.Split(contract, "\n")[1:] {
		methods = append(methods, strings.TrimSpace(line[:strings.Index(line, "(")]))
	}
	if got, want := strings.Join(methods, " "), "GetOne GetManyByIDs FindOne FindMany FindPage"; got != want {
		t.Errorf("PartReleasedRepositoryInterface has %s, want %s", got, want)
	}
	for _, write := range repositoryWrites {
		if strings.Contains(interfaces, "func (r *NoOpPartReleasedRepository) "+write+"(") {
			t.Errorf("NoOpPartReleasedRepository has %s", write)
		}
	}
	for _, method := range []string{"FindPage", "CreateOne", "UpdateOne", "DeleteMany"} {
		if !strings.Contains(interfaces, "func (r *NoOpPartRepository) "+method+"(") {
			t.Errorf("NoOpPartRepository has no %s", method)
		}
	}

	query := generatedFile(t, outDir, "query.go")
	for _, text := range []string{"PartReleasedUpdate", "PartReleasedUniqueKey", "PartReleasedUniqueBy"} {
		if strings.Contains(query, text) {
			t.Errorf("query.go declares %s, which only a write of PartReleased would take", text)
		}
	}
	for _, text := range []string{
		"type PartReleasedFilter struct {", "type PartReleasedFields struct {", "type PartReleasedFindOptions struct {",
		"type PartReleasedPageOptions struct {", "func ParsePartReleasedSort(", "func PartReleasedFilterConditions(",
		"type PartUpdate struct {", "type PartUniqueKey string", "func NewPartSnapshotUpdate(",
	} {
		if !strings.Contains(query, text) {
			t.Errorf("query.go has no %q", text)
		}
	}

	readme := generatedFile(t, outDir, "README.md")
	for _, text := range []string{
		"- `repository_part_released.go` -- PartReleased repository, read-only: the released rows of Part\n",
		"(`PartReleased`) is read-only: it exposes `GetOne`, `GetManyByIDs`,\n",
		"The graph's `Release` owns those rows",
	} {
		if !strings.Contains(readme, text) {
			t.Errorf("README.md does not say %q", text)
		}
	}
}

// TestReleasedFollowsTheMarkNotTheName: a repository is read-only when its
// type is marked as a member's released rows (ir.TypeDef.ReleasedOf), and
// only then. A table the schema names like one is an ordinary table, and a
// schema with no such table says nothing of one.
func TestReleasedFollowsTheMarkNotTheName(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	schema.Types["TenantReleased"] = &ir.TypeDef{
		Name: "TenantReleased",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true, AutoGenerated: true},
			{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schema.Types["Published"] = &ir.TypeDef{
		Name:       "Published",
		Role:       ir.RoleDBTable,
		ReleasedOf: "Tenant",
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true, AutoGenerated: true},
			{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	output, err := Generate(schema, Options{
		SchemaName:  "fixture-db",
		ModulePath:  "example.com/schemas/orm/fixture-db",
		TypesModule: "example.com/schemas/types/go/fixture-db",
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if named := repositoryOf(t, output, "TenantReleased"); named.Released || named.ReleasedOf != "" {
		t.Errorf("TenantReleased, which no member releases into, is read-only: %+v", named)
	}
	if marked := repositoryOf(t, output, "Published"); !marked.Released || marked.ReleasedOf != "Tenant" {
		t.Errorf("Published: Released = %v, ReleasedOf = %q; want true, Tenant", marked.Released, marked.ReleasedOf)
	}
	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	if named := generatedFile(t, outDir, "repository_tenant_released.go"); !strings.Contains(named, "func (r *TenantReleasedRepository) CreateOne(") {
		t.Error("TenantReleased, an ordinary table, has no CreateOne")
	}
	marked := generatedFile(t, outDir, "repository_published.go")
	if !strings.Contains(marked, "func (r *PublishedRepository) FindMany(") {
		t.Error("Published has no FindMany")
	}
	for _, write := range repositoryWrites {
		if strings.Contains(marked, "func (r *PublishedRepository) "+write+"(") {
			t.Errorf("Published, a member's released rows, has %s", write)
		}
	}

	// A schema with no released rows names no read-only repository.
	plain := t.TempDir()
	if err := WriteORM(generateFixtureDB(t), plain); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	if readme := generatedFile(t, plain, "README.md"); strings.Contains(readme, "read-only") || strings.Contains(readme, "released rows") {
		t.Error("fixture-db's README.md describes a read-only repository, and the schema has none")
	}
}

const releasedShapesService = "released-shapes"

// releasedShapesSchema is a table, Stop, and StopReleased, which holds the
// columns the loader would give the table of Stop's released rows: a column
// of each shape whose read or write takes an import or a helper of its own.
// The test marks StopReleased, which the data forms cannot.
const releasedShapesSchema = `{
  "name": "released-shapes",
  "kind": "DB",
  "scalars": {
    "Identity.UUID": { "name": "Identity.UUID", "languagePrimitive": "string" },
    "Temporal.DateTime": { "name": "Temporal.DateTime", "languagePrimitive": "string" },
    "Temporal.Date": { "name": "Temporal.Date", "languagePrimitive": "string" },
    "Temporal.Duration": { "name": "Temporal.Duration", "languagePrimitive": "string" },
    "Geo.Location": { "name": "Geo.Location", "languagePrimitive": "string" },
    "Generic.JSON": { "name": "Generic.JSON", "languagePrimitive": "string" }
  },
  "types": {
    "Trip": {
      "name": "Trip",
      "role": "DBTable",
      "fields": [
        { "name": "id", "typeRef": { "name": "Identity.UUID" }, "required": true, "key": true, "autoGenerated": true },
        { "name": "title", "typeRef": { "name": "string" }, "required": true }
      ]
    },
    "Stop": {
      "name": "Stop",
      "role": "DBTable",
      "fields": [
        { "name": "id", "typeRef": { "name": "Identity.UUID" }, "required": true, "key": true, "autoGenerated": true },
        { "name": "trip", "typeRef": { "name": "Trip" }, "required": true, "relation": { "type": "Trip", "onDelete": "CASCADE" } },
        SHAPES
      ]
    },
    "StopReleased": {
      "name": "StopReleased",
      "role": "DBTable",
      "fields": [
        { "name": "id", "typeRef": { "name": "Identity.UUID" }, "required": true, "key": true, "autoGenerated": true },
        { "name": "trip", "typeRef": { "name": "Trip" }, "required": true, "relation": { "type": "Trip", "onDelete": "CASCADE" } },
        { "name": "entityKey", "typeRef": { "name": "Identity.UUID" }, "required": true },
        SHAPES,
        { "name": "sequence", "typeRef": { "name": "number" }, "required": true }
      ]
    }
  }
}
`

// releasedShapes are the content columns of Stop and of StopReleased.
const releasedShapes = `{ "name": "name", "typeRef": { "name": "string" }, "required": true },
        { "name": "note", "typeRef": { "name": "string" } },
        { "name": "open", "typeRef": { "name": "boolean" } },
        { "name": "arrivesAt", "typeRef": { "name": "Temporal.DateTime" }, "required": true },
        { "name": "leftAt", "typeRef": { "name": "Temporal.DateTime" } },
        { "name": "day", "typeRef": { "name": "Temporal.Date" }, "required": true },
        { "name": "nextDay", "typeRef": { "name": "Temporal.Date" } },
        { "name": "dwell", "typeRef": { "name": "Temporal.Duration" }, "required": true },
        { "name": "delay", "typeRef": { "name": "Temporal.Duration" } },
        { "name": "splits", "typeRef": { "name": "Temporal.Duration", "isArray": true } },
        { "name": "at", "typeRef": { "name": "Geo.Location" }, "required": true },
        { "name": "dropoff", "typeRef": { "name": "Geo.Location" } },
        { "name": "route", "typeRef": { "name": "Geo.Location", "isArray": true }, "required": true },
        { "name": "tags", "typeRef": { "name": "string", "isArray": true } },
        { "name": "riderIds", "typeRef": { "name": "Identity.UUID", "isArray": true } },
        { "name": "seenAt", "typeRef": { "name": "Temporal.DateTime", "isArray": true } },
        { "name": "payload", "typeRef": { "name": "Generic.JSON" } },
        { "name": "payloadList", "typeRef": { "name": "Generic.JSON", "isArray": true } },
        { "name": "payloadGrid", "typeRef": { "name": "Generic.JSON", "isArray": true, "isArrayOfArrays": true } },
        { "name": "payloadByName", "typeRef": { "name": "Generic.JSON", "isMap": true }, "required": true, "jsonField": true }`

// TestReleasedRepositoriesCompile generates the Go types and the ORM of
// schemas whose tables the test marks as released rows (D139), and builds
// and vets each ORM module: a read-only repository imports what its reads
// use and no more, whatever its columns. releasedShapesSchema's StopReleased
// has a column of each shape beside Stop, which keeps its writes;
// fixture-db's tables, every one read-only, have a hasMany relation,
// history, soft deletes and JSON lists; fixture-vault-db's ApiCredential
// opens its encrypted field and seals nothing; fixture-vectors-db's Passage
// and TextEmbedding keep Nearest, which runs on database; and
// fixture-fenced-db's Delivery and Depot have no fences to advance.
func TestReleasedRepositoriesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	shapes := t.TempDir()
	if err := os.MkdirAll(filepath.Join(shapes, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shapes, "schema.config.json"), []byte(`{"name": "released-shapes", "kind": "DB", "outputs": {"types": {"go": {"enabled": true}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shapes, "src", "stops.schema.json"), []byte(strings.ReplaceAll(releasedShapesSchema, "SHAPES", releasedShapes)), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, dir string
		// released are the tables to mark, every table when nil.
		released []string
		extend   func(*ir.Schema)
		// file is a generated file, want what it must hold and not what it
		// must leave out.
		file string
		want []string
		not  []string
	}{
		{
			name: releasedShapesService, dir: shapes, released: []string{"StopReleased"},
			file: "repository_stop_released.go",
			want: []string{"func (r *StopReleasedRepository) FindPage(", "\t\"time\"\n", "pgtype.Point", "pgtype.Date"},
		},
		{
			name: "fixture-db", dir: filepath.Join(fixturesDir, "fixture-db"), extend: extendFixtureForCompileCoverage,
			file: "repository_tenant.go",
			want: []string{"func (r *TenantRepository) GetVersion(", "func (r *TenantRepository) FindMany("},
		},
		{
			name: encryptedFixture, dir: filepath.Join(fixturesDir, encryptedFixture), released: []string{"ApiCredential"},
			file: "encrypted.go",
			want: []string{"func (r *ApiCredentialRepository) open(", "func (r *PlaidItemRepository) seal(", "func (r *PlaidItemRepository) Reencrypt("},
		},
		{
			name: "fixture-vectors-db", dir: filepath.Join(fixturesDir, "fixture-vectors-db"), released: []string{"LedgerLine", "Passage", "TextEmbedding"},
			file: "repository_passage.go",
			want: []string{"func (r *PassageRepository) database() *Database {"},
		},
		{
			name: "fixture-fenced-db", dir: filepath.Join(fixturesDir, "fixture-fenced-db"), released: []string{"Delivery", "Depot"},
			file: "fences.go",
			want: []string{"var parcelFences = []tableFence{"},
			not:  []string{"deliveryFences", "depotFences"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := loader.LoadService(tc.dir)
			if err != nil {
				t.Fatalf("load %s: %v", tc.name, err)
			}
			if tc.extend != nil {
				tc.extend(schema)
			}
			marked := map[string]bool{}
			for _, name := range tc.released {
				if schema.Types[name] == nil {
					t.Fatalf("%s has no table %s", tc.name, name)
				}
				marked[name] = true
			}
			for _, td := range tableTypeDefs(schema) {
				if tc.released == nil || marked[td.Name] {
					td.ReleasedOf = "Member"
					marked[td.Name] = true
				}
			}

			paths := testpaths.Local(t)
			clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
			typesModule := "example.com/schemas/types/go/" + tc.name
			root := t.TempDir()
			typesDir := filepath.Join(root, "types", "go", tc.name)
			ormDir := filepath.Join(root, "orm", tc.name)
			typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: tc.name, ModulePath: typesModule, Clock: clock})
			if err != nil {
				t.Fatalf("generate types: %v", err)
			}
			if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
				t.Fatalf("set types replace paths: %v", err)
			}
			if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
				t.Fatalf("write types: %v", err)
			}
			output, err := Generate(schema, Options{SchemaName: tc.name, ModulePath: "example.com/schemas/orm/" + tc.name, TypesModule: typesModule, Clock: clock})
			if err != nil {
				t.Fatalf("generate orm: %v", err)
			}
			if err := SetReplacePaths(output, paths, ormDir); err != nil {
				t.Fatalf("set orm replace paths: %v", err)
			}
			if err := WriteORM(output, ormDir); err != nil {
				t.Fatalf("write orm: %v", err)
			}

			for _, repo := range output.Repositories {
				if repo.Released != marked[repo.TypeName] {
					t.Errorf("%s: Released = %v, want %v", repo.TypeName, repo.Released, marked[repo.TypeName])
				}
				if !repo.Released {
					continue
				}
				file := generatedFile(t, ormDir, "repository_"+codegen.ToSnakeCase(repo.TypeName)+".go")
				if len(repo.EncryptedFields) > 0 {
					// encrypted.go holds the repository's seals and its Reencrypt.
					file += generatedFile(t, ormDir, "encrypted.go")
				}
				for _, write := range append(append([]string{}, repositoryWrites...), writeHelpers...) {
					if strings.Contains(file, "func (r *"+repo.Name+") "+write+"(") {
						t.Errorf("%s, a read-only repository, has %s", repo.Name, write)
					}
				}
			}
			file := generatedFile(t, ormDir, tc.file)
			for _, text := range tc.want {
				if !strings.Contains(file, text) {
					t.Errorf("%s has no %q", tc.file, text)
				}
			}
			for _, text := range tc.not {
				if strings.Contains(file, text) {
					t.Errorf("%s holds %q, which only a write uses", tc.file, text)
				}
			}
			runORMModule(t, ormDir)
		})
	}
}
