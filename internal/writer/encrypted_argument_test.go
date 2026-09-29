package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// encryptedArgumentFixture is apigen's TypeScript schema with an
// EncryptedField<string> argument outside an Encrypted operation set.
const encryptedArgumentFixture = "../generator/apigen/testdata/services/encrypted-argument-api"

// TestEncryptedArgumentsRoundTrip: an argument's EncryptedField<T> survives
// every format. The TypeScript writer spells it EncryptedField<string>, and
// the TypeScript, JSON and YAML forms each load back to the same IR.
func TestEncryptedArgumentsRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(encryptedArgumentFixture)
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	if !schema.OperationSets[0].Operations[0].Arguments[1].Encrypted {
		t.Fatal("precondition: storeCard's number argument is not encrypted")
	}
	want := normalizeIR(t, schema)
	for _, target := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(target), func(t *testing.T) {
			if got := normalizeIR(t, writeAndReload(t, schema, target)); got != want {
				t.Errorf("IR mismatch after TS -> %s round trip\nwant:\n%s\ngot:\n%s", target, want, got)
			}
		})
	}

	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(dir, "src", "cards.schema.ts"))
	if err != nil {
		t.Fatalf("reading TS output: %v", err)
	}
	for _, want := range []string{
		"storeCard(customerId: string, number: EncryptedField<string>, label: Nullable<string>): CardReceipt",
		"renameCard(id: string, label: string): CardReceipt",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("TS output missing %q:\n%s", want, source)
		}
	}
}
