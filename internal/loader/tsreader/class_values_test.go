package tsreader

import (
	"path/filepath"
	"strings"
	"testing"
)

// A class is a value only in a decorator argument: schema.config.ts names
// services with sentinels, and a class there fails the read at the class.
func TestClassIsNotAConfigValue(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	configPackage := filepath.ToSlash(filepath.Join(root, "packages", "schema-config", "src", "index.ts"))
	dir := decoratorTestService(t, "General", map[string]string{
		"tsconfig.json": `{
  "compilerOptions": {
    "target": "ES2022", "module": "ESNext", "moduleResolution": "Bundler", "lib": ["ES2022"],
    "strict": true, "strictPropertyInitialization": false, "experimentalDecorators": true,
    "skipLibCheck": true, "noEmit": true,
    "paths": { "@superschematic/schema-config": ["` + configPackage + `"] }
  },
  "include": ["schema.config.ts", "src/**/*.ts"]
}`,
		"schema.config.ts": `import { defineConfig, SchemaKind } from "@superschematic/schema-config";

abstract class Helper {}

export default defineConfig({
  name: "decorator-test",
  kind: SchemaKind.General,
  outputs: { types: Helper as never }
});
`,
		"src/item.schema.ts": "export abstract class Item {\n  name: string;\n}\n",
	})
	_, _, err = LoadService(dir)
	if err == nil {
		t.Fatal("a config naming a class loaded")
	}
	if want := "schema.config.ts:8:21: a class is not a schema.config value"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v\nwant it to contain %q", err, want)
	}
}
