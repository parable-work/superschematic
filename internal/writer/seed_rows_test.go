package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestSeedRowsRoundTrip: a table's @seedRows (#625) is written to each
// format and read back to the same rows and key, and the TypeScript form
// is the decorator on the class, each row's keys sorted, with its key when
// the schema names one.
func TestSeedRowsRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-seed-rows-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	want := normalizeIR(t, schema)
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		if got := normalizeIR(t, writeAndReload(t, schema, format)); got != want {
			t.Errorf("IR mismatch after TS -> %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
		}
	}
	dir := t.TempDir()
	written, err := WriteService(schema, FormatTS, dir)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, path := range written {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		text += string(data)
	}
	for _, line := range []string{
		`@seedRows<Region>([{ code: "eu", name: "Europe" }, { code: "us", name: "United States" }])`,
		`@seedRows<Reminder>([{ kind: "*", leadMinutes: 30, member: null, quietStart: "22:00" }, { kind: "deadline", leadMinutes: 120, member: null }], { key: ["member", "kind"] })`,
		`limits: { exports: true, storageGb: 50 }`,
	} {
		if !strings.Contains(text, line) {
			t.Errorf("written TypeScript lacks %q:\n%s", line, text)
		}
	}
}
