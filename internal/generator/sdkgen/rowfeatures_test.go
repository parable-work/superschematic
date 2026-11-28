package sdkgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// rowFeaturesAPI is the API that serves fixture-references-db's Ticket's
// and Note's references, backlinks and files (#723).
const rowFeaturesAPI = "fixture-references-api"

// rowFeaturesAPIGolden is fixture-references-api's SDK, whose types
// package depends on fixture-references-db's.
func rowFeaturesAPIGolden(t *testing.T) goldenSDK {
	schema, db, apiOutput, parseable := loadBehaviorAPI(t, rowFeaturesAPI, "fixture-references-db")
	return goldenSDK{
		apiOutput: apiOutput,
		parseable: parseable,
		clock:     nestedArraysClock,
		types: []goldenTypes{
			{name: rowFeaturesAPI, schema: schema, deps: map[string]*ir.Schema{"fixture-references-db": db}},
			{name: "fixture-references-db", schema: db},
		},
	}
}

// TestRowFeaturesSDKGolden pins every file of the TypeScript SDK for
// fixture-references-api, its tool documents included: each operation of
// a table's references and files is a method of the table's namespace,
// sdk.ticket.backlinks(id) and sdk.ticket.completeUpload(id, file), and
// the tools are the reads and the add of a reference alone.
// TestEveryGoldenSDKTypeChecks type-checks it. Regenerate with:
// go test ./internal/generator/sdkgen -run TestRowFeaturesSDKGolden -update
func TestRowFeaturesSDKGolden(t *testing.T) {
	_, _, apiOutput, parseable := loadBehaviorAPI(t, rowFeaturesAPI, "fixture-references-db")
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", rowFeaturesAPI))

	source, err := os.ReadFile(filepath.Join(outDir, "namespaces", "ticket.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, each := range []string{
		"public async backlinks(", "`/api/ticket/${encodeURIComponent(id)}/backlinks`",
		"public async completeUpload(", "`/api/ticket/${encodeURIComponent(id)}/files/${encodeURIComponent(file)}/complete`",
		"public async beginUpload(", "RowAttachmentBegun",
	} {
		if !strings.Contains(string(source), each) {
			t.Errorf("namespaces/ticket.ts lacks %q", each)
		}
	}
	// The tools an agent is handed: the reads and the add of a reference,
	// each table's, and none of an upload, a download, a filing or a
	// removal.
	data, err := os.ReadFile(filepath.Join(outDir, "tools", "anthropic.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range doc.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := "note.addReference note.attachments note.backlinks note.references ticket.addReference ticket.attachments ticket.backlinks ticket.references"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("the tools = %s, want %s", got, want)
	}
}
