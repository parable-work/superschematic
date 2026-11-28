package rustrestgen

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// rowFeaturesFixture is fixture-references-api, whose @rowFeatureOperations
// sets serve the references and files of fixture-references-db's Ticket
// and Note, its one DB dependency (#723).
var rowFeaturesFixture = behaviorFixture{api: "fixture-references-api", db: "fixture-references-db"}

// rowFeaturesBucket is the Deps field of fixture-references-api's one
// bucket, which holds the files.
var rowFeaturesBucket = DepsBucket{Service: "fixture-references-files", Field: "fixture_references_files"}

// rowFeaturesOutput is fixture-references-api's crate, with its MCP
// endpoint, and its Deps set as the dispatch layer sets them: the pool
// and the Rust types of its database, on sqlite when sqlite says so, and
// its bucket.
func rowFeaturesOutput(t *testing.T, sqlite bool) *APIOutput {
	t.Helper()
	_, db, _ := rowFeaturesFixture.load(t)
	api, err := loader.LoadService(filepath.Join(fixturesDir, rowFeaturesFixture.api))
	if err != nil {
		t.Fatalf("load %s: %v", rowFeaturesFixture.api, err)
	}
	outDir := t.TempDir()
	output, err := generateFrom(api, apiSource{upstream: rowFeaturesFixture.db, upstreamIR: db}, Options{
		SchemaName:   rowFeaturesFixture.api,
		Dependencies: map[string]*ir.Schema{rowFeaturesFixture.db: db},
		TypesCrate:   "schemas-" + rowFeaturesFixture.api + "-types",
		TypesDir:     filepath.Join(outDir, "types", "rust", rowFeaturesFixture.api),
		OutputDir:    filepath.Join(outDir, "api", rowFeaturesFixture.api),
		Clock:        codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		MCP:          true,
	})
	if err != nil || output == nil {
		t.Fatalf("generate %s: %v, %v", rowFeaturesFixture.api, output, err)
	}
	if err := output.SetDeps(DepsInfo{
		Database:           rowFeaturesFixture.db,
		SQLite:             sqlite,
		Buckets:            []DepsBucket{rowFeaturesBucket},
		DatabaseTypesCrate: naming.Default().RustTypesCrate(rowFeaturesFixture.db),
		DatabaseTypesPath:  "../../types/rust/" + rowFeaturesFixture.db,
	}); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	return output
}

// TestWriteRustAPIGoldenRowFeatureOperations pins the crate of
// fixture-references-api (#723): the trait holds getTicket alone, the
// router hands each row feature operation to its function of
// src/rowfeatures.rs, which runs it through the references and the
// attachments of fixture-references-db's repositories as the request's
// caller, Implementations::new takes the pool and the bucket of the files,
// and the MCP endpoint lists the reads and addReference as tools, routed
// to those handlers. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenRowFeatureOperations -update
func TestWriteRustAPIGoldenRowFeatureOperations(t *testing.T) {
	output := rowFeaturesOutput(t, false)
	if !output.HasRowFeatureOperations() || !output.HasDatabaseTypes() || output.HasTableOperations() || output.HasGraphOperations() {
		t.Fatalf("row features %t, database types %t", output.HasRowFeatureOperations(), output.HasDatabaseTypes())
	}
	namesFile := []string{ir.RowOpCompleteUpload, ir.RowOpDownload, ir.RowOpFileAttachment, ir.RowOpRemoveAttachment}
	var ticket, note []string
	for _, endpoint := range output.RowFeatureEndpoints() {
		r := endpoint.RowFeature
		if !endpoint.RouterServed() || r.KeyField != "id" || r.Database != rowFeaturesFixture.db {
			t.Errorf("%s: %+v", endpoint.Name, r)
		}
		if names := r.FileField != ""; names != slices.Contains(namesFile, r.Operation) {
			t.Errorf("%s names a file: %t", endpoint.Name, names)
		}
		if takes := r.Bucket != ""; takes != ir.RowFeatureOperationTakesBucket(r.Operation) {
			t.Errorf("%s takes the bucket: %t", endpoint.Name, takes)
		}
		switch r.Type {
		case "Ticket":
			ticket = append(ticket, r.Operation)
		case "Note":
			note = append(note, r.Operation)
		}
	}
	slices.Sort(ticket)
	slices.Sort(note)
	all := slices.Clone(ir.RowFeatureOperationNames)
	slices.Sort(all)
	if !slices.Equal(ticket, all) {
		t.Errorf("the router serves %v of Ticket, want every row feature operation", ticket)
	}
	if want := slices.DeleteFunc(slices.Clone(all), func(op string) bool { return op == ir.RowOpRemoveAttachment }); !slices.Equal(note, want) {
		t.Errorf("the router serves %v of Note, want %v", note, want)
	}
	if output.RowFeatureBucket() != rowFeaturesBucket.Service || output.RowFeatureBucketField() != rowFeaturesBucket.Field {
		t.Errorf("the bucket = %q, field %q", output.RowFeatureBucket(), output.RowFeatureBucketField())
	}
	generated := writeGoldenAPI(t, rowFeaturesFixture.api, output)
	for file, wants := range map[string][]string{
		"src/interfaces.rs":  {"async fn get_ticket(", "pub row_features: crate::rowfeatures::RowFeatures,"},
		"src/router.rs":      {"crate::rowfeatures::ticket_add_reference(", "crate::rowfeatures::note_file_attachment("},
		"src/rowfeatures.rs": {"pub(crate) async fn ticket_begin_upload(", `RowFeatureOperation::new("Ticket", RowFeatureOp::BeginUpload)`, ".with_file(args.file)", "database_types::orm::postgres::tables(db)"},
		"src/deps.rs":        {"row_features: crate::rowfeatures::RowFeatures::new(db, attachments),", "attachments: Arc<dyn Bucket>"},
		"src/router/mcp.rs":  {`"operation": "ticket.backlinks"`, `"operation": "ticket.addReference"`},
		"Cargo.toml":         {`features = ["postgres"]`},
	} {
		for _, want := range wants {
			if !strings.Contains(generated[file], want) {
				t.Errorf("%s holds no %q", file, want)
			}
		}
	}
	for _, unwanted := range []string{"async fn backlinks(", "async fn begin_upload(", "NoteImplementation"} {
		if strings.Contains(generated["src/interfaces.rs"], unwanted) {
			t.Errorf("the trait holds a row feature operation: %q", unwanted)
		}
	}
}

