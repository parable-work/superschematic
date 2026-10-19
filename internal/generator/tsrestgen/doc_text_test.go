package tsrestgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocTextCannotCloseTheImplementationComment: an operation description
// of several lines, with "*/" in its text and at its end, is written into
// its implementation method's JSDoc a line at a time behind the comment's
// " * ", with each "*/" escaped, and the package still type-checks.
func TestDocTextCannotCloseTheImplementationComment(t *testing.T) {
	schema := loadObjectListsAPI(t)
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			op.Comment = "Replace the outline src/*/outline names.\n\nThe last line closes the comment. */"
		}
	}
	fixture := objectListsFixture(t, schema)
	dir := t.TempDir()
	if err := WriteAPI(generateFixture(t, fixture), dir); err != nil {
		t.Fatalf("WriteAPI: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(dir, "interfaces.ts"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "  /**\n" +
		"   * Replace the outline src/*\\/outline names.\n" +
		"   *\n" +
		"   * The last line closes the comment. *\\/\n" +
		"   *\n" +
		"   * PUT /api/drawings/{id}/outline\n"
	if !strings.Contains(string(source), want) {
		t.Errorf("interfaces.ts does not hold\n%s\nin\n%s", want, source)
	}

	materializeAPI(t, fixture).typeCheck(t)
}
