package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/tsreader"
	"github.com/parable-work/superschematic/internal/testpaths"
	"github.com/parable-work/superschematic/internal/writer"
	"github.com/parable-work/superschematic/internal/writer/jsonwriter"
)

// corpus is the loader's fixture services the writer round-trip covers, in
// TypeScript, JSON and YAML.
var corpus = []string{
	"internal/loader/tsreader/testdata/services/fixture-db",
	"internal/loader/tsreader/testdata/services/fixture-relation-ondelete",
	"internal/loader/tsreader/testdata/services/fixture-api",
	"internal/loader/tsreader/testdata/services/fixture-general",
	"internal/loader/tsreader/testdata/services/fixture-deny-unknown-fields",
	"internal/loader/tsreader/testdata/services/fixture-strict-json",
	"internal/loader/tsreader/testdata/services/fixture-docs",
	"internal/loader/tsreader/testdata/services/fixture-mcp",
	"internal/loader/tsreader/testdata/services/fixture-projection",
	"internal/loader/tsreader/testdata/services/fixture-nested-arrays",
	"internal/loader/tsreader/testdata/services/fixture-nested-arrays-db",
	"internal/loader/tsreader/testdata/services/fixture-nested-arrays-api",
	"internal/loader/tsreader/testdata/services/fixture-service-auth-api",
	"internal/loader/testdata/services/fixture-db-json",
	"internal/loader/testdata/services/fixture-db-yaml",
	"internal/loader/testdata/services/fixture-general-json",
	"internal/loader/testdata/services/fixture-general-yaml",
	"internal/loader/testdata/services/fixture-comments-yaml",
}

// openParts are documents the types accept because a registry, not the
// core, closes what they use: a kind, extension data, a document, a
// renamed invocation policy key and behaviors with their configs.
const openParts = `
export const extensionKind: Document = { name: 'jobs', kind: 'Worker' };
export const extensionData: Document = {
  extensions: { acme: { catalog: true } },
  documents: { catalogConfig: { shelves: 3 } },
  types: {
    Item: {
      name: 'Item',
      role: 'DBTable',
      extensions: { acme: { tagged: true } },
      fields: [{ name: 'slug', typeRef: { name: 'Identity.Slug' }, extensions: { acme: { shelf: { aisle: 3 } } } }],
    },
  },
};
export const behaviors: TypeDef = {
  name: 'Item',
  role: 'DBTable',
  behaviors: [{ name: 'acme.Stock', config: { aisles: 3, unit: 'box' } }, { name: 'acme.Audited' }],
};
export const renamedPolicy: OperationSetFile = {
  kind: 'OperationSet',
  name: 'OrderOperations',
  operations: [{ name: 'deleteOrder', typeRef: { name: 'Order' }, mcp: { handle: 'delete_order', hidden: false, review: 'always' } }],
};
`

// closedParts are documents the types refuse: an unknown key, a value
// outside an enum, a missing required key and a single-definition file
// without its discriminator.
const closedParts = `
// @ts-expect-error an unknown key
export const unknownKey: TypeDef = { name: 'Item', role: 'DBTable', colour: 'red' };
// @ts-expect-error a role outside the enum
export const unknownRole: TypeDef = { name: 'Item', role: 'Table' };
// @ts-expect-error a field without its typeRef
export const missingTypeRef: FieldDef = { name: 'slug' };
// @ts-expect-error an enum file without its kind
export const enumWithoutKind: EnumFile = { name: 'Colour', values: [] };
// @ts-expect-error a string where the mcp record takes a boolean
export const hiddenAsString: OperationMCP = { hidden: 'no' };
// @ts-expect-error a behavior without its name
export const behaviorWithoutName: BehaviorRef = { config: { aisles: 3 } };
// @ts-expect-error an unknown key on a behavior
export const behaviorExtraKey: TypeDef = { name: 'Item', role: 'DBTable', behaviors: [{ name: 'acme.Audited', extra: 1 }] };
`

