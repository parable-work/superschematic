package tsreader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// configService writes a service beside the tsreader fixtures, whose
// tsconfig base maps @schemas/<dir> to each fixture's src/index.ts, with the
// given schema.config.ts. src/helpers.ts holds a type, a function and a
// const that is not a sentinel, for a config to import from a module of its
// own.
func configService(t *testing.T, config string) string {
	t.Helper()
	dir, err := os.MkdirTemp(filepath.Join("testdata", "services"), "fixture-config-imports-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for rel, content := range map[string]string{
		"tsconfig.json":       `{ "extends": "../../tsconfig.base.json", "include": ["schema.config.ts", "src/**/*.ts"] }`,
		"package.json":        `{ "name": "@schemas/` + filepath.Base(dir) + `", "private": true }`,
		"schema.config.ts":    config,
		"src/probe.schema.ts": "export enum Probe {\n  Ok = \"ok\"\n}\n",
		"src/helpers.ts":      "export type Shape = { name: string };\nexport function helper(): string {\n  return \"probe\";\n}\nexport const notASentinel = 1;\n",
	} {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const configTail = `export default defineConfig({ name: "probe", kind: SchemaKind.API, authDb: FixtureDb, outputs: {} });
`

// TestConfigImportRule: a schema.config.ts imports the config package, under
// any binding form, and other services' sentinels by name as values; each
// other form is refused at the import with a message that says what it is
// (D34).
func TestConfigImportRule(t *testing.T) {
	for _, tc := range []struct {
		name    string
		imports string
		want    string
	}{
		{"a sentinel", `import { FixtureDb } from "@schemas/fixture-db";`, ""},
		{"a renamed sentinel", `import { FixtureDb as Db } from "@schemas/fixture-db";
const FixtureDb = Db;`, ""},
		{"the config package as a namespace", `import * as config from "@superschematic/schema-config";
import { FixtureDb } from "@schemas/fixture-db";
const _unused = config.SchemaKind.API;`, ""},
		{"a type of the config package", `import { type ServiceHandle } from "@superschematic/schema-config";
import { FixtureDb } from "@schemas/fixture-db";
const _db: ServiceHandle = FixtureDb;`, ""},
		{"a schema class", `import { FixtureDb, Tenant } from "@schemas/fixture-db";`,
			`schema.config.ts imports Tenant from "@schemas/fixture-db", which is a class, not a service sentinel; a config imports only @superschematic/schema-config and other services' sentinels (D34)`},
		{"an enum", `import { FixtureDb, TenantStatus } from "@schemas/fixture-db";`,
			`imports TenantStatus from "@schemas/fixture-db", which is an enum, not a service sentinel`},
		{"a type", `import { FixtureDb } from "@schemas/fixture-db";
import { type Shape } from "./src/helpers";`,
			`imports Shape from "./src/helpers" as a type only; a config names a sentinel as a value`},
		{"a type alias imported as a value", `import { FixtureDb } from "@schemas/fixture-db";
import { Shape } from "./src/helpers";`,
			`imports Shape from "./src/helpers", which is a type, not a service sentinel`},
		{"a sentinel as a type only", `import type { FixtureDb as Handle } from "@schemas/fixture-db";
import { FixtureDb } from "@schemas/fixture-db";`,
			`imports Handle from "@schemas/fixture-db" as a type only`},
		{"a function", `import { FixtureDb } from "@schemas/fixture-db";
import { helper } from "./src/helpers";`,
			`imports helper from "./src/helpers", which is a function, not a service sentinel`},
		{"a const that is not a sentinel", `import { FixtureDb } from "@schemas/fixture-db";
import { notASentinel } from "./src/helpers";`,
			`imports notASentinel from "./src/helpers", which is a value, not a service sentinel`},
		{"a default import", `import FixtureDb from "@schemas/fixture-db";`,
			`schema.config.ts imports FixtureDb from "@schemas/fixture-db" as a default import; a sentinel is a named export`},
		{"a sibling as a namespace", `import * as db from "@schemas/fixture-db";
const FixtureDb = db.FixtureDb;`,
			`imports db from "@schemas/fixture-db", which is a namespace of another module, not a service sentinel`},
		{"a side effect", `import "@schemas/fixture-db";
import { FixtureDb } from "@schemas/fixture-db";`,
			`schema.config.ts may not import "@schemas/fixture-db" for its side effects; a config imports only`},
		{"a name that does not resolve", `import { FixtureDb, Missing } from "@schemas/fixture-db";`,
			`imports Missing from "@schemas/fixture-db", which does not resolve`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := configService(t, `import { defineConfig, SchemaKind } from "@superschematic/schema-config";
`+tc.imports+"\n"+configTail)
			cfg, err := ReadServiceConfig(dir, nil)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("ReadServiceConfig: %v", err)
				}
				if cfg.AuthDB != "fixture-db" {
					t.Errorf("authDb = %q, want fixture-db", cfg.AuthDB)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ReadServiceConfig error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestReadServiceIdentityReadsOnlyNameAndKind: the sweep's read needs no
// sentinel, so it reads a config whose handles do not resolve yet, where the
// full read cannot.
func TestReadServiceIdentityReadsOnlyNameAndKind(t *testing.T) {
	dir := configService(t, `import { defineConfig, SchemaKind } from "@superschematic/schema-config";
import { NotBuiltYet } from "@schemas/not-built-yet";
export default defineConfig({ name: "probe", kind: SchemaKind.API, calls: [NotBuiltYet], outputs: {} });
`)
	if _, err := ReadServiceConfig(dir, nil); err == nil || !strings.Contains(err.Error(), "does not resolve") {
		t.Fatalf("ReadServiceConfig error = %v, want the import to not resolve", err)
	}
	cfg, err := ReadServiceIdentity(dir, nil)
	if err != nil {
		t.Fatalf("ReadServiceIdentity: %v", err)
	}
	if cfg.Name != "probe" || cfg.Kind != ir.SchemaKindAPI || cfg.Calls != nil {
		t.Errorf("identity = %+v, want name probe, kind API and nothing else", cfg)
	}
}
