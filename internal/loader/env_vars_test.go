package loader

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestLoadRefusesTwoEnvVarsConfigs: a service's config is the @envVars
// class no other extends (D94), so an API with two @envVars classes,
// neither extending the other, fails the load where the first is written,
// naming both and the fix, in the TypeScript form and in the data forms;
// the documented pair, one class extending the other, loads, and so does a
// chain through a plain class.
func TestLoadRefusesTwoEnvVarsConfigs(t *testing.T) {
	_, err := LoadService(filepath.Join("tsreader", "testdata", "services", "broken-env-vars"))
	want := "src/keys.schema.ts: broken-env-vars has two @envVars classes, FieldKeys (src/keys.schema.ts) and FinancesConfig (src/config.schema.ts), and neither extends the other; a service's config is the one @envVars class no other extends, which holds the fields of every class it extends, so make one of them extend the other, or remove @envVars from one (D94)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("LoadService = %v, want an error containing %q", err, want)
	}

	config := `{"name": "temp-api", "kind": "API", "outputs": {}}`
	class := func(name, extends string, envVars bool) string {
		return `"` + name + `": {"name": "` + name + `", "role": "EmbeddedStruct", "envVars": ` + strconv.FormatBool(envVars) +
			`, "extends": "` + extends + `", "fields": [{"name": "` + strings.ToUpper(name) + `", "typeRef": {"name": "string"}, "required": true}]}`
	}
	for _, tc := range []struct {
		name, types, want string
	}{
		{"two", class("FieldKeys", "", true) + ", " + class("FinancesConfig", "", true), "src/config.schema.json: temp-api has two @envVars classes, FieldKeys (src/config.schema.json) and FinancesConfig (src/config.schema.json), and neither extends the other;"},
		{"the documented pair", class("FieldKeys", "", true) + ", " + class("FinancesConfig", "FieldKeys", true), ""},
		{"through a plain class", class("FieldKeys", "", true) + ", " + class("Secrets", "FieldKeys", false) + ", " + class("FinancesConfig", "Secrets", true), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeService(t, map[string]string{
				"schema.config.json":     config,
				"src/config.schema.json": `{"types": {` + tc.types + `}}`,
			})
			schema, err := LoadService(dir)
			switch {
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("LoadService = %v, want an error containing %q", err, tc.want)
			case tc.want == "" && err != nil:
				t.Fatalf("LoadService: %v", err)
			case tc.want == "":
				if td, err := schema.EnvVarsType(); err != nil || td == nil || td.Name != "FinancesConfig" {
					t.Errorf("EnvVarsType = %v, %v; want FinancesConfig", td, err)
				}
			}
		})
	}
}
