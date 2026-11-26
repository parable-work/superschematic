package tsreader

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestGraphOperationsClass: the @graphOperations class of
// fixture-graph-api is an operation set with its root, its options and its
// clause and no operations, which the loader adds (D138); the root is an
// import of the API's database, and the API's other set is read as before.
func TestGraphOperationsClass(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-graph-api"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	sets := map[string]*ir.OperationSet{}
	for _, set := range schema.OperationSets {
		sets[set.Name] = set
	}
	recipes, queries := sets["RecipeOperations"], sets["RecipeQueries"]
	if recipes == nil || queries == nil {
		t.Fatalf("operation sets = %v", sets)
	}
	want := &ir.GraphOperationsConfig{
		Type:               "Recipe",
		Permissions:        map[string][]string{"release": {"recipes.release"}},
		By:                 []string{"createdBy"},
		OverridePermission: "recipes.admin",
	}
	if !reflect.DeepEqual(recipes.GraphOperations, want) {
		t.Errorf("RecipeOperations.GraphOperations = %+v, want %+v", recipes.GraphOperations, want)
	}
	if !reflect.DeepEqual(recipes.Permissions, []string{"recipes.write"}) || recipes.Auth || recipes.Public {
		t.Errorf("RecipeOperations' clause = auth %v, permissions %v, public %v", recipes.Auth, recipes.Permissions, recipes.Public)
	}
	if recipes.Operations == nil || len(recipes.Operations) != 0 {
		t.Errorf("the reader added operations: %v", recipes.Operations)
	}
	if !strings.HasPrefix(recipes.Comment, "A recipe's version graph as routes (D138):") {
		t.Errorf("RecipeOperations.Comment = %q", recipes.Comment)
	}
	if queries.IsGraphOperations() || len(queries.Operations) != 1 || queries.Auth {
		t.Errorf("RecipeQueries = %+v", queries)
	}
	wantImports := []ir.Import{{Package: "@schemas/fixture-version-graph-db", Types: []string{"Recipe"}}}
	if !reflect.DeepEqual(schema.Imports, wantImports) {
		t.Errorf("Imports = %+v, want %+v", schema.Imports, wantImports)
	}
	for name := range schema.Types {
		t.Errorf("the reader added the type %s", name)
	}
}

// TestGraphOperationsRefusals pins each refusal of a @graphOperations
// class's form, and of its decorator's arguments, at the node that
// carries it.
func TestGraphOperationsRefusals(t *testing.T) {
	const imports = `import { Authenticated, Encrypted, HttpMethod, behaviorOperations, graphOperations, rest, userSessions } from "@superschematic/api";
declare abstract class Recipe { id: string }
`
	const member = `RecipeOperations: a @graphOperations class has no members: the loader adds one operation for each operation of Recipe's version graph the set serves, and the generated server runs it; an operation that needs code of the project's own is a method of another class, which calls the graph's facade`
	cases := []struct {
		name   string
		kind   string
		source string
		want   []string
	}{
		{
			name: "a method",
			kind: "API",
			source: imports + `@graphOperations(Recipe)
export abstract class RecipeOperations {
  @rest(HttpMethod.POST, "recipes/{id}/publish")
  publish(id: string): boolean {
    throw new Error("schema declaration only");
  }
}
`,
			want: []string{`a.schema.ts:5:3: ` + member},
		},
		{
			name: "a property",
			kind: "API",
			source: imports + `@graphOperations(Recipe)
export abstract class RecipeOperations {
  note: string;
}
`,
			want: []string{`a.schema.ts:5:3: ` + member},
		},
		{
			name: "an Authenticated base",
			kind: "API",
			source: imports + `@graphOperations(Recipe)
export abstract class RecipeOperations extends Authenticated {}
`,
			want: []string{`a.schema.ts:4:48: RecipeOperations: a @graphOperations class does not extend Authenticated: write @auth on the class`},
		},
		{
			name: "an Encrypted base",
			kind: "API",
			source: imports + `@graphOperations(Recipe)
export abstract class RecipeOperations extends Encrypted {}
`,
			want: []string{`a.schema.ts:4:48: RecipeOperations: a @graphOperations class is not Encrypted: its routes' bodies are the graph's edits, which the generated handlers read as plain JSON`},
		},
		{
			name: "twice on one class",
			kind: "API",
			source: imports + `@graphOperations(Recipe)
@graphOperations(Recipe)
export abstract class RecipeOperations {}
`,
			want: []string{`a.schema.ts:4:1: @graphOperations appears twice on the class: a set serves one graph`},
		},
		{
			name: "beside @userSessions",
			kind: "API",
			source: imports + `@userSessions()
@graphOperations(Recipe)
export abstract class RecipeOperations {}
`,
			want: []string{`a.schema.ts:4:1: @graphOperations contradicts @userSessions on the same class: a class takes one of them`},
		},
		{
			name: "beside @behaviorOperations",
			kind: "API",
			source: imports + `@graphOperations(Recipe)
@behaviorOperations(Recipe)
export abstract class RecipeOperations {}
`,
			want: []string{`a.schema.ts:4:1: @behaviorOperations contradicts @graphOperations on the same class: a class takes one of them`},
		},
		{
			name: "no argument",
			kind: "API",
			source: imports + `// @ts-expect-error the root is required
@graphOperations()
export abstract class RecipeOperations {}
`,
			want: []string{`@graphOperations takes the root of the version graph the set serves, imported from the API's database, and its options: @graphOperations(Recipe, { by: ["createdBy"] })`},
		},
		{
			name: "an option it has no member for",
			kind: "API",
			source: imports + `// @ts-expect-error paths is no option
@graphOperations(Recipe, { paths: "recipes" })
export abstract class RecipeOperations {}
`,
			want: []string{`a.schema.ts:4:26: @graphOperations takes operations, permissions, by and overridePermission, not paths`},
		},
		{
			name: "a DB schema",
			kind: "DB",
			source: imports + `@graphOperations(Recipe)
export abstract class RecipeOperations {}
`,
			want: []string{`a.schema.ts:3:1: @graphOperations is only allowed in an API schema (this service is kind DB)`},
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
