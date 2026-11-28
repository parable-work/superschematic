package rustrestgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// withFilesSlot gives fixture-references-api an @envVars class whose
// PREVIEWS slot takes the files interface, and its Deps the file
// processor's pass, as the dispatch layer sets them for a database that
// declares @attachments (#614).
func withFilesSlot(api *ir.Schema, deps *DepsInfo) {
	api.Types["FilesConfig"] = &ir.TypeDef{
		Name:          "FilesConfig",
		EnvVars:       true,
		ProviderSlots: map[string]string{"PREVIEWS": "files"},
		Fields:        []*ir.FieldDef{{Name: "PREVIEWS", TypeRef: ir.TypeRef{Name: "string"}}},
	}
	deps.FileProcessing = true
}

// TestWriteRustAPIGoldenFileProcessing pins src/files.rs of
// fixture-references-api with a files slot (#614): process_pending_files
// runs the HTTP runtime's process_files over the attachments of Deps.db's
// repositories and Deps.providers, with the bucket its caller names, on a
// connection of the pool. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenFileProcessing -update
func TestWriteRustAPIGoldenFileProcessing(t *testing.T) {
	output := rowFeaturesOutputWith(t, false, withFilesSlot)
	if !output.HasFileProcessing() {
		t.Fatal("the crate holds no file processor's pass")
	}
	generated := writeGoldenAPI(t, "fixture-references-files-pass-api", output)
	for file, wants := range map[string][]string{
		"src/files.rs": {"pub async fn process_pending_files(", "database_types::orm::postgres::Repositories::new(&client)?", "process_files(&attachments, &options).await"},
		"src/lib.rs":   {"pub mod files;"},
		"src/deps.rs":  {"pub providers: Arc<Providers>,"},
	} {
		for _, want := range wants {
			if !strings.Contains(generated[file], want) {
				t.Errorf("%s holds no %q", file, want)
			}
		}
	}
}

// TestFileProcessingOnSQLite: on a database that lists sqlite first the
// pass runs in a transaction of the file's writing connection; without a
// files slot, or over a database with no @attachments, the crate holds no
// pass; and a pass over a database with no Rust types is refused.
func TestFileProcessingOnSQLite(t *testing.T) {
	output := rowFeaturesOutputWith(t, true, withFilesSlot)
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "src", FilesFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".write_async(move |connection| {", "database_types::orm::sqlite::Repositories::new(connection)?", "wait(process_files(&attachments, &options))"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("src/files.rs holds no %q", want)
		}
	}
	if rowFeaturesOutput(t, false).HasFileProcessing() {
		t.Error("an API with no files slot holds the pass")
	}
	noAttachments := rowFeaturesOutputWith(t, false, func(api *ir.Schema, deps *DepsInfo) {
		withFilesSlot(api, deps)
		deps.FileProcessing = false
	})
	if noAttachments.HasFileProcessing() {
		t.Error("an API over a database with no @attachments holds the pass")
	}
	_, db, _ := rowFeaturesFixture.load(t)
	unbuilt := rowFeaturesFixture.generate(t, db)
	unbuilt.Providers = &ProvidersInfo{Slots: []ProviderSlotInfo{{Slot: "PREVIEWS", Interface: "files"}}}
	unbuilt.Endpoints = nil
	err = unbuilt.SetDeps(DepsInfo{Database: rowFeaturesFixture.db, FileProcessing: true})
	const want = "rustrestgen: fixture-references-api runs the file processor's pass over the attachments of fixture-references-db (#614), whose repositories are in its Rust types, and fixture-references-db generates none; enable outputs.types.rust in fixture-references-db's config"
	if err == nil || err.Error() != want {
		t.Errorf("SetDeps = %v,\nwant %s", err, want)
	}
}
