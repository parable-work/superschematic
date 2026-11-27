package tsreader

import (
	"os"
	"path/filepath"
	"reflect"
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

// TestReadSiteDevConfig: a Site config's site.dev names its dev server's
// command and port (D82); each is optional, and a member it does not take,
// a command that is not a string or a port that is not a whole number is
// refused by the JSON Schema and decoder the data forms use.
func TestReadSiteDevConfig(t *testing.T) {
	for _, tc := range []struct {
		site string
		want *ir.SiteDevConfig
		err  string
	}{
		{`{ dev: { command: "vite", port: 5173 } }`, &ir.SiteDevConfig{Command: "vite", Port: 5173}, ""},
		{`{ build: "build", dev: {} }`, &ir.SiteDevConfig{}, ""},
		{`{ build: "build" }`, nil, ""},
		{`{ dev: { script: "dev" } }`, nil, `at '/site/dev': additional properties 'script' not allowed`},
		{`{ dev: { command: 1 } }`, nil, `at '/site/dev/command': got number, want string`},
		{`{ dev: { port: 51.5 } }`, nil, "cannot unmarshal number 51.5 into Go struct field SchemaConfig.site.dev.port of type int"},
		{`{ dev: "vite" }`, nil, `at '/site/dev': got string, want object`},
		{`{ dev: { command: "vite --port 3000" } }`, nil, `site.dev.command "vite --port 3000" is no script name`},
	} {
		t.Run(tc.site, func(t *testing.T) {
			dir := configService(t, `import { defineConfig, SchemaKind } from "@superschematic/schema-config";
export default defineConfig({ name: "probe-web", kind: SchemaKind.Site, site: `+tc.site+` });
`)
			cfg, err := ReadServiceConfig(dir, nil)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("ReadServiceConfig error = %v, want it to contain %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadServiceConfig: %v", err)
			}
			if got := cfg.Site.Dev; (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("site.dev = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestReadClientVersions: an API config's clientVersions (D143) is read
// from the TypeScript form, true alone turning it on.
func TestReadClientVersions(t *testing.T) {
	for value, want := range map[string]bool{"true": true, "false": false} {
		dir := configService(t, `import { defineConfig, SchemaKind } from "@superschematic/schema-config";
export default defineConfig({ name: "probe", kind: SchemaKind.API, clientVersions: `+value+`, outputs: {} });
`)
		cfg, err := ReadServiceConfig(dir, nil)
		if err != nil {
			t.Fatalf("ReadServiceConfig: %v", err)
		}
		if cfg.ClientVersions != want || cfg.NewSchema().ClientVersions != want {
			t.Errorf("clientVersions: %s reads as %t", value, cfg.ClientVersions)
		}
	}
	dir := configService(t, `import { defineConfig, SchemaKind } from "@superschematic/schema-config";
export default defineConfig({ name: "probe", kind: SchemaKind.API, outputs: {} });
`)
	if cfg, err := ReadServiceConfig(dir, nil); err != nil || cfg.ClientVersions {
		t.Errorf("a config without clientVersions: %+v, %v", cfg, err)
	}
}

// TestReadContentAddressedBucket: a Bucket config's contentAddressed and
// maxBytes (D142) are read from the TypeScript form as from the data forms:
// a maxBytes of 0 is the default, as when absent, and a fraction or a
// negative number is refused.
func TestReadContentAddressedBucket(t *testing.T) {
	config := func(members string) string {
		return `import { defineConfig, SchemaKind } from "@superschematic/schema-config";
export default defineConfig({ name: "probe", kind: SchemaKind.Bucket, ` + members + ` outputs: {} });
`
	}
	cfg, err := ReadServiceConfig(configService(t, config("contentAddressed: true, maxBytes: 1048576,")), nil)
	if err != nil {
		t.Fatalf("ReadServiceConfig: %v", err)
	}
	if !cfg.ContentAddressed || cfg.MaxBytes != 1<<20 {
		t.Errorf("config = %+v", cfg)
	}
	if cfg, err := ReadServiceConfig(configService(t, config("")), nil); err != nil || cfg.ContentAddressed || cfg.MaxBytes != 0 {
		t.Errorf("a plain bucket: %+v, %v", cfg, err)
	}
	if cfg, err := ReadServiceConfig(configService(t, config("contentAddressed: true, maxBytes: 0,")), nil); err != nil || cfg.ContentMaxBytes() != ir.DefaultContentMaxBytes {
		t.Errorf("maxBytes 0: %+v, %v", cfg, err)
	}
	for bad, want := range map[string]string{
		"1.5": "cannot unmarshal number 1.5 into Go struct field SchemaConfig.maxBytes of type int64",
		"-4":  "sets maxBytes to -4; it is a whole number of bytes above 0",
	} {
		if _, err := ReadServiceConfig(configService(t, config("contentAddressed: true, maxBytes: "+bad+",")), nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("maxBytes %s: %v, want it to contain %q", bad, err, want)
		}
	}
}

// TestReadConfigThroughTheJSONSchema: the TypeScript form is decoded as the
// data forms are, so a mistyped or unknown key is refused, not dropped, an
// API without outputs is refused, and each handle key takes handles alone.
// The authDb handle's kind, which the data forms do not write, is kept and
// checked.
func TestReadConfigThroughTheJSONSchema(t *testing.T) {
	for _, tc := range []struct {
		name, members, want string
	}{
		{"a mistyped key", `public: "yes", outputs: {}`, `at '/public': got string, want boolean`},
		{"an unknown key", `pubic: true, outputs: {}`, `additional properties 'pubic' not allowed`},
		{"a mistyped outputs key", `outputs: { api: { enabled: "yes" } }`, `at '/outputs/api/enabled': got string, want boolean`},
		{"an API without outputs", ``, `defineConfig: schema config for probe has no outputs; only a Site service may leave them out`},
		{"authDb as a name", `authDb: "fixture-db", outputs: {}`, `defineConfig authDb must be a service({...}) sentinel, not a string`},
		{"dependencies of plain objects", `dependencies: [{ name: "fixture-db", kind: "DB" }], outputs: {}`, `defineConfig dependencies entries must be service({...}) sentinels, not an object`},
		{"calls that is no list", `calls: service({ name: "other-api", kind: "API" }), outputs: {}`, `defineConfig calls must be a list of service({...}) sentinels, not a service sentinel`},
		{"an authDb of an unknown kind", `authDb: service({ name: "other-db", kind: "Nope" }), outputs: {}`, `has an authDb of unknown kind "Nope"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := configService(t, `import { defineConfig, service, SchemaKind } from "@superschematic/schema-config";
export default defineConfig({ name: "probe", kind: SchemaKind.API, `+tc.members+` });
`)
			_, err := ReadServiceConfig(dir, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "schema.config.ts:2:1: ") {
				t.Fatalf("ReadServiceConfig error = %v, want it at schema.config.ts:2:1 and to contain %q", err, tc.want)
			}
		})
	}

	dir := configService(t, `import { defineConfig, service, SchemaKind } from "@superschematic/schema-config";
import { FixtureDb } from "@schemas/fixture-db";
export default defineConfig({
  name: "probe",
  kind: SchemaKind.API,
  authDb: FixtureDb,
  dependencies: [FixtureDb],
  calls: [service({ name: "other-api", kind: SchemaKind.API })],
  outputs: { api: { enabled: true } },
});
`)
	cfg, err := ReadServiceConfig(dir, nil)
	if err != nil {
		t.Fatalf("ReadServiceConfig: %v", err)
	}
	want := []ServiceDependency{{Name: "fixture-db", Kind: ir.SchemaKindDB}}
	if cfg.AuthDB != "fixture-db" || cfg.AuthDBKind != ir.SchemaKindDB || !reflect.DeepEqual(cfg.Dependencies, want) ||
		len(cfg.Calls) != 1 || cfg.Calls[0].Name != "other-api" || !reflect.DeepEqual(cfg.Outputs, map[string]any{"api": map[string]any{"enabled": true}}) {
		t.Errorf("config = %+v", cfg)
	}
}
