package tsreader

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/registry"
)

// writeBehaviorClasses writes files into a directory of their own beside a
// tsconfig.json that resolves @superschematic/db to this checkout's
// packages, as a project's behaviors directory would, and returns it.
func writeBehaviorClasses(t *testing.T, files map[string]string) string {
	t.Helper()
	packages, err := filepath.Abs("../../../packages")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	config := map[string]any{
		"compilerOptions": map[string]any{
			"target": "ES2022", "module": "ESNext", "moduleResolution": "Bundler", "lib": []string{"ES2022"},
			"strict": true, "strictPropertyInitialization": false, "experimentalDecorators": true,
			"skipLibCheck": true, "noEmit": true,
			"paths": map[string]any{"@superschematic/*": []string{filepath.ToSlash(packages) + "/*/src/index.ts"}},
		},
		"include": []string{"*.behavior.ts"},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	files["tsconfig.json"] = string(data)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A project's behavior class (D103) declares every member a declaration
// holds: its name and Rust crate, a contract it implements and one it
// asks, a value field a create gives, a volatile field, a shared sub-table
// with a row's key, an operation of the table as a whole that checks a
// permission, a beside operation that logs nothing, an event, a signal,
// the create's parameters and a caller's precondition.
func TestABehaviorClassDeclaresEveryMember(t *testing.T) {
	dir := writeBehaviorClasses(t, map[string]string{"rating.behavior.ts": `
import { declaresBehavior, fromCreate, subTable, valueField, volatile, type Capacity, type Integer, type JsonSchema, type OneOf, type Open, type RowKey } from "@superschematic/db";

interface RatingConfig {
  /** The most stars a rating gives. */
  maxStars: JsonSchema<Integer, { minimum: 1; maximum: 10 }>;
  /** Who may clear the ratings. */
  clearPermission?: string;
}

/** The codes its refusals carry. */
enum RatingVeto {
  /** More stars than maxStars. */
  too_many_stars,
}

/** Shoppers rate an item. */
@declaresBehavior({
  name: "acme.Rating",
  requires: ["Workflow"],
  hearsChanges: true,
  vetoes: RatingVeto,
  asks: { Supply: "Asks nothing really." },
  crate: { crate: "acme-rules", path: "crates/rules", register: "acme_rules::Rating" },
})
export abstract class Rating<T, C extends RatingConfig> implements /** Room for more ratings. */ Capacity {
  /** The item's label, which a create gives. */
  @valueField @fromCreate label?: string;
  /** The average, computed at a read. */
  @volatile average: unknown;

  /**
   * Clears every rating.
   * @writes
   * @scope schema
   * @checks clearPermission
   * @returns How many it cleared.
   */
  abstract clear(): Integer;

  /**
   * Marks a rating read.
   * @writes
   * @beside
   * @log false
   * @asks read
   */
  abstract markRead(params: { at?: Integer }): OneOf<{ ok: true } | null>;

  /**
   * A rating was given.
   * @event
   */
  abstract rated(payload: { stars: Integer }): void;

  /**
   * Someone is rating.
   * @signal read
   */
  abstract rating(data: Open<{ by: string }>): void;

  /** What a create gives. */
  abstract create(params: { stars?: Integer }): void;

  /** What a caller presents. */
  abstract precondition(entry: { token: Integer }): void;

  /** The ratings across the table, by the rater. */
  @subTable({ key: "rater", shared: true })
  static ratings = class {
    /** Who rated. */
    rater: string;
    /** The rated row. */
    item: RowKey;
  };
}
`})
	classes, err := ReadBehaviorClasses(dir, nil)
	if err != nil {
		t.Fatalf("ReadBehaviorClasses: %v", err)
	}
	if len(classes) != 1 {
		t.Fatalf("read %d classes, want 1", len(classes))
	}
	class := classes[0]
	if class.File != "rating.behavior.ts" || class.Class != "Rating" {
		t.Fatalf("class %s in %s", class.Class, class.File)
	}
	if want := (registry.BehaviorRust{Crate: "acme-rules", Path: "crates/rules", Register: "acme_rules::Rating"}); class.Rules != want {
		t.Fatalf("rules %+v, want %+v", class.Rules, want)
	}
	got, err := registry.CanonicalDeclaration(class.Declaration)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "name": "acme.Rating",
  "description": "Shoppers rate an item.",
  "configSchema": {"additionalProperties":false,"properties":{"clearPermission":{"description":"Who may clear the ratings.","type":"string"},"maxStars":{"description":"The most stars a rating gives.","maximum":10,"minimum":1,"type":"integer"}},"required":["maxStars"],"type":"object"},
  "createParamsSchema": {"additionalProperties":false,"properties":{"stars":{"type":"integer"}},"type":"object"},
  "requires": [
    "Workflow"
  ],
  "fields": [
    {
      "name": "label",
      "description": "The item's label, which a create gives.",
      "type": "string",
      "optional": true,
      "fromCreate": true
    },
    {
      "name": "average",
      "description": "The average, computed at a read.",
      "volatile": true
    }
  ],
  "value": "label",
  "tables": [
    {
      "name": "ratings",
      "description": "The ratings across the table, by the rater.",
      "key": "rater",
      "scope": "schema",
      "fields": [
        {
          "name": "rater",
          "description": "Who rated.",
          "type": "string"
        },
        {
          "name": "item",
          "description": "The rated row.",
          "type": "string",
          "rowKey": true
        }
      ]
    }
  ],
  "hearsChanges": true,
  "operations": [
    {
      "name": "clear",
      "description": "Clears every rating.",
      "paramsSchema": {"additionalProperties":false,"type":"object"},
      "resultSchema": {"description":"How many it cleared.","type":"integer"},
      "writes": true,
      "scope": "schema",
      "permissions": [
        "clearPermission"
      ]
    },
    {
      "name": "markRead",
      "description": "Marks a rating read.",
      "paramsSchema": {"additionalProperties":false,"properties":{"at":{"type":"integer"}},"type":"object"},
      "resultSchema": {"oneOf":[{"additionalProperties":false,"properties":{"ok":{"const":true}},"required":["ok"],"type":"object"},{"type":"null"}]},
      "writes": true,
      "beside": true,
      "log": false,
      "asks": "read"
    }
  ],
  "events": [
    {
      "kind": "rated",
      "description": "A rating was given.",
      "payloadSchema": {"additionalProperties":false,"properties":{"stars":{"type":"integer"}},"required":["stars"],"type":"object"}
    }
  ],
  "contracts": [
    {
      "name": "Capacity",
      "role": "implements",
      "description": "Room for more ratings."
    },
    {
      "name": "Supply",
      "role": "asks",
      "description": "Asks nothing really."
    }
  ],
  "signals": [
    {
      "name": "rating",
      "description": "Someone is rating.",
      "paramsSchema": {"properties":{"by":{"type":"string"}},"required":["by"],"type":"object"},
      "access": "read"
    }
  ],
  "preconditionSchema": {"additionalProperties":false,"properties":{"token":{"type":"integer"}},"required":["token"],"type":"object"},
  "vetoes": [
    {
      "code": "too_many_stars",
      "description": "More stars than maxStars."
    }
  ]
}
`
	var gotCompact, wantCompact bytes.Buffer
	if err := json.Compact(&gotCompact, got); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&wantCompact, []byte(want)); err != nil {
		t.Fatal(err)
	}
	if gotCompact.String() != wantCompact.String() {
		t.Fatalf("derived:\n%s\nwant:\n%s", gotCompact.String(), wantCompact.String())
	}
}

// What a behavior's class may not say: a field with a value, a method with
// a body, a sub-table that is not static, a field of no column's type and
// a schema that names the class's type parameter.
func TestABehaviorClassRefusesWhatItCannotDeclare(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"a field with a value", `status: string = "draft";`, "field status takes no value: a class declares and never implements"},
		{"a method with a body", `transition(params: { to: string }): string { return params.to; }`, "transition is an abstract method signature"},
		{"a sub-table that is not static", `@subTable({ key: "n" }) rows = class { n: string; };`, "a sub-table is a static nested class"},
		{"a field of no column's type", `status: string[];`, "a field's type is a column's"},
		{"a schema that names the config", `abstract echo(params: C): string;`, "names no type parameter of its class (C)"},
		{"an optional field that is not the value", `note?: string;`, "only the value field takes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeBehaviorClasses(t, map[string]string{"thing.behavior.ts": `
import { declaresBehavior, subTable } from "@superschematic/db";
/** A thing. */
@declaresBehavior()
export abstract class Thing<C extends { a?: string }> {
  ` + tc.body + `
}
`})
			_, err := ReadBehaviorClasses(dir, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want one that says %q", err, tc.want)
			}
		})
	}
}
