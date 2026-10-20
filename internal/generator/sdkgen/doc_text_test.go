package sdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// closingDoc is a description of several lines, with "*/" in its text and
// at its end, which must neither close a method's JSDoc early nor lose the
// comment's asterisks.
const closingDoc = "Every item, as src/*/items would glob them.\n\nThe last line closes the comment. */"

// withClosingDocs gives every operation of the schema closingDoc as its
// description.
func withClosingDocs(schema *ir.Schema) error {
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			op.Comment = closingDoc
		}
	}
	return nil
}

// TestDocTextCannotCloseTheMethodComment: every line of a method's
// description is written into its JSDoc behind the comment's " * ", and a
// "*/" in it is escaped, on each of fixture-paged-api's four methods.
func TestDocTextCannotCloseTheMethodComment(t *testing.T) {
	_, apiOutput, parseable := loadPagedAPI(t, withClosingDocs)
	outDir := t.TempDir()
	writePagedSDK(t, apiOutput, parseable, outDir)
	source, err := os.ReadFile(filepath.Join(outDir, "namespaces", "catalog.ts"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "  /**\n" +
		"   * Every item, as src/*\\/items would glob them.\n" +
		"   *\n" +
		"   * The last line closes the comment. *\\/\n"
	if got := strings.Count(string(source), want); got != 4 {
		t.Errorf("the description is written %d times as\n%s\nwant 4, one per method, in\n%s", got, want, source)
	}
}

// TestDocTextSDKCompiles type-checks the TypeScript SDK of
// fixture-paged-api, with closingDoc as every operation's description,
// against its generated types package: unescaped, the "*/" ends the JSDoc
// and tsc reads the rest of the description as code.
func TestDocTextSDKCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}

	schema, apiOutput, parseable := loadPagedAPI(t, withClosingDocs)
	typesDir := writeTypesPackage(t, bunPath, schema, pagedService, tempRoot)
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", pagedService)
	writePagedSDK(t, apiOutput, parseable, sdkDir)
	if err := CompileSDK(sdkDir, typesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}
}
