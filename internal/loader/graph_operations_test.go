package loader

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/graphops"
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// graphAPIFixture is the TypeScript-authored API whose @graphOperations set
// names fixture-version-graph-db's Recipe, the root of its one DB
// dependency's version graph (D138).
const (
	graphAPIFixture = "tsreader/testdata/services/fixture-graph-api"
	graphDBFixture  = "tsreader/testdata/services/fixture-version-graph-db"
)

// loadGraphDB loads fixture-version-graph-db, its version graph expanded.
func loadGraphDB(t *testing.T) *ir.Schema {
	t.Helper()
	db, err := LoadService(filepath.FromSlash(graphDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// TestGraphOperationsExpand: the API loads with its @graphOperations set
// filled from its database, which the loader reads once, operation by
// operation as D138 lists them: the route, the arguments, the result, the
// clause with the operation's own permissions, the docs, the tool and the
// marker with the row rule on the operations that write a change set, and
// the types and enums they name, each the loader's.
func TestGraphOperationsExpand(t *testing.T) {
	db := loadGraphDB(t)
	var asked []string
	schema, err := LoadService(filepath.FromSlash(graphAPIFixture), WithDependencyLoader(func(name string) (*ir.Schema, error) {
		asked = append(asked, name)
		return db, nil
	}))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if !reflect.DeepEqual(asked, []string{"fixture-version-graph-db"}) {
		t.Errorf("the dependency loader was asked for %v, want fixture-version-graph-db once", asked)
	}
	var set *ir.OperationSet
	for _, each := range schema.OperationSets {
		if each.Name == "RecipeOperations" {
			set = each
		} else if len(each.Operations) != 1 || each.Operations[0].GraphOperation != nil || each.Operations[0].Origin != "" {
			t.Errorf("%s = %s", each.Name, marshal(t, each))
		}
	}
	if set == nil {
		t.Fatal("the API has no RecipeOperations set")
	}
	wantConfig := &ir.GraphOperationsConfig{
		Type:               "Recipe",
		Permissions:        map[string][]string{"release": {"recipes.release"}},
		By:                 []string{"createdBy"},
		OverridePermission: "recipes.admin",
	}
	if !reflect.DeepEqual(set.GraphOperations, wantConfig) || !reflect.DeepEqual(set.Permissions, []string{"recipes.write"}) {
		t.Errorf("RecipeOperations = %s", marshal(t, set))
	}

	const uuid, int64Name = "Identity.UUID", "Generic.Int64"
	arg := func(name, typeName string) string { return name + ":" + typeName }
	type want struct {
		name, method, path string
		args               []string
		result             string
		handle, title      string
		owned, reads       bool
		permissions        []string
	}
	write := []string{"recipes.write"}
	wants := []want{
		{"branch", "POST", "recipes/{id}/refs", []string{arg("id", uuid), arg("input", "RecipeGraphBranchInput")}, "RecipeGraphRef", "recipe_branch", "Recipe graph: branch", false, false, write},
		{"save", "PUT", "recipes/{id}/refs/{ref}/edits", []string{arg("id", uuid), arg("ref", uuid), arg("input", "RecipeGraphSaveInput")}, "RecipeGraphSaveResult", "recipe_save", "Recipe graph: save", true, false, write},
		{"arrange", "POST", "recipes/{id}/refs/{ref}/arrange", []string{arg("id", uuid), arg("ref", uuid), arg("input", "RecipeGraphArrangeInput")}, "RecipeGraphSaveResult", "recipe_arrange", "Recipe graph: arrange", true, false, write},
		{"commit", "POST", "recipes/{id}/refs/{ref}/commit", []string{arg("id", uuid), arg("ref", uuid), arg("input", "RecipeGraphCommitInput")}, "RecipeGraphCommitResult", "recipe_commit", "Recipe graph: commit", true, false, write},
		{"seal", "POST", "recipes/{id}/refs/{ref}/seal", []string{arg("id", uuid), arg("ref", uuid), arg("input", "RecipeGraphSealInput")}, "RecipeGraphCommitResult", "recipe_seal", "Recipe graph: seal", true, false, write},
		{"merge", "POST", "recipes/{id}/refs/{ref}/merge", []string{arg("id", uuid), arg("ref", uuid), arg("input", "RecipeGraphMergeInput")}, "RecipeGraphMergeResult", "recipe_merge", "Recipe graph: merge", false, false, write},
		{"rebase", "POST", "recipes/{id}/refs/{ref}/rebase", []string{arg("id", uuid), arg("ref", uuid), arg("input", "RecipeGraphRebaseInput")}, "RecipeGraphMergeResult", "recipe_rebase", "Recipe graph: rebase", true, false, write},
		{"revert", "POST", "recipes/{id}/refs/{ref}/revert", []string{arg("id", uuid), arg("ref", uuid), arg("input", "RecipeGraphRevertInput")}, "RecipeGraphCommitResult", "recipe_revert", "Recipe graph: revert", true, false, write},
		{"discard", "POST", "recipes/{id}/refs/{ref}/discard", []string{arg("id", uuid), arg("ref", uuid), arg("input", "RecipeGraphDiscardInput")}, "boolean", "recipe_discard", "Recipe graph: discard", true, false, write},
		{"history", "GET", "recipes/{id}/refs/{ref}/history", []string{arg("id", uuid), arg("ref", uuid)}, "RecipeGraphCommit[]", "recipe_history", "Recipe graph: history", false, true, write},
		{"compose", "GET", "recipes/{id}/refs/{ref}/tree", []string{arg("id", uuid), arg("ref", uuid)}, "RecipeGraphTree", "recipe_compose", "Recipe graph: compose", false, true, write},
		{"release", "POST", "recipes/{id}/release", []string{arg("id", uuid), arg("input", "RecipeGraphReleaseInput")}, "RecipeGraphRelease", "recipe_release", "Recipe graph: release", false, false, []string{"recipes.release"}},
		{"released", "GET", "recipes/{id}/released", []string{arg("id", uuid)}, "RecipeGraphReleased", "recipe_released", "Recipe graph: released", false, true, write},
		{"materialize", "GET", "recipes/{id}/commits/{commit}", []string{arg("id", uuid), arg("commit", uuid)}, "RecipeGraphTree", "recipe_materialize", "Recipe graph: materialize", false, true, write},
		{"diff", "GET", "recipes/{id}/diff", []string{arg("id", uuid), "?" + arg("from", uuid), "?" + arg("to", uuid)}, "RecipeGraphChange[]", "recipe_diff", "Recipe graph: diff", false, true, write},
		{"entityHistory", "GET", "recipes/{id}/entities/{kind}/{key}/history", []string{arg("id", uuid), arg("kind", "string"), arg("key", uuid)}, "RecipeGraphEntityChange[]", "recipe_entity_history", "Recipe graph: entity history", false, true, write},
	}
	if len(set.Operations) != len(wants) {
		t.Fatalf("RecipeOperations has %d operations, want %d: %s", len(set.Operations), len(wants), marshal(t, set.Operations))
	}
	for i, w := range wants {
		op := set.Operations[i]
		where := "RecipeOperations." + w.name
		if op.Name != w.name || op.HTTPMethod != w.method || op.RestPath != w.path || op.Origin != ir.OriginGraphOperations || !op.Required {
			t.Errorf("%s: %s %s %s, origin %q, required %v", where, op.Name, op.HTTPMethod, op.RestPath, op.Origin, op.Required)
			continue
		}
		var args []string
		for _, a := range op.Arguments {
			text := arg(a.Name, a.TypeRef.Name)
			if a.IsQuery {
				text = "?" + text
			}
			if !a.Required || a.TypeRef.IsArray {
				t.Errorf("%s: argument %s is optional or a list", where, a.Name)
			}
			args = append(args, text)
		}
		if !reflect.DeepEqual(args, w.args) {
			t.Errorf("%s: arguments = %v, want %v", where, args, w.args)
		}
		result := op.TypeRef.Name
		if op.TypeRef.IsArray {
			result += "[]"
		}
		if result != w.result {
			t.Errorf("%s: result = %s, want %s", where, result, w.result)
		}
		if op.Auth || !reflect.DeepEqual(op.Permissions, w.permissions) || op.Public || op.ServiceCallers != nil || op.Middleware != nil {
			t.Errorf("%s: clause = auth %v, permissions %v, public %v", where, op.Auth, op.Permissions, op.Public)
		}
		if op.Docs == nil || op.Docs.Title != w.title || op.Docs.Description == "" || op.Docs.Description != op.Comment ||
			op.Docs.Capability != "recipe.graph."+strings.ReplaceAll(strings.TrimPrefix(w.handle, "recipe_"), "_", "-") ||
			op.Docs.Lifecycle != ir.DocsLifecycleActive || op.Docs.Visibility != ir.DocsVisibilityPublic || op.Docs.MappingStatus != ir.DocsMappingStatusMapped {
			t.Errorf("%s: docs = %s", where, marshal(t, op.Docs))
		}
		if readOnly := op.Docs != nil && op.Docs.ReplayMode == ir.DocsReplayModeReadOnly; readOnly != w.reads {
			t.Errorf("%s: read only = %v, want %v", where, readOnly, w.reads)
		}
		if op.MCP == nil || op.MCP.Handle != w.handle || op.MCP.Hidden || op.MCP.Invocation != (ir.MCPInvocation{Key: "invocationPolicy", Value: "auto"}) {
			t.Errorf("%s: mcp = %+v", where, op.MCP)
		}
		wantMarker := &ir.GraphOperation{Database: "fixture-version-graph-db", Graph: "Recipe", Type: "Recipe", Key: "id", Operation: w.name}
		if w.owned {
			wantMarker.By, wantMarker.OverridePermission = []string{"createdBy"}, "recipes.admin"
		}
		if !reflect.DeepEqual(op.GraphOperation, wantMarker) {
			t.Errorf("%s: marker = %+v, want %+v", where, op.GraphOperation, wantMarker)
		}
	}

	// The types are the loader's and the API's own, named from the graph.
	fields := func(name string) []string {
		td := schema.Types[name]
		if td == nil {
			t.Errorf("the type %s was not added", name)
			return nil
		}
		if td.Origin != ir.OriginGraphOperations || td.Owner != "fixture-graph-api" {
			t.Errorf("%s: origin %q, owner %q", name, td.Origin, td.Owner)
		}
		var out []string
		for _, fd := range td.Fields {
			text := fd.Name + ":" + fd.TypeRef.Name
			if fd.TypeRef.IsArrayOfArrays {
				text += "[][]"
			} else if fd.TypeRef.IsArray {
				text += "[]"
			}
			if !fd.Required {
				text += "?"
			}
			if fd.Origin != ir.OriginGraphOperations {
				t.Errorf("%s.%s: origin %q", name, fd.Name, fd.Origin)
			}
			out = append(out, text)
		}
		return out
	}
	for name, want := range map[string][]string{
		"RecipeGraphRef":          {"id:Identity.UUID", "root:Identity.UUID", "parent:Identity.UUID?", "base:Identity.UUID?", "head:Identity.UUID?", "name:string", "sealed:boolean", "discarded:boolean", "version:Generic.Int64"},
		"RecipeGraphCommit":       {"id:Identity.UUID", "root:Identity.UUID", "ref:Identity.UUID", "parent:Identity.UUID?", "message:string", "schemaEpoch:Generic.Int64", "contentHash:string", "sequence:Generic.Int64?", "createdAt:Temporal.DateTime", "createdBy:Identity.UUID", "snapshot:boolean"},
		"RecipeGraphRelease":      {"id:Identity.UUID", "root:Identity.UUID", "commit:Identity.UUID", "version:Generic.Int64"},
		"RecipeGraphFinding":      {"code:string", "kind:string", "entityKey:Identity.UUID?", "message:string"},
		"RecipeGraphStepRow":      {"entityKey:Identity.UUID", "position:Generic.Int64", "instruction:string", "timings:Generic.JSON", "scratch:string?"},
		"RecipeGraphStepRowInput": {"entityKey:Identity.UUID?", "position:Generic.Int64", "instruction:string", "timings:Generic.JSON", "scratch:string?"},
		"RecipeGraphStepRowEdits": {"upsert:RecipeGraphStepRowInput[]?", "delete:Identity.UUID[]?", "unset:Identity.UUID[]?"},
		"RecipeGraphPartRow":      {"entityKey:Identity.UUID", "parentKey:Identity.UUID?", "rank:string", "heading:string"},
		"RecipeGraphUtensilRow":   {"entityKey:Identity.UUID", "name:string"},
		"RecipeGraphTastingRow": {"entityKey:Identity.UUID", "taster:Identity.UserID", "salty:boolean", "score:number", "servings:Generic.Int64",
			"tastedOn:Temporal.Date", "tastedAt:Temporal.DateTime", "servedAt:Temporal.Time", "rested:Temporal.Duration", "verdict:RecipeGraphVerdict",
			"remarks:Generic.JSON", "tags:string[]", "helpers:Identity.UUID[]", "bites:Generic.Int64[][]"},
		"RecipeGraphTree": {"cover:RecipeGraphCoverRow[]", "ingredient:RecipeGraphIngredientRow[]", "note:RecipeGraphNoteRow[]", "part:RecipeGraphPartRow[]",
			"step:RecipeGraphStepRow[]", "tasting:RecipeGraphTastingRow[]", "utensil:RecipeGraphUtensilRow[]", "contentHash:string", "findings:RecipeGraphFinding[]"},
		"RecipeGraphRows": {"cover:RecipeGraphCoverRow[]", "ingredient:RecipeGraphIngredientRow[]", "note:RecipeGraphNoteRow[]", "part:RecipeGraphPartRow[]",
			"step:RecipeGraphStepRow[]", "tasting:RecipeGraphTastingRow[]", "utensil:RecipeGraphUtensilRow[]"},
		"RecipeGraphEdits": {"cover:RecipeGraphCoverRowEdits?", "ingredient:RecipeGraphIngredientRowEdits?", "note:RecipeGraphNoteRowEdits?", "part:RecipeGraphPartRowEdits?",
			"step:RecipeGraphStepRowEdits?", "tasting:RecipeGraphTastingRowEdits?", "utensil:RecipeGraphUtensilRowEdits?"},
		"RecipeGraphSaveResult":   {"ref:RecipeGraphRef", "saved:RecipeGraphRows"},
		"RecipeGraphCommitResult": {"ref:RecipeGraphRef", "commit:RecipeGraphCommit?"},
		"RecipeGraphMergeResult":  {"ref:RecipeGraphRef", "commit:RecipeGraphCommit?", "conflicts:RecipeGraphConflict[]"},
		"RecipeGraphConflict": {"kind:RecipeGraphKind", "entityKey:Identity.UUID", "path:string", "base:string?", "ours:string?", "theirs:string?",
			"oursAuthor:Identity.UUID?", "theirsAuthor:Identity.UUID?"},
		"RecipeGraphResolution":   {"kind:RecipeGraphKind", "entityKey:Identity.UUID", "path:string", "take:RecipeGraphTake?", "value:string?"},
		"RecipeGraphChange":       {"kind:RecipeGraphKind", "entityKey:Identity.UUID", "operation:RecipeGraphChangeOperation", "row:string?"},
		"RecipeGraphEntityChange": {"commit:RecipeGraphCommit", "operation:RecipeGraphChangeOperation"},
		"RecipeGraphReleased":     {"release:RecipeGraphRelease", "tree:RecipeGraphTree"},
		"RecipeGraphBranchInput":  {"from:Identity.UUID?", "name:string"},
		"RecipeGraphSaveInput":    {"version:Generic.Int64", "edits:RecipeGraphEdits"},
		"RecipeGraphArrangeInput": {"version:Generic.Int64", "kind:RecipeGraphKind", "parent:Identity.UUID?", "keys:Identity.UUID[]"},
		"RecipeGraphCommitInput":  {"version:Generic.Int64", "message:string?", "tag:boolean?"},
		"RecipeGraphSealInput":    {"version:Generic.Int64"},
		"RecipeGraphMergeInput":   {"into:Identity.UUID?", "version:Generic.Int64", "resolutions:RecipeGraphResolution[]?", "message:string?", "tag:boolean?"},
		"RecipeGraphRebaseInput":  {"version:Generic.Int64", "resolutions:RecipeGraphResolution[]?"},
		"RecipeGraphRevertInput":  {"version:Generic.Int64", "toCommit:Identity.UUID"},
		"RecipeGraphDiscardInput": {"version:Generic.Int64"},
		"RecipeGraphReleaseInput": {"commit:Identity.UUID", "version:Generic.Int64"},
	} {
		if got := fields(name); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	for name, role := range map[string]ir.Role{
		"RecipeGraphStepRow": ir.RoleEmbeddedStruct, "RecipeGraphStepRowInput": ir.RoleAPIInput, "RecipeGraphEdits": ir.RoleAPIInput,
		"RecipeGraphResolution": ir.RoleAPIInput, "RecipeGraphSaveInput": ir.RoleAPIInput, "RecipeGraphTree": ir.RoleEmbeddedStruct,
	} {
		if td := schema.Types[name]; td != nil && td.Role != role {
			t.Errorf("%s: role %q, want %q", name, td.Role, role)
		}
	}
	for name, want := range map[string][]string{
		"RecipeGraphKind":            {"cover", "ingredient", "note", "part", "step", "tasting", "utensil"},
		"RecipeGraphChangeOperation": {"ADD", "UPDATE", "DELETE"},
		"RecipeGraphTake":            {"base", "ours", "theirs"},
		"RecipeGraphVerdict":         {"again", "tweak", "never"},
	} {
		ed := schema.Enums[name]
		if ed == nil || ed.Origin != ir.OriginGraphOperations || ed.Owner != "fixture-graph-api" {
			t.Errorf("the enum %s = %s", name, marshal(t, ed))
			continue
		}
		var values []string
		for _, v := range ed.Values {
			values = append(values, v.SerializedAs)
		}
		if !reflect.DeepEqual(values, want) {
			t.Errorf("%s = %v, want %v", name, values, want)
		}
	}
	for _, name := range []string{int64Name, uuid, "Generic.JSON", "Temporal.DateTime", "Identity.UserID", "Temporal.Duration"} {
		if def := schema.Scalars[name]; def == nil || len(def.TypeMappings) == 0 {
			t.Errorf("the scalar %s is not hydrated: %+v", name, def)
		}
	}
	// The database is as it was: the expansion adds nothing to it.
	for name, td := range db.Types {
		if td.Origin == ir.OriginGraphOperations {
			t.Errorf("the database gained %s", name)
		}
	}
}

// recipesAPI is an API of fixture-version-graph-db, its one DB dependency,
// importing Recipe from it, with the sets given.
func recipesAPI(sets ...*ir.OperationSet) *ir.Schema {
	api := ir.NewSchema("recipes-api", ir.SchemaKindAPI)
	api.Dependencies = []ir.ServiceRef{{Name: "fixture-version-graph-db", Kind: ir.SchemaKindDB}}
	api.Imports = []ir.Import{{Package: "@schemas/fixture-version-graph-db", Types: []string{"Recipe", "Step", "Verdict"}}}
	api.OperationSets = sets
	return api
}

// graphSet is a @graphOperations set of root, with the options mutate
// writes.
func graphSet(name, root string, mutate func(*ir.GraphOperationsConfig)) *ir.OperationSet {
	cfg := &ir.GraphOperationsConfig{Type: root}
	if mutate != nil {
		mutate(cfg)
	}
	return &ir.OperationSet{Name: name, Operations: []*ir.FieldDef{}, GraphOperations: cfg}
}

// TestGraphOperationsRefusals pins each refusal of the checks of a
// @graphOperations set (D138): its form, its options, the API's database,
// the root, a second set of a root, and the names, routes and handles the
// expansion adds. An API the checks refuse is not expanded, and the pass
// reports the refusal among its findings.
func TestGraphOperationsRefusals(t *testing.T) {
	reg := registry.New(naming.Default())
	fixture := loadGraphDB(t)
	same := func() *ir.Schema { return fixture }
	// A copy of the database to change: the fixture loaded again.
	changed := func(mutate func(*ir.Schema)) func() *ir.Schema {
		return func() *ir.Schema {
			db := loadGraphDB(t)
			mutate(db)
			return db
		}
	}
	plain := func() *ir.OperationSet { return graphSet("RecipeOperations", "Recipe", nil) }
	authored := func(name string, op *ir.FieldDef) *ir.OperationSet {
		return &ir.OperationSet{Name: name, Operations: []*ir.FieldDef{op}}
	}
	const operations = "branch, save, arrange, commit, seal, merge, rebase, revert, discard, history, compose, release, released, materialize, diff, entityHistory"
	cases := []struct {
		name string
		api  func() *ir.Schema
		db   func() *ir.Schema
		want string
	}{
		{"an authored operation", func() *ir.Schema {
			set := plain()
			set.Operations = []*ir.FieldDef{{Name: "publish", TypeRef: ir.TypeRef{Name: "boolean"}, Required: true, HTTPMethod: "POST"}}
			return recipesAPI(set)
		}, same, "RecipeOperations: a @graphOperations class has no members: the loader adds one operation for each operation of Recipe's version graph the set serves, and the generated server runs it; an operation that needs code of the project's own is a method of another class, which calls the graph's facade"},
		{"encrypted", func() *ir.Schema {
			set := plain()
			set.Encrypted = true
			return recipesAPI(set)
		}, same, "RecipeOperations: a @graphOperations class is not Encrypted: its routes' bodies are the graph's edits, which the generated handlers read as plain JSON"},
		{"not an API", func() *ir.Schema {
			api := recipesAPI(plain())
			api.Kind = ir.SchemaKindGeneral
			return api
		}, same, "RecipeOperations: @graphOperations is only allowed in an API schema (this schema has kind General)"},
		{"@publicRoute with @auth", func() *ir.Schema {
			set := plain()
			set.Public, set.Auth = true, true
			return recipesAPI(set)
		}, same, "RecipeOperations: @publicRoute contradicts @auth on the class: a public route needs no caller"},
		{"@publicRoute with @requirePermission", func() *ir.Schema {
			set := plain()
			set.Public, set.Permissions = true, []string{"recipes.write"}
			return recipesAPI(set)
		}, same, "RecipeOperations: @publicRoute contradicts @requirePermission on the class: a public route needs no caller"},
		{"@publicRoute with @requireService", func() *ir.Schema {
			set := plain()
			set.Public, set.ServiceCallers = true, &ir.ServiceCallers{Mode: ir.ServiceCallersRequire}
			return recipesAPI(set)
		}, same, "RecipeOperations: @publicRoute contradicts @requireService on the class: a public route needs no caller"},
		{"an unknown operation", func() *ir.Schema {
			return recipesAPI(graphSet("RecipeOperations", "Recipe", func(c *ir.GraphOperationsConfig) { c.Operations = []string{"save", "publish"} }))
		}, same, `RecipeOperations: @graphOperations lists the operation "publish", which is none of a version graph's: its operations are ` + operations},
		{"an operation named twice", func() *ir.Schema {
			return recipesAPI(graphSet("RecipeOperations", "Recipe", func(c *ir.GraphOperationsConfig) { c.Operations = []string{"save", "commit", "save"} }))
		}, same, "RecipeOperations: @graphOperations lists the operation save twice"},
		{"permissions for an unknown operation", func() *ir.Schema {
			return recipesAPI(graphSet("RecipeOperations", "Recipe", func(c *ir.GraphOperationsConfig) {
				c.Permissions = map[string][]string{"publish": {"recipes.release"}}
			}))
		}, same, `RecipeOperations: @graphOperations names permissions for "publish", which is none of a version graph's operations: they are ` + operations},
		{"permissions for an operation the set does not serve", func() *ir.Schema {
			return recipesAPI(graphSet("RecipeOperations", "Recipe", func(c *ir.GraphOperationsConfig) {
				c.Operations = []string{"branch", "save"}
				c.Permissions = map[string][]string{"release": {"recipes.release"}}
			}))
		}, same, "RecipeOperations: @graphOperations names permissions for release, which the set does not serve: its operations lists branch, save"},
		{"an empty permission list", func() *ir.Schema {
			return recipesAPI(graphSet("RecipeOperations", "Recipe", func(c *ir.GraphOperationsConfig) {
				c.Permissions = map[string][]string{"release": {}}
			}))
		}, same, "RecipeOperations: @graphOperations names no permission for release: permissions.release lists the permissions the operation needs, at least one"},
		{"permissions on a public set", func() *ir.Schema {
			set := graphSet("RecipeOperations", "Recipe", func(c *ir.GraphOperationsConfig) {
				c.Permissions = map[string][]string{"release": {"recipes.release"}}
			})
			set.Public = true
			return recipesAPI(set)
		}, same, "RecipeOperations: @graphOperations names permissions for release, and @publicRoute on the class opens every route it serves: a public route needs no caller"},
		{"overridePermission without by", func() *ir.Schema {
			return recipesAPI(graphSet("RecipeOperations", "Recipe", func(c *ir.GraphOperationsConfig) { c.OverridePermission = "recipes.admin" }))
		}, same, `RecipeOperations: @graphOperations overridePermission lifts a row rule, and the set has none: name the ref's creator in by, by: ["createdBy"]`},
		{"by another member", func() *ir.Schema {
			return recipesAPI(graphSet("RecipeOperations", "Recipe", func(c *ir.GraphOperationsConfig) { c.By = []string{"updatedBy"} }))
		}, same, `RecipeOperations: @graphOperations by is ["createdBy"] for now, the ref's creator, and this names updatedBy: the operations that write a change set are kept to the caller who branched it`},
		{"by two members", func() *ir.Schema {
			return recipesAPI(graphSet("RecipeOperations", "Recipe", func(c *ir.GraphOperationsConfig) { c.By = []string{"createdBy", "updatedBy"} }))
		}, same, `RecipeOperations: @graphOperations by is ["createdBy"] for now, the ref's creator, and this names createdBy, updatedBy: the operations that write a change set are kept to the caller who branched it`},
		{"by on a public set", func() *ir.Schema {
			set := graphSet("RecipeOperations", "Recipe", func(c *ir.GraphOperationsConfig) { c.By = []string{"createdBy"} })
			set.Public = true
			return recipesAPI(set)
		}, same, "RecipeOperations: @graphOperations by keeps a change set to the caller who branched it, and @publicRoute on the class admits a request with no caller"},
		{"no database", func() *ir.Schema {
			api := recipesAPI(plain())
			api.Dependencies = nil
			return api
		}, same, "RecipeOperations: @graphOperations(Recipe) runs Recipe's version graph on the API's database, and recipes-api connects to none: set its authDb, or give it one DB dependency, the DB service that declares Recipe"},
		{"several databases and no authDb", func() *ir.Schema {
			api := recipesAPI(plain())
			api.Dependencies = append(api.Dependencies, ir.ServiceRef{Name: "billing-db", Kind: ir.SchemaKindDB})
			return api
		}, same, "RecipeOperations: @graphOperations(Recipe) runs Recipe's version graph on the API's database, and recipes-api names 2 DB dependencies and no authDb: set its authDb to the one that declares Recipe"},
		{"not imported", func() *ir.Schema {
			api := recipesAPI(plain())
			api.Imports = nil
			return api
		}, same, "RecipeOperations: @graphOperations names Recipe, which recipes-api does not import; import it from fixture-version-graph-db, its database"},
		{"imported from another service", func() *ir.Schema {
			api := recipesAPI(plain())
			api.Imports = []ir.Import{{Package: "@schemas/cookbook-db", Types: []string{"Recipe"}}}
			return api
		}, same, "RecipeOperations: @graphOperations names Recipe, which recipes-api imports from cookbook-db, not from fixture-version-graph-db, its database (its authDb, or its one DB dependency)"},
		{"not declared", func() *ir.Schema {
			api := recipesAPI(graphSet("MenuOperations", "Menu", nil))
			api.Imports = []ir.Import{{Package: "@schemas/fixture-version-graph-db", Types: []string{"Menu"}}}
			return api
		}, same, "MenuOperations: @graphOperations names Menu, which fixture-version-graph-db does not declare"},
		{"not a table", func() *ir.Schema {
			api := recipesAPI(graphSet("VerdictOperations", "RecipeCommitted", nil))
			api.Imports = []ir.Import{{Package: "@schemas/fixture-version-graph-db", Types: []string{"RecipeCommitted"}}}
			return api
		}, same, "VerdictOperations: @graphOperations names RecipeCommitted, which is not a table of fixture-version-graph-db: a set serves the root of a version graph, a table marked @versionGraph (D138)"},
		{"a member, not the root", func() *ir.Schema {
			return recipesAPI(graphSet("StepOperations", "Step", nil))
		}, same, "StepOperations: @graphOperations names Step, a member of Recipe's version graph, not its root: name Recipe, the table marked @versionGraph (D138)"},
		{"a table that is no root", func() *ir.Schema {
			api := recipesAPI(graphSet("RefOperations", "RecipeRef", nil))
			api.Imports = []ir.Import{{Package: "@schemas/fixture-version-graph-db", Types: []string{"RecipeRef"}}}
			return api
		}, same, "RefOperations: @graphOperations names RecipeRef, a table of fixture-version-graph-db that is no version graph's root; a set serves a table marked @versionGraph (D138)"},
		{"a key named as a path parameter", func() *ir.Schema { return recipesAPI(plain()) },
			changed(func(db *ir.Schema) {
				for _, fd := range db.Types["Recipe"].Fields {
					if fd.Key {
						fd.Name = "ref"
					}
				}
			}), "RecipeOperations: @graphOperations names Recipe, whose key field is named ref, as a path parameter of its routes is (ref, commit, kind, key): serve the graph from code of the project's own, or rename the key"},
		{"two sets of one root", func() *ir.Schema {
			return recipesAPI(plain(), graphSet("RecipeReleases", "Recipe", func(c *ir.GraphOperationsConfig) { c.Operations = []string{"released"} }))
		}, same, "RecipeReleases: @graphOperations names Recipe, which RecipeOperations names too: an API serves a root's graph from one set"},
		{"arrange with no kind to place", func() *ir.Schema {
			return recipesAPI(graphSet("RecipeOperations", "Recipe", func(c *ir.GraphOperationsConfig) { c.Operations = []string{"save", "arrange"} }))
		}, changed(func(db *ir.Schema) {
			db.Types["Part"].GraphMember.Order = ""
		}), "RecipeOperations: @graphOperations(Recipe) lists arrange, which places the rows of a kind whose order is a string key, and no member of Recipe's graph has one: take arrange off the list, or order a member by a string field (D137)"},
		{"a type the API declares", func() *ir.Schema {
			api := recipesAPI(plain())
			api.Types["RecipeGraphRef"] = &ir.TypeDef{Name: "RecipeGraphRef", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true}}}
			return api
		}, same, "RecipeOperations: @graphOperations(Recipe) adds the type RecipeGraphRef (A line of the Recipe graph: a primary line when parent is absent, else a change set), and recipes-api already declares or imports one of that name; rename it"},
		{"an enum the API imports", func() *ir.Schema {
			api := recipesAPI(plain())
			api.Imports[0].Types = append(api.Imports[0].Types, "RecipeGraphKind")
			return api
		}, same, "RecipeOperations: @graphOperations(Recipe) adds the enum RecipeGraphKind (A kind of the Recipe graph's entities, as its descriptor names it), and recipes-api already declares or imports one of that name; rename it"},
		{"an operation another set of the namespace takes", func() *ir.Schema {
			return recipesAPI(plain(), authored("RecipeQueries", &ir.FieldDef{Name: "history", TypeRef: ir.TypeRef{Name: "string"}, Required: true, HTTPMethod: "GET", RestPath: "recipe-history"}))
		}, same, "RecipeOperations: @graphOperations(Recipe) adds the operation history to the namespace recipe, where RecipeQueries.history takes that name; rename that operation, or name the set for another namespace"},
		{"an MCP handle another tool takes", func() *ir.Schema {
			return recipesAPI(plain(), authored("Kitchen", &ir.FieldDef{Name: "cut", TypeRef: ir.TypeRef{Name: "string"}, Required: true, HTTPMethod: "POST", RestPath: "cut",
				MCP: &ir.OperationMCP{Handle: "recipe_branch"}}))
		}, same, `RecipeOperations: @graphOperations(Recipe) adds the MCP tool "recipe_branch", for Recipe's branch, and Kitchen.cut's @mcp takes that handle; rename that handle (D11)`},
		{"a route an authored operation matches", func() *ir.Schema {
			return recipesAPI(plain(), authored("Kitchen", &ir.FieldDef{Name: "readPart", TypeRef: ir.TypeRef{Name: "string"}, Required: true, HTTPMethod: "GET", RestPath: "recipes/{recipe}/{part}",
				Arguments: []*ir.ArgumentDef{{Name: "recipe", TypeRef: ir.TypeRef{Name: "string"}, Required: true}, {Name: "part", TypeRef: ir.TypeRef{Name: "string"}, Required: true}}}))
		}, same, "RecipeOperations: the operation released it adds is routed at GET /api/recipes/{id}/released, which Kitchen.readPart's GET /api/recipes/{recipe}/{part} also matches: move that route, or leave released out of the set's operations (D138)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api, db := tc.api(), tc.db()
			in := verify.Input{Registry: reg, Database: db}
			res := verify.CheckGraphOperations(api, in)
			var got []string
			for _, d := range res.Errors {
				got = append(got, d.Msg)
			}
			if !slices.Contains(got, tc.want) {
				t.Errorf("CheckGraphOperations = %q, want %q among them", got, tc.want)
			}
			// The pass reports the same refusal among its findings.
			full := verify.Run(api, in)
			found := false
			for _, d := range full.Errors {
				found = found || d.Msg == tc.want
			}
			if !found {
				t.Errorf("verify.Run does not report %q", tc.want)
			}
		})
	}

	// Without the database the root is not checked, and nothing else is
	// refused.
	if res := verify.CheckGraphOperations(recipesAPI(plain()), verify.Input{Registry: reg}); res.Err() != nil {
		t.Errorf("CheckGraphOperations without a database = %v", res.Err())
	}
	// The set's clause is admitted on it, as on a @behaviorOperations set.
	clause := plain()
	clause.Auth, clause.Permissions = true, []string{"recipes.write"}
	in := verify.Input{Registry: reg, Database: fixture}
	if err := verify.CheckBehaviorOperations(recipesAPI(clause), in).Err(); err != nil {
		t.Errorf("the set's clause on a @graphOperations set = %v", err)
	}
	// A clean API passes, and so does its expansion when checked again.
	api := recipesAPI(clause)
	if err := verify.CheckGraphOperations(api, in).Err(); err != nil {
		t.Fatalf("CheckGraphOperations = %v", err)
	}
	if _, err := graphops.Expand(api, fixture, reg); err != nil {
		t.Fatal(err)
	}
	if err := verify.CheckGraphOperations(api, in).Err(); err != nil {
		t.Errorf("CheckGraphOperations of the expanded API = %v", err)
	}
	first := marshal(t, api)
	if _, err := graphops.Expand(api, fixture, reg); err != nil {
		t.Fatal(err)
	}
	if again := marshal(t, api); again != first {
		t.Error("expanding an expanded API changed it")
	}
}

// TestGraphOperationsNarrowed: a set that lists operations serves those,
// in the loader's order whatever order it lists them in, and adds the
// types they name alone; a set that lists none over a graph with no
// string-ordered kind leaves arrange out; and a set whose @requireService
// admits only services hides each tool.
func TestGraphOperationsNarrowed(t *testing.T) {
	reg := registry.New(naming.Default())
	db := loadGraphDB(t)
	names := func(plan *graphops.Plan) []string {
		var out []string
		for _, op := range plan.Operations {
			out = append(out, op.Name)
		}
		return out
	}
	set := graphSet("RecipeReleases", "Recipe", func(c *ir.GraphOperationsConfig) { c.Operations = []string{"released", "release", "history"} })
	plan, err := graphops.PlanSet(set, db, reg)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(plan); !reflect.DeepEqual(got, []string{"history", "release", "released"}) {
		t.Errorf("operations = %v", got)
	}
	added := graphops.AddedNames(plan)
	if slices.Contains(added, "RecipeGraphEdits") || slices.Contains(added, "RecipeGraphConflict") || !slices.Contains(added, "RecipeGraphReleased") || !slices.Contains(added, "RecipeGraphStepRow") {
		t.Errorf("a set of history, release and released adds %v", added)
	}

	unordered := loadGraphDB(t)
	unordered.Types["Part"].GraphMember.Order = ""
	plan, err = graphops.PlanSet(graphSet("RecipeOperations", "Recipe", nil), unordered, reg)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(plan); slices.Contains(got, "arrange") || len(got) != len(ir.GraphOperationNames)-1 {
		t.Errorf("a graph with no string-ordered kind serves %v", got)
	}

	services := graphSet("RecipeOperations", "Recipe", nil)
	services.ServiceCallers = &ir.ServiceCallers{Mode: ir.ServiceCallersRequire}
	plan, err = graphops.PlanSet(services, db, reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range plan.Operations {
		if op.MCP == nil || !op.MCP.Hidden || op.MCP.Handle != "" || op.MCP.HiddenReason != "Only the services that @requireService on RecipeOperations admits call it (D37)." {
			t.Errorf("%s: mcp = %+v", op.Name, op.MCP)
		}
	}
}

// TestGraphOperationsRowsOfRelationsAndStructs: a member's field that is a
// to-one relation to a table is the row's member <field>Id, typed by the
// target's key, and one typed by a struct of the database is JSON, since
// the API declares neither type; the plan's members name the relation for
// the handlers, which hand the facade the object that holds the key.
func TestGraphOperationsRowsOfRelationsAndStructs(t *testing.T) {
	reg := registry.New(naming.Default())
	db := loadGraphDB(t)
	db.Types["NoteMeta"] = &ir.TypeDef{Name: "NoteMeta", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{{Name: "mood", TypeRef: ir.TypeRef{Name: "string"}, Required: true}}}
	note := db.Types["Note"]
	note.Fields = append(note.Fields,
		&ir.FieldDef{Name: "utensil", Comment: "The utensil the note is about.", TypeRef: ir.TypeRef{Name: "Utensil"}},
		&ir.FieldDef{Name: "meta", TypeRef: ir.TypeRef{Name: "NoteMeta"}, JsonField: true},
	)
	plan, err := graphops.PlanSet(graphSet("RecipeOperations", "Recipe", nil), db, reg)
	if err != nil {
		t.Fatal(err)
	}
	var row *ir.TypeDef
	for _, td := range plan.Types {
		if td.Name == "RecipeGraphNoteRow" {
			row = td
		}
	}
	if row == nil {
		t.Fatal("the plan has no RecipeGraphNoteRow")
	}
	var got []string
	for _, fd := range row.Fields {
		got = append(got, fd.Name+":"+fd.TypeRef.Name)
	}
	if want := []string{"entityKey:Identity.UUID", "replyTo:Identity.UUID", "body:string", "utensilId:Identity.UUID", "meta:Generic.JSON"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RecipeGraphNoteRow = %v, want %v", got, want)
	}
	for _, member := range plan.Members {
		if member.Type != "Note" {
			continue
		}
		if !reflect.DeepEqual(member.Fields, []string{"replyTo", "body", "utensilId", "meta"}) ||
			!reflect.DeepEqual(member.Relations, []graphops.Relation{{Member: "utensilId", Field: "utensil", Key: "id"}}) || member.Kind != "note" || member.Tree != "note" || member.Ordered {
			t.Errorf("the Note member = %+v", member)
		}
	}
	if !slices.Contains(plan.Scalars, "Generic.JSON") {
		t.Errorf("the plan's scalars = %v", plan.Scalars)
	}
}
