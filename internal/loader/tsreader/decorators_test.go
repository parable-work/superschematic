package tsreader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// decoratorTestService lays out a temp TypeScript service whose tsconfig maps
// the @psgen/* authoring packages to their sources. kind is the config kind;
// files are paths relative to the service directory.
func decoratorTestService(t *testing.T, kind string, files map[string]string) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	pkg := func(name string) string {
		return filepath.ToSlash(filepath.Join(root, "packages", name, "src", "index.ts"))
	}
	scalars := filepath.ToSlash(filepath.Join(root, "..", "parable-scalars", "typescript", "src", "index.ts"))
	dir := t.TempDir()
	all := map[string]string{
		"package.json":       `{"private": true}`,
		"schema.config.json": `{"name": "decorator-test", "kind": "` + kind + `", "outputs": {}}`,
		"tsconfig.json": `{
  "compilerOptions": {
    "target": "ES2022", "module": "ESNext", "moduleResolution": "Bundler", "lib": ["ES2022"],
    "strict": true, "strictPropertyInitialization": false, "experimentalDecorators": true,
    "emitDecoratorMetadata": false, "skipLibCheck": true, "noEmit": true, "baseUrl": ".",
    "paths": {
      "@psgen/api": ["` + pkg("api") + `"],
      "@psgen/db": ["` + pkg("db") + `"],
      "@psgen/schema": ["` + pkg("schema") + `"],
      "@psgen/scalar-lib": ["` + scalars + `"]
    }
  },
  "include": ["src/**/*.ts"]
}`,
	}
	for rel, contents := range files {
		all[rel] = contents
	}
	for rel, contents := range all {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestDecoratorDiagnosticsArePinned pins the text and location of the
// decorator diagnostics the walker emits for origin, target and kind
// violations and for malformed arguments. The dispatch behind them lives in
// the registry; these strings do not change.
func TestDecoratorDiagnosticsArePinned(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		source string
		want   []string
	}{
		{
			name: "decorator outside the toolchain",
			kind: "DB",
			source: `import { key } from "@psgen/db";
function local(): PropertyDecorator { return () => {}; }
export abstract class Tenant {
  @key
  id: string;
  @local()
  name: string;
}
`,
			want: []string{"a.schema.ts:6:3: decorator @local does not come from a @psgen toolchain package"},
		},
		{
			name: "field decorator on a type",
			kind: "DB",
			source: `import { key, unique } from "@psgen/db";
// @ts-expect-error field decorator on a class
@unique
export abstract class Tenant {
  @key
  id: string;
}
`,
			want: []string{"a.schema.ts:3:1: decorator @unique is not valid on a type declaration"},
		},
		{
			name: "type decorator on a field",
			kind: "DB",
			source: `import { key, index } from "@psgen/db";
export abstract class Tenant {
  @key
  // @ts-expect-error type decorator on a field
  @index(["id"])
  id: string;
}
`,
			want: []string{"a.schema.ts:5:3: decorator @index is not valid on a field"},
		},
		{
			name: "field decorator on an operation set and a type decorator on an operation",
			kind: "API",
			source: `import { HttpMethod, rest, uiHidden } from "@psgen/api";
import { index } from "@psgen/db";
// @ts-expect-error field decorator on a class
@uiHidden
export class Queries {
  @rest(HttpMethod.GET, "things")
  // @ts-expect-error type decorator on a method
  @index(["id"])
  list(): string {
    throw new Error("schema declaration only");
  }
}
`,
			want: []string{
				"a.schema.ts:4:1: decorator @uiHidden is not valid on an operation set",
				"a.schema.ts:8:3: decorator @index is not valid on an operation",
			},
		},
		{
			name: "source outside API and General",
			kind: "DB",
			source: `import { key } from "@psgen/db";
import { source } from "@psgen/schema";
export abstract class Tenant {
  @key
  id: string;
}
@source(Tenant)
export abstract class TenantView {
  id: string;
}
`,
			want: []string{"a.schema.ts:7:1: @source is only allowed in API or General schemas (this service is kind DB)"},
		},
		{
			name: "operation set outside API",
			kind: "General",
			source: `export class Queries {
  list(): string {
    throw new Error("schema declaration only");
  }
}
`,
			want: []string{"a.schema.ts:1:1: operation sets (classes with methods) are only allowed in API schemas (this service is kind General)"},
		},
		{
			name: "index argument errors point at the argument",
			kind: "DB",
			source: `import { key, index } from "@psgen/db";
@index(["id"], { name: "Not Snake" })
// @ts-expect-error unique is not a boolean
@index(["id"], { unique: "yes" })
// @ts-expect-error missing arguments
@index()
export abstract class Tenant {
  @key
  id: string;
}
`,
			want: []string{
				`a.schema.ts:2:16: @index name "Not Snake" must be a lowercase identifier (e.g. digest)`,
				"a.schema.ts:4:16: @index unique must be a boolean literal",
				"a.schema.ts:6:1: @index takes one array argument and an optional unique flag or options object",
			},
		},
		{
			name: "field argument errors",
			kind: "DB",
			source: `import { key } from "@psgen/db";
import { temporalFormat } from "@psgen/schema";
import { Temporal } from "@psgen/scalar-lib";
export abstract class A {
  @key
  id: string;
  // @ts-expect-error not a declared unit
  @temporalFormat("weeks")
  at: Temporal.DateTime;
}
`,
			want: []string{
				`a.schema.ts:8:3: @temporalFormat "weeks" is not a declared epoch unit (unix, unix_millis, unix_micros, unix_nanos)`,
			},
		},
		{
			name: "operation argument errors",
			kind: "API",
			source: `import { HttpMethod, hmacVerified, rateLimit, requirePermission, rest } from "@psgen/api";
// @ts-expect-error requestsPerMinute is not a number
@rateLimit({ requestsPerMinute: "many" })
export class Queries {
  @rest(HttpMethod.GET, "things")
  @requirePermission([])
  list(): string {
    throw new Error("schema declaration only");
  }
  // @ts-expect-error not an HttpMethod
  @rest("FETCH", "things")
  fetch(): string {
    throw new Error("schema declaration only");
  }
  @rest(HttpMethod.POST, "hooks")
  @hmacVerified({ provider: "" })
  hook(): string {
    throw new Error("schema declaration only");
  }
}
`,
			want: []string{
				"a.schema.ts:3:1: @rateLimit requires a literal requestsPerMinute",
				"a.schema.ts:6:3: @requirePermission requires a non-empty array of permission strings",
				`a.schema.ts:11:3: unsupported HTTP method "FETCH"`,
				"a.schema.ts:16:3: @hmacVerified requires a literal provider",
			},
		},
		{
			name: "versioned config errors survive a failed field",
			kind: "DB",
			source: `import { key, versioned } from "@psgen/db";
// @ts-expect-error retentionDays is not a number
@versioned({ retentionDays: "x" })
export abstract class Tenant {
  @key
  id: string;
  // @ts-expect-error unknown type
  bad: Nope;
}
`,
			want: []string{
				"a.schema.ts:3:1: @versioned retentionDays must be a number literal",
				`a.schema.ts:8:8: cannot resolve type "Nope"`,
			},
		},
		{
			name: "middleware errors keep the operation",
			kind: "API",
			source: `import { rateLimit } from "@psgen/api";
export class Queries {
  // @ts-expect-error requestsPerMinute is not a number
  @rateLimit({ requestsPerMinute: "many" })
  a(): string {
    throw new Error("schema declaration only");
  }
}
`,
			want: []string{
				"a.schema.ts:4:3: @rateLimit requires a literal requestsPerMinute",
				"a.schema.ts:4:3: operation a needs @rest(method, path?) or @manualRouteRegistration",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := decoratorTestService(t, tc.kind, map[string]string{"src/a.schema.ts": tc.source})
			_, _, err := LoadService(dir)
			if err == nil {
				t.Fatal("expected schema errors")
			}
			msg := err.Error()
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("missing diagnostic %q in:\n%s", want, msg)
				}
			}
		})
	}
}
