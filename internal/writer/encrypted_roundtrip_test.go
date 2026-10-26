package writer

import (
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestEncryptedFieldsRoundTrip: fixture-vault-db's Encrypted<T> fields
// (D85) are written to TypeScript, JSON and YAML and read back to the same
// IR: TypeScript writes Encrypted<string>, which implies Secret<T>, and
// the data forms their encryptedAtRest.
func TestEncryptedFieldsRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-vault-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	want := normalizeIR(t, schema)
	for _, target := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(target), func(t *testing.T) {
			written := schema
			if target == FormatTS {
				written = writableAsTS(t, schema)
			}
			if got := normalizeIR(t, writeAndReload(t, written, target)); got != want {
				t.Errorf("IR mismatch after JSON -> %s round trip\nwant:\n%s\ngot:\n%s", target, want, got)
			}
		})
	}
}
