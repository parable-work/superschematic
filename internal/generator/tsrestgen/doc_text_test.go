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
// " * ", with each "*/" escaped, and the package still type-checks. A
// description whose lines end in CRLF is written the same, with no CR.
func TestDocTextCannotCloseTheImplementationComment(t *testing.T) {
	const comment = "Replace the outline src/*/outline names.\n\nThe last line closes the comment. */"
	const want = "  /**\n" +
		"   * Replace the outline src/*\\/outline names.\n" +
		"   *\n" +
		"   * The last line closes the comment. *\\/\n" +
		"   *\n" +
		"   * PUT /api/drawings/{id}/outline\n"
	for _, tc := range []struct {
		name      string
		comment   string
		typeCheck bool
	}{
		{name: "LF", comment: comment, typeCheck: true},
		{name: "CRLF", comment: strings.ReplaceAll(comment, "\n", "\r\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := loadObjectListsAPI(t)
			for _, set := range schema.OperationSets {
				for _, op := range set.Operations {
					op.Comment = tc.comment
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
			if !strings.Contains(string(source), want) {
				t.Errorf("interfaces.ts does not hold\n%s\nin\n%s", want, source)
			}
			if strings.Contains(string(source), "\r") {
				t.Errorf("interfaces.ts holds a CR:\n%q", source)
			}
			if tc.typeCheck {
				materializeAPI(t, fixture).typeCheck(t)
			}
		})
	}
}