// TestTypesCheckTheCorpus type-checks the generated types with the
// compiler the schema loader uses. The package is mounted as a consumer
// installs it, with its package.json, and imported through the exports
// map. Every data-form file the JSON writer produces for the fixture
// corpus is assigned to the type of its form, every document the Go reader
// decodes in the schema-file parity corpus to Document, the open parts
// accept what a registry may add, and the closed parts refuse what no
// registry accepts.
func TestTypesCheckTheCorpus(t *testing.T) {
	root := testpaths.RepoRoot(t)
	outputs, err := render()
	if err != nil {
		t.Fatal(err)
	}
	pkg := "node_modules/@superschematic/schema-ir/"
	files := map[string]string{
		pkg + "schema-file.d.ts":  string(outputs[typesPath]),
		pkg + "schema-file.json":  string(outputs[schemaPath]),
		pkg + "package.json":      readFile(t, filepath.Join(root, "ir", "typescript", "package.json")),
		pkg + "index.d.ts":        readFile(t, filepath.Join(root, "ir", "typescript", "index.d.ts")),
		"root-exports-resolve.ts": "import type { Schema } from '@superschematic/schema-ir';\nexport type Root = Schema;\n",
	}

	var check strings.Builder
	check.WriteString("import type { BehaviorRef, Document, EnumFile, FieldDef, OperationMCP, OperationSetFile, ScalarFile, TypeDef, UnionFile } from '@superschematic/schema-ir/schema-file';\n")
	count := 0
	for _, dir := range corpus {
		schema, err := loader.LoadService(filepath.Join(root, dir))
		if err != nil {
			t.Fatalf("loading %s: %v", dir, err)
		}
		docs := writer.SplitSchema(schema)
		for _, file := range sortedKeys(docs) {
			data, err := jsonwriter.Write(docs[file])
			if err != nil {
				t.Fatalf("%s %s: %v", dir, file, err)
			}
			form, err := formOf(data)
			if err != nil {
				t.Fatalf("%s %s: %v", dir, file, err)
			}
			fmt.Fprintf(&check, "// %s %s\nexport const doc%d: %s = %s;\n", dir, file, count, form, data)
			count++
		}
	}
	if count == 0 {
		t.Fatal("the corpus wrote no documents")
	}
	// Every document the Go reader accepts in the schema-file parity
	// corpus, core and extended registry alike, is a Document as the
	// reader decodes it.
	var parity struct {
		Vectors []struct {
			Name      string `json:"name"`
			Accept    bool   `json:"accept"`
			Canonical string `json:"canonical"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(root, "runtime", "schema", "testdata", "schema_file_parity.json"))), &parity); err != nil {
		t.Fatal(err)
	}
	for _, vector := range parity.Vectors {
		if vector.Accept {
			fmt.Fprintf(&check, "// parity: %s\nexport const doc%d: Document = %s;\n", vector.Name, count, vector.Canonical)
			count++
		}
	}
	check.WriteString(openParts)
	check.WriteString(closedParts)
	files["check.ts"] = check.String()

	program, err := tsreader.NewDeclarationProgram(tsreader.DeclarationInput{
		Files: files,
		Roots: []string{"check.ts", "root-exports-resolve.ts"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer program.Close()
	if diags := program.Diagnostics(); len(diags) > 0 {
		t.Fatalf("the generated types do not check %d corpus documents:\n%v", count, diags)
	}
}

// formOf names the type of a data-form file as the readers dispatch it: a
// single-definition kind, a role for a type, or the document.
func formOf(data []byte) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	switch payload["kind"] {
	case "Enum":
		return "EnumFile", nil
	case "Union":
		return "UnionFile", nil
	case "Scalar":
		return "ScalarFile", nil
	case "OperationSet":
		return "OperationSetFile", nil
	}
	if _, ok := payload["role"]; ok {
		return "TypeDef", nil
	}
	return "Document", nil
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
