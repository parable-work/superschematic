package typegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
)

const scalarRulesSchemaJSON = `{
  "scalars": {
    "Network.Url": { "name": "Network.Url", "languagePrimitive": "string" }
  },
  "types": {
    "Site": {
      "name": "Site",
      "role": "EmbeddedStruct",
      "fields": [
        {
          "name": "home",
          "typeRef": { "name": "Network.Url" },
          "validateMaxLength": 100,
          "validatePattern": "^https://"
        },
        {
          "name": "mirrors",
          "typeRef": { "name": "Network.Url", "isArray": true },
          "required": true,
          "validateMaxLength": 100
        }
      ]
    }
  }
}`

// TestScalarFieldRulesLeftToTheScalar pins that Validate checks a scalar
// field's own constraints but not a copy of the scalar's: the scalar's
// Validate already checks its pattern and lengths, and a second copy
// reported a malformed value twice.
func TestScalarFieldRulesLeftToTheScalar(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(`{"name": "scalar-rules", "kind": "General", "outputs": {"types": {"go": {"enabled": true}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "site.schema.json"), []byte(scalarRulesSchemaJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatal(err)
	}
	output, err := Generate(schema, Options{
		SchemaName: "scalar-rules",
		ModulePath: "example.com/schemas/types/go/scalar-rules",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "types.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		`t.Home.Validate()`,
		`item.ValidateRequired()`,
		`errors.AddFieldError("home", "maxLength", "must be at most 100 characters")`,
		`regexp.MatchString("^https://", string(value))`,
		`errors.AddFieldError(fieldKey, "maxLength", "must be at most 100 characters")`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("types.go lacks %s", want)
		}
	}
	// Network.Url's own maxLength (2048) and pattern stay in the scalar.
	for _, unwanted := range []string{"must be at most 2048 characters", "^https?://"} {
		if strings.Contains(source, unwanted) {
			t.Errorf("types.go repeats the scalar's rule %q", unwanted)
		}
	}
}
