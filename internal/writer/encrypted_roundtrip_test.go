package writer

import (
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestEncryptedAndNamedFieldsRoundTrip: fixture-vault-db's Encrypted<T>
// fields and fixture-naming-db's @table and @column (D85) are written to
// TypeScript, JSON and YAML and read back to the same IR: TypeScript
// writes Encrypted<string>, which implies Secret<T>, @table and @column,
// and the data forms their encryptedAtRest and sqlName.
func TestEncryptedAndNamedFieldsRoundTrip(t *testing.T) {
	for _, fixture := range []string{"fixture-vault-db", "fixture-naming-db"} {
		t.Run(fixture, func(t *testing.T) {
			schema, err := loader.LoadService(tsFixtures + "/" + fixture)
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
						t.Errorf("IR mismatch after the round trip to %s\nwant:\n%s\ngot:\n%s", target, want, got)
					}
				})
			}
		})
	}
}
