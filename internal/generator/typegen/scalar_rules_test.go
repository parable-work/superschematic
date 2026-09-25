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

// TestScalarFieldRulesCheckedOnce pins how Validate checks a scalar field:
// the scalar's own lengths and pattern live once, in validateNetworkUrlValue,
// which reports a failure by the rule's name and takes the scalar core's
// verdict only for a value they accept; the field's own constraints stay
// inline. Before, the scalar's Validate and an inline copy of its rules both
// reported a failing value.
func TestScalarFieldRulesCheckedOnce(t *testing.T) {
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
		`func validateNetworkUrlValue(value NetworkUrl, required bool) (bool, []ValidationError) {`,
		`validateNetworkUrlValue(*t.Home, false)`,
		`validateNetworkUrlValue(item, true)`,
		`errors.AddFieldError("home", "maxLength", "must be at most 100 characters")`,
		`regexp.MatchString("^https://", string(value))`,
		`errors.AddFieldError(fieldKey, "maxLength", "must be at most 100 characters")`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("types.go lacks %s", want)
		}
	}
	// Network.Url's own maxLength (2048) and pattern appear once, in
	// validateNetworkUrlValue, not next to each field.
	for _, rule := range []string{`Validator: "maxLength", Message: "must be at most 2048 characters"`, `regexp.MatchString("^https?://`} {
		if got := strings.Count(source, rule); got != 1 {
			t.Errorf("types.go has %d copies of the scalar's rule %q, want 1", got, rule)
		}
	}
	for _, unwanted := range []string{`t.Home.Validate()`, `item.ValidateRequired()`} {
		if strings.Contains(source, unwanted) {
			t.Errorf("types.go calls %s instead of validateNetworkUrlValue", unwanted)
		}
	}
}
