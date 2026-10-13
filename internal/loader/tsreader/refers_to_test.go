package tsreader

import (
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestRefersToAndSearchQueryLoad: RefersTo writes references on the field
// or argument whose value, or list of values, it wraps, in either nesting
// and under the other wrappers, with only the config members written; a
// SearchQuery is an optional query argument with search set, and a bare
// one is text (D61, D62).
func TestRefersToAndSearchQueryLoad(t *testing.T) {
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { Identity } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { HttpMethod, QueryParam, RefersTo, SearchQuery, rest } from "@superschematic/api";
export abstract class Item {
  id: Identity.UUID;
  sku: Identity.Slug;
  name: string;
}
export abstract class Line {
  one: RefersTo<Identity.UUID, Item>;
  listOutside: RefersTo<Identity.UUID, Item>[];
  listInside: RefersTo<Identity.UUID[], Item>;
  configured: Nullable<RefersTo<Identity.Slug, Item, { options: "Catalog.listItems"; value: "sku"; label: "name" }>>;
  bounded: Validate<RefersTo<Identity.UUID, Item>[], { listMax: 3 }>;
  plain: Identity.UUID;
}
export class Catalog {
  @rest(HttpMethod.GET, "items")
  listItems(
    q: SearchQuery<Validate<string, { maxLength: 50 }>>,
    text: SearchQuery,
    ids: QueryParam<Nullable<RefersTo<Identity.UUID, Item>[]>>
  ): Item[] {
    throw new Error("schema declaration only");
  }
}
`})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	item := &ir.ReferenceDef{Type: "Item"}
	want := map[string]struct {
		ref  ir.TypeRef
		refs *ir.ReferenceDef
	}{
		"one":         {ir.TypeRef{Name: "Identity.UUID"}, item},
		"listOutside": {ir.TypeRef{Name: "Identity.UUID", IsArray: true}, item},
		"listInside":  {ir.TypeRef{Name: "Identity.UUID", IsArray: true}, item},
		"configured":  {ir.TypeRef{Name: "Identity.Slug"}, &ir.ReferenceDef{Type: "Item", Options: "Catalog.listItems", Value: "sku", Label: "name"}},
		"bounded":     {ir.TypeRef{Name: "Identity.UUID", IsArray: true}, item},
		"plain":       {ir.TypeRef{Name: "Identity.UUID"}, nil},
	}
	for _, f := range schema.Types["Line"].Fields {
		w, ok := want[f.Name]
		if !ok {
			t.Errorf("unexpected field %s", f.Name)
			continue
		}
		if f.TypeRef != w.ref || !reflect.DeepEqual(f.References, w.refs) {
			t.Errorf("%s: TypeRef %+v references %+v, want %+v and %+v", f.Name, f.TypeRef, f.References, w.ref, w.refs)
		}
	}
	if f := schema.Types["Line"].Fields[4]; f.ValidateListMax == nil || *f.ValidateListMax != 3 {
		t.Errorf("bounded lost its listMax: %+v", f)
	}

	maxLength := 50
	args := schema.OperationSets[0].Operations[0].Arguments
	wantArgs := []*ir.ArgumentDef{
		{Name: "q", TypeRef: ir.TypeRef{Name: "string"}, ValidateMaxLength: &maxLength, IsQuery: true, Search: true},
		{Name: "text", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true, Search: true},
		{Name: "ids", TypeRef: ir.TypeRef{Name: "Identity.UUID", IsArray: true}, IsQuery: true, References: item},
	}
	if !reflect.DeepEqual(args, wantArgs) {
		for i := range args {
			t.Errorf("argument %d = %+v", i, *args[i])
		}
		t.Fatal("arguments differ")
	}
}

func TestRefersToAndSearchQueryRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, decl, want string
	}{
		{"a scalar target", `a: RefersTo<Identity.UUID, Identity.UUID>;`, "RefersTo<T, Target> needs a schema type as Target"},
		{"a primitive target", `a: RefersTo<string, string>;`, "RefersTo<T, Target> needs a schema type as Target"},
		{"a list target", `a: RefersTo<Identity.UUID, Item[]>;`, "RefersTo<T, Target> needs a schema type as Target"},
		{"a nullable target", `a: RefersTo<Identity.UUID, Nullable<Item>>;`, "RefersTo<T, Target> needs a schema type as Target"},
		{"twice", `a: RefersTo<RefersTo<Identity.UUID, Item>, Item>;`, "RefersTo is written once on a value; this one already refers to Item"},
		{"an empty member", `a: RefersTo<Identity.UUID, Item, { options: "" }>;`, "RefersTo options must be a non-empty string literal"},
		{"a search field", `a: SearchQuery;`, "SearchQuery marks an operation's argument; a field is no search"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { RefersTo, SearchQuery } from "@superschematic/api";
export abstract class Item {
  id: Identity.UUID;
}
export abstract class Line {
  ` + tc.decl + `
}
`})
			_, _, err := LoadService(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want %q", err, tc.want)
			}
		})
	}

	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { HttpMethod, SearchQuery, rest } from "@superschematic/api";
export class Catalog {
  @rest(HttpMethod.GET, "search")
  search(): SearchQuery {
    throw new Error("schema declaration only");
  }
}
`})
	if _, _, err := LoadService(dir); err == nil || !strings.Contains(err.Error(), "SearchQuery marks an operation's argument, not its result") {
		t.Fatalf("a search result: %v", err)
	}
}

// TestBrokenReferencesIsALocatedSchemaError: a reference whose target is
// no schema type fails the walk with a diagnostic at the type argument.
func TestBrokenReferencesIsALocatedSchemaError(t *testing.T) {
	_, _, err := LoadService(filepath.Join("testdata", "services", "broken-references"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	msg := err.Error()
	if !regexp.MustCompile(`broken\.schema\.ts:6:\d+: RefersTo<T, Target> needs a schema type as Target`).MatchString(msg) {
		t.Fatalf("unexpected error: %s", msg)
	}
}