// TestRowFeatureOperationsOnSQLite: on a database that lists sqlite first
// the operations run through the SQLite repositories of the file Deps.db
// holds.
func TestRowFeatureOperationsOnSQLite(t *testing.T) {
	output := rowFeaturesOutput(t, true)
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "src", RowFeaturesFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"use crate::runtime::orm::sqlite::{Database, Tables};",
		"pub fn new(db: Arc<Database>, attachments: Arc<dyn Bucket>) -> Self {",
		"database_types::orm::sqlite::tables(db)",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("src/rowfeatures.rs holds no %q", want)
		}
	}
}

// TestRowFeatureOperationsNeedTheirDeps: the operations run through the
// repositories in the database's Rust types, and the file operations sign
// the URLs of a plain bucket the API lists, so Deps without the types, of
// another database, without the bucket or with a content bucket are
// refused.
func TestRowFeatureOperationsNeedTheirDeps(t *testing.T) {
	_, db, _ := rowFeaturesFixture.load(t)
	const prefix = "rustrestgen: fixture-references-api serves "
	typesCrate := naming.Default().RustTypesCrate(rowFeaturesFixture.db)
	content := DepsBucket{Service: rowFeaturesBucket.Service, Field: rowFeaturesBucket.Field, ContentAddressed: true, MaxBytes: 1}
	for name, tc := range map[string]struct {
		deps DepsInfo
		want string
	}{
		"no Rust types":  {DepsInfo{Database: rowFeaturesFixture.db, Buckets: []DepsBucket{rowFeaturesBucket}}, "the references and files of tables of fixture-references-db (#723), whose repositories are in its Rust types, and fixture-references-db generates none; enable outputs.types.rust in fixture-references-db's config"},
		"another db":     {DepsInfo{Database: "shop-db", DatabaseTypesCrate: typesCrate, Buckets: []DepsBucket{rowFeaturesBucket}}, "the references and files of tables of fixture-references-db (#723), and its Deps hold shop-db's"},
		"no bucket":      {DepsInfo{Database: rowFeaturesFixture.db, DatabaseTypesCrate: typesCrate}, "files, in the bucket fixture-references-files, and its Deps hold no field for it: list fixture-references-files in the API's buckets (#723)"},
		"content bucket": {DepsInfo{Database: rowFeaturesFixture.db, DatabaseTypesCrate: typesCrate, Buckets: []DepsBucket{content}}, "files, in the bucket fixture-references-files, which is content addressed (D142): the attachments sign their uploads of a plain bucket until D142's naming of an object slots in (#723)"},
	} {
		t.Run(name, func(t *testing.T) {
			err := rowFeaturesFixture.generate(t, db).SetDeps(tc.deps)
			if err == nil || err.Error() != prefix+tc.want {
				t.Errorf("SetDeps = %v,\nwant %s", err, prefix+tc.want)
			}
		})
	}
}
