package tsreader

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestArraysOfArraysLoad covers every TypeScript spelling of T[][]: the
// bracket form, Array<Array<T>>, the mixed forms and the readonly variants.
// Each loads as one TypeRef with IsArray and IsArrayOfArrays set.
func TestArraysOfArraysLoad(t *testing.T) {
	dir := decoratorTestService(t, "General", map[string]string{"src/a.schema.ts": `import { Nullable, Validate } from "@superschematic/schema";
export enum Shade {
  Light = "light",
  Dark = "dark"
}
export type Point = {
  readonly x: number;
  readonly y: number;
};
export abstract class Shapes {
  brackets: string[][];
  generic: Array<Array<string>>;
  genericOuter: Array<string[]>;
  genericInner: Array<string>[];
  readonlyBrackets: readonly (readonly string[])[];
  readonlyGeneric: ReadonlyArray<ReadonlyArray<string>>;
  shades: Shade[][];
  polygons: Point[][];
  optional: Nullable<number[][]>;
  bounded: Validate<number[][], { listMin: 1; listMax: 4; min: 0 }>;
  single: Array<string>;
}
`})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	fields := map[string]*ir.FieldDef{}
	for _, f := range schema.Types["Shapes"].Fields {
		fields[f.Name] = f
	}
	nested := func(name string) ir.TypeRef {
		return ir.TypeRef{Name: name, IsArray: true, IsArrayOfArrays: true}
	}
	want := map[string]ir.TypeRef{
		"brackets":         nested("string"),
		"generic":          nested("string"),
		"genericOuter":     nested("string"),
		"genericInner":     nested("string"),
		"readonlyBrackets": nested("string"),
		"readonlyGeneric":  nested("string"),
		"shades":           nested("Shade"),
		"polygons":         nested("Point"),
		"optional":         nested("number"),
		"bounded":          nested("number"),
		"single":           {Name: "string", IsArray: true},
	}
	for name, ref := range want {
		f := fields[name]
		if f == nil {
			t.Errorf("field %s not loaded", name)
			continue
		}
		if f.TypeRef != ref {
			t.Errorf("%s TypeRef = %+v, want %+v", name, f.TypeRef, ref)
		}
	}
	if fields["brackets"].Required != true || fields["optional"].Required != false {
		t.Errorf("required: brackets %v, optional %v", fields["brackets"].Required, fields["optional"].Required)
	}
	bounded := fields["bounded"]
	if bounded.ValidateListMin == nil || *bounded.ValidateListMin != 1 ||
		bounded.ValidateListMax == nil || *bounded.ValidateListMax != 4 ||
		bounded.ValidateMin == nil || *bounded.ValidateMin != 0 {
		t.Errorf("bounded constraints = listMin %v listMax %v min %v", bounded.ValidateListMin, bounded.ValidateListMax, bounded.ValidateMin)
	}
}

// TestArraysOfArraysRejectedForms pins the diagnostics for the shapes T[][]
// does not cover: a third level, a map whose value is a list of lists, a
// nullable inner list and list bounds on the inner lists.
func TestArraysOfArraysRejectedForms(t *testing.T) {
	cases := []struct {
		name  string
		field string
		want  string
	}{
		{"three levels", "cube: string[][][];", "arrays nest at most two levels (T[][])"},
		{"three levels generic", "cube: Array<Array<Array<string>>>;", "arrays nest at most two levels (T[][])"},
		{"three levels mixed", "cube: Array<string[][]>;", "arrays nest at most two levels (T[][])"},
		{"map value", "byName: Record<string, string[][]>;", "map values cannot be arrays of arrays"},
		{"list of maps of lists", "rows: Record<string, string[]>[];", "map values cannot be arrays of arrays"},
		{"nullable inner list", "rows: Nullable<string[]>[];", "the inner lists of an array of arrays cannot be null"},
		{"nullable inner list union", "rows: (string[] | null)[];", "the inner lists of an array of arrays cannot be null"},
		{"inner list bounds", "rows: Validate<string[], { listMax: 3 }>[];", "list bounds of an array of arrays apply to the outer list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := decoratorTestService(t, "General", map[string]string{"src/a.schema.ts": `import { Nullable, Validate } from "@superschematic/schema";
export abstract class Shapes {
  ` + tc.field + `
}
`})
			_, _, err := LoadService(dir)
			if err == nil {
				t.Fatal("expected a schema error")
			}
			if !strings.Contains(err.Error(), "a.schema.ts:3:") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want a.schema.ts:3 and %q", err, tc.want)
			}
		})
	}
}
