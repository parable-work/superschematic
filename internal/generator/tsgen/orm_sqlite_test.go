package tsgen

import (
	"strings"
	"testing"
)

// tableDefinitionBlock is the block of a template that writes a table's
// definition: from its doc comment to the line that closes the object.
func tableDefinitionBlock(t *testing.T, name string) string {
	t.Helper()
	source, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(source), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "/** The {{.Table}} table") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s writes no table definition", name)
	}
	for end := start; end < len(lines); end++ {
		if lines[end] == "};" {
			return strings.Join(lines[start:end+1], "\n")
		}
	}
	t.Fatalf("%s's table definition does not end", name)
	return ""
}

// TestTheSQLiteDefinitionOfATableIsPostgres: orm-sqlite.ts writes a table's
// definition as orm.ts does, the same members from the same fields, but for
// its doc comment and its audience's condition in SQLite's words (D182), so
// a member a later change adds to one is added to the other.
func TestTheSQLiteDefinitionOfATableIsPostgres(t *testing.T) {
	postgres := tableDefinitionBlock(t, "orm.tmpl")
	sqlite := tableDefinitionBlock(t, "orm_sqlite.tmpl")
	sqlite = strings.Replace(sqlite, "/** The {{.Table}} table on SQLite, as", "/** The {{.Table}} table, as", 1)
	sqlite = strings.Replace(sqlite, ", contains: {{jsonString .AudienceContains}}, placeholder: {{jsonString .AudiencePlaceholder}} }", " }", 1)
	if postgres != sqlite {
		t.Errorf("orm_sqlite.tmpl's table definition is not orm.tmpl's:\n%s\n----\n%s", sqlite, postgres)
	}
}
