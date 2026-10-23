package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/registry/registrytest"
)

func runCommandWith(t *testing.T, exts []registry.Extension, args ...string) (string, error) {
	t.Helper()
	buf := new(bytes.Buffer)
	root := New(Config{}, exts...)
	root.SetOut(buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// A service whose type composes a behavior loads, prints its IR and
// converts between the data forms; build writes its types with
// ItemBehaviors, since every generator of the General pipeline renders
// behaviors (D58). The core binary refuses the behavior.
func TestBehaviorsThroughTheCommands(t *testing.T) {
	acme := []registry.Extension{registrytest.Acme{}}
	service := filepath.Join(acmeTestdata, "stock-json")

	out, err := runCommandWith(t, acme, "build", service, "--emit-ir", "--out", t.TempDir())
	require.NoError(t, err)
	var schema struct {
		Types map[string]struct {
			Behaviors []struct {
				Name   string          `json:"name"`
				Config json.RawMessage `json:"config"`
			} `json:"behaviors"`
		} `json:"types"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &schema))
	behaviors := schema.Types["Item"].Behaviors
	require.Len(t, behaviors, 2)
	assert.Equal(t, "acme.Stock", behaviors[0].Name)
	assert.JSONEq(t, `{"aisles": 3, "unit": "box"}`, string(behaviors[0].Config))
	assert.Equal(t, "acme.Audited", behaviors[1].Name)
	assert.Empty(t, behaviors[1].Config)

	buildDir := t.TempDir()
	_, err = runCommandWith(t, acme, "build", service, "--out", buildDir)
	require.NoError(t, err)
	behaviorsTS, err := os.ReadFile(filepath.Join(buildDir, "types", "typescript", "stock", "types", "behaviors.ts"))
	require.NoError(t, err)
	assert.Contains(t, string(behaviorsTS), "export interface ItemBehaviors {")

	_, err = runCommandWith(t, nil, "build", service, "--emit-ir", "--out", t.TempDir())
	require.ErrorContains(t, err, `behavior "acme.Stock" on type "Item" is not a registered behavior (registered: Approvals, Assignment, Blueprint, Branches, Budget, Calendar, Comments, Constants, Dependencies, Derived, Holds, Lease, Links, Numbering, Payments, Presence, Provenance, Queue, Reactions, Retries, Revisions, Rollups, Search, StateRules, Stock, Timers, Variants, Workflow)`)

	yamlOut, err := runCommandWith(t, acme, "format", "--to=yaml", "--stdout", filepath.Join(service, "src/item.schema.json"))
	require.NoError(t, err)
	assert.Contains(t, yamlOut, "behaviors:\n      - name: acme.Stock\n        config:\n          aisles: 3\n          unit: box\n      - name: acme.Audited\n")

	schemaOut, err := runCommandWith(t, acme, "json-schema")
	require.NoError(t, err)
	var def struct {
		Defs struct {
			BehaviorRef struct {
				Properties struct {
					Name struct {
						Enum []string `json:"enum"`
					} `json:"name"`
				} `json:"properties"`
			} `json:"BehaviorRef"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal([]byte(schemaOut), &def))
	assert.Equal(t, []string{"Approvals", "Assignment", "Blueprint", "Branches", "Budget", "Calendar", "Comments", "Constants", "Dependencies", "Derived", "Holds", "Lease", "Links", "Numbering", "Payments", "Presence", "Provenance", "Queue", "Reactions", "Retries", "Revisions", "Rollups", "Search", "StateRules", "Stock", "Timers", "Variants", "Workflow", "acme.Audited", "acme.Stock"}, def.Defs.BehaviorRef.Properties.Name.Enum)
	compileSchema(t, []byte(schemaOut), "superschematic://schema-file.json")
}

// The behaviors command writes a canonical copy of each registered
// declaration, one the registry takes back unchanged; --check fails on a
// copy that differs, is missing or has no behavior, and a write repairs
// all three.
func TestBehaviorsCommand(t *testing.T) {
	acme := []registry.Extension{registrytest.Acme{}}
	out := filepath.Join(t.TempDir(), "declarations")

	log, err := runCommandWith(t, acme, "behaviors", "--extension", "acme", "--out", out)
	require.NoError(t, err)
	assert.Equal(t, "behaviors: 2 declaration(s) in "+out+"\n", log)
	files, err := behaviorFiles(out)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"acme.Audited.behavior.json", "acme.Stock.behavior.json"}, sortedKeys(files))

	// The copy is canonical: the declaration's keys in order, schema keys
	// sorted, whatever the embedded file's layout.
	stock := string(files["acme.Stock.behavior.json"])
	assert.True(t, strings.HasPrefix(stock, "{\n  \"name\": \"acme.Stock\",\n  \"description\": \"Counts the units of an item on hand.\",\n  \"configSchema\": {\n    \"additionalProperties\": false,\n"), stock)
	assert.True(t, strings.HasSuffix(stock, "}\n"))
	assert.JSONEq(t, string(registrytest.StockBehavior), stock)
	assert.JSONEq(t, string(registrytest.AuditedBehavior), string(files["acme.Audited.behavior.json"]))

	// The registry takes the copy as the declaration it was made from.
	reg := registry.New(naming.Default())
	require.NoError(t, reg.RegisterBehavior(registry.BehaviorSpec{Extension: "acme", Declaration: files["acme.Stock.behavior.json"]}))
	copied, ok := reg.Behavior("acme.Stock")
	require.True(t, ok)
	written, err := behaviorDeclarationFile(copied.BehaviorDeclaration)
	require.NoError(t, err)
	assert.Equal(t, stock, string(written))

	log, err = runCommandWith(t, acme, "behaviors", "--extension", "acme", "--out", out, "--check")
	require.NoError(t, err)
	assert.Equal(t, "behaviors: 2 declaration(s) in "+out+" are current\n", log)

	require.NoError(t, os.WriteFile(filepath.Join(out, "acme.Stock.behavior.json"), registrytest.StockBehavior, 0o644))
	require.NoError(t, os.Remove(filepath.Join(out, "acme.Audited.behavior.json")))
	require.NoError(t, os.WriteFile(filepath.Join(out, "acme.Gone.behavior.json"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(out, "README.md"), []byte("not a declaration\n"), 0o644))
	_, err = runCommandWith(t, acme, "behaviors", "--extension", "acme", "--out", out, "--check")
	require.EqualError(t, err, "behaviors: "+out+": acme.Audited.behavior.json is missing; acme.Stock.behavior.json differs from the registered declaration; "+
		"acme.Gone.behavior.json is no registered behavior's declaration; run: superschematic behaviors --out "+out+" --extension acme")

	_, err = runCommandWith(t, acme, "behaviors", "--extension", "acme", "--out", out)
	require.NoError(t, err)
	repaired, err := behaviorFiles(out)
	require.NoError(t, err)
	assert.Equal(t, files, repaired)
	_, err = os.Stat(filepath.Join(out, "README.md"))
	require.NoError(t, err, "only *.behavior.json files are the command's")
	_, err = runCommandWith(t, acme, "behaviors", "--extension", "acme", "--out", out, "--check")
	require.NoError(t, err)
}

// An operation's scope sits after writes in the canonical copy, and an
// operation that gives none keeps none: the default, an instance, is not
// written out.
func TestBehaviorDeclarationFileKeepsScope(t *testing.T) {
	params := json.RawMessage(`{"type":"object","additionalProperties":false}`)
	written, err := behaviorDeclarationFile(registry.BehaviorDeclaration{
		Name: "acme.Rating",
		Operations: []registry.BehaviorOperation{
			{Name: "rate", ParamsSchema: params, ResultSchema: json.RawMessage(`true`), Writes: true},
			{Name: "topRated", ParamsSchema: params, ResultSchema: json.RawMessage(`true`), Writes: true, Scope: registry.OperationScopeSchema, InvocationPolicy: "ask"},
		},
	})
	require.NoError(t, err)
	assert.Contains(t, string(written), "\"resultSchema\": true,\n      \"writes\": true\n    },")
	assert.Contains(t, string(written), "\"writes\": true,\n      \"scope\": \"schema\",\n      \"invocationPolicy\": \"ask\"\n")

	reg := registry.New(naming.Default())
	require.NoError(t, reg.RegisterBehavior(registry.BehaviorSpec{Extension: "acme", Declaration: written}))
	copied, ok := reg.Behavior("acme.Rating")
	require.True(t, ok)
	assert.Equal(t, []string{"", registry.OperationScopeSchema}, []string{copied.Operations[0].Scope, copied.Operations[1].Scope})
}

// --extension keeps one extension's behaviors, and names what the binary
// registers when that extension registers none; the core binary writes the
// core's declarations.
func TestBehaviorsCommand_Extension(t *testing.T) {
	acme := []registry.Extension{registrytest.Acme{}}
	out := t.TempDir()
	_, err := runCommandWith(t, acme, "behaviors", "--out", out, "--extension", "acme")
	require.NoError(t, err)
	files, err := behaviorFiles(out)
	require.NoError(t, err)
	assert.Len(t, files, 2)

	_, err = runCommandWith(t, acme, "behaviors", "--out", out, "--extension", "shop", "--check")
	require.EqualError(t, err, `behaviors: extension "shop" registers no behavior in this binary (registered: Approvals (core), Assignment (core), Blueprint (core), Branches (core), Budget (core), Calendar (core), Comments (core), Constants (core), Dependencies (core), Derived (core), Holds (core), Lease (core), Links (core), Numbering (core), Payments (core), Presence (core), Provenance (core), Queue (core), Reactions (core), Retries (core), Revisions (core), Rollups (core), Search (core), StateRules (core), Stock (core), Timers (core), Variants (core), Workflow (core), acme.Audited (extension acme), acme.Stock (extension acme))`)

	_, err = runCommandWith(t, acme, "behaviors", "--out", out, "--extension", "acme", "--check")
	require.NoError(t, err)

	core := filepath.Join(t.TempDir(), "core")
	log, err := runCommandWith(t, nil, "behaviors", "--out", core)
	require.NoError(t, err)
	assert.Equal(t, "behaviors: 28 declaration(s) in "+core+"\n", log)
	written, err := behaviorFiles(core)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"Approvals.behavior.json", "Assignment.behavior.json", "Blueprint.behavior.json", "Branches.behavior.json", "Budget.behavior.json", "Calendar.behavior.json", "Comments.behavior.json", "Constants.behavior.json", "Dependencies.behavior.json", "Derived.behavior.json", "Holds.behavior.json", "Lease.behavior.json",
		"Links.behavior.json", "Numbering.behavior.json", "Payments.behavior.json", "Presence.behavior.json", "Provenance.behavior.json", "Queue.behavior.json", "Reactions.behavior.json", "Retries.behavior.json", "Revisions.behavior.json", "Rollups.behavior.json",
		"Search.behavior.json", "StateRules.behavior.json", "Stock.behavior.json", "Timers.behavior.json", "Variants.behavior.json", "Workflow.behavior.json"}, sortedKeys(written))
	_, err = runCommandWith(t, nil, "behaviors", "--out", out, "--check")
	require.ErrorContains(t, err, "acme.Audited.behavior.json is no registered behavior's declaration")

	_, err = runCommandWith(t, nil, "behaviors")
	require.ErrorContains(t, err, `required flag(s) "out" not set`)
}

// --package keeps the behaviors one npm package implements: the core's
// split between the engine, the work-queue package and the commerce
// package, each of which gets only its own, and --check holds each
// directory to its package. With
// --extension too, a behavior must match both. A package that implements
// nothing the binary registers is refused, naming the packages there are.
func TestBehaviorsCommand_Package(t *testing.T) {
	engine := filepath.Join(t.TempDir(), "engine")
	log, err := runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine", "--out", engine)
	require.NoError(t, err)
	assert.Equal(t, "behaviors: 17 declaration(s) in "+engine+"\n", log)
	files, err := behaviorFiles(engine)
	require.NoError(t, err)
	assert.Equal(t, []string{"Approvals.behavior.json", "Branches.behavior.json", "Comments.behavior.json", "Constants.behavior.json", "Dependencies.behavior.json", "Derived.behavior.json", "Links.behavior.json", "Numbering.behavior.json",
		"Provenance.behavior.json", "Reactions.behavior.json", "Revisions.behavior.json", "Rollups.behavior.json", "Search.behavior.json", "StateRules.behavior.json", "Timers.behavior.json", "Variants.behavior.json", "Workflow.behavior.json"}, sortedKeys(files))

	workqueue := filepath.Join(t.TempDir(), "workqueue")
	_, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine-workqueue", "--out", workqueue)
	require.NoError(t, err)
	files, err = behaviorFiles(workqueue)
	require.NoError(t, err)
	assert.Equal(t, []string{"Assignment.behavior.json", "Blueprint.behavior.json", "Budget.behavior.json", "Lease.behavior.json", "Presence.behavior.json", "Queue.behavior.json",
		"Retries.behavior.json"}, sortedKeys(files))

	commerce := filepath.Join(t.TempDir(), "commerce")
	log, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine-commerce", "--out", commerce)
	require.NoError(t, err)
	assert.Equal(t, "behaviors: 4 declaration(s) in "+commerce+"\n", log)
	files, err = behaviorFiles(commerce)
	require.NoError(t, err)
	assert.Equal(t, []string{"Calendar.behavior.json", "Holds.behavior.json", "Payments.behavior.json", "Stock.behavior.json"}, sortedKeys(files))
	_, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine-commerce", "--out", commerce, "--check")
	require.NoError(t, err)

	// Each directory is current for its own package and stale for the other.
	_, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine-workqueue", "--out", workqueue, "--check")
	require.NoError(t, err)
	_, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine", "--out", workqueue, "--check")
	require.EqualError(t, err, "behaviors: "+workqueue+": Approvals.behavior.json is missing; Branches.behavior.json is missing; Comments.behavior.json is missing; Constants.behavior.json is missing; Dependencies.behavior.json is missing; Derived.behavior.json is missing; Links.behavior.json is missing; "+
		"Numbering.behavior.json is missing; Provenance.behavior.json is missing; Reactions.behavior.json is missing; Revisions.behavior.json is missing; Rollups.behavior.json is missing; Search.behavior.json is missing; "+
		"StateRules.behavior.json is missing; Timers.behavior.json is missing; Variants.behavior.json is missing; Workflow.behavior.json is missing; Assignment.behavior.json is no registered behavior's declaration; Blueprint.behavior.json is no registered behavior's declaration; "+
		"Budget.behavior.json is no registered behavior's declaration; Lease.behavior.json is no registered behavior's declaration; "+
		"Presence.behavior.json is no registered behavior's declaration; Queue.behavior.json is no registered behavior's declaration; "+
		"Retries.behavior.json is no registered behavior's declaration; run: superschematic behaviors --out "+workqueue+" --package @superschematic/engine")

	_, err = runCommandWith(t, nil, "behaviors", "--package", "@acme/behaviors", "--out", t.TempDir())
	require.EqualError(t, err, `behaviors: no behavior this binary registers is implemented by package "@acme/behaviors" (packages: @superschematic/engine, @superschematic/engine-commerce, @superschematic/engine-workqueue)`)
	acme := []registry.Extension{registrytest.Acme{}}
	_, err = runCommandWith(t, acme, "behaviors", "--extension", "acme", "--package", "@superschematic/engine", "--out", t.TempDir())
	require.EqualError(t, err, `behaviors: no behavior this binary registers for extension acme is implemented by package "@superschematic/engine" (packages: @superschematic/engine, @superschematic/engine-commerce, @superschematic/engine-workqueue)`)
}

// D10 for behaviors: the binary with no extension linked loads a schema
// that composes the core's behaviors, in the data form an engine reads and
// in TypeScript, to the same IR; refuses a config the declaration's schema
// rejects; admits exactly the core's names in json-schema; and writes the
// copies @superschematic/engine and @superschematic/engine-workqueue
// carry, which are current.
func TestCoreBehaviorsWithNoExtension(t *testing.T) {
	type ir struct {
		Types map[string]struct {
			Behaviors []struct {
				Name   string          `json:"name"`
				Config json.RawMessage `json:"config"`
			} `json:"behaviors"`
		} `json:"types"`
	}
	load := func(service string) ir {
		t.Helper()
		out, err := runCommandWith(t, nil, "build", service, "--emit-ir", "--out", t.TempDir())
		require.NoError(t, err)
		var schema ir
		require.NoError(t, json.Unmarshal([]byte(out), &schema))
		return schema
	}
	fromJSON := load(filepath.Join(loaderTestdata, "fixture-behaviors-json"))
	behaviors := fromJSON.Types["Document"].Behaviors
	require.Len(t, behaviors, 3)
	assert.Equal(t, "Workflow", behaviors[0].Name)
	assert.JSONEq(t, `{"states": ["draft", "review", "published", "archived"], "transitions": [
		{"from": "draft", "to": "review"}, {"from": "review", "to": "draft"},
		{"from": "review", "to": "published", "permission": "documents.publish"}, {"from": "published", "to": "archived"}]}`, string(behaviors[0].Config))
	assert.Equal(t, "Comments", behaviors[1].Name)
	assert.Empty(t, behaviors[1].Config)
	assert.Equal(t, "Revisions", behaviors[2].Name)
	assert.JSONEq(t, `{"review": {"permission": "documents.review"}}`, string(behaviors[2].Config))
	assert.Equal(t, fromJSON.Types["Document"], load(filepath.Join(tsreaderTestdata, "fixture-behaviors")).Types["Document"])

	_, err := runCommandWith(t, nil, "build", filepath.Join(tsreaderTestdata, "broken-behavior-config"), "--emit-ir", "--out", t.TempDir())
	require.ErrorContains(t, err, "type Memo: behavior Workflow config: ")
	_, err = runCommandWith(t, nil, "build", filepath.Join(loaderTestdata, "fixture-behaviors-json"), "--out", t.TempDir())
	require.NoError(t, err)

	// A DB service's engine types build: sql and orm leave them out and
	// the enginedoc generator writes their engine document (D58).
	dist := t.TempDir()
	_, err = runCommandWith(t, nil, "build", filepath.Join(loaderTestdata, "fixture-engine-db-json"), "--out", dist)
	require.NoError(t, err)
	engineDocument, err := os.ReadFile(filepath.Join(dist, "types", "go", "fixture-engine-db", "engine", "fixture-engine-db.json"))
	require.NoError(t, err)
	assert.Contains(t, string(engineDocument), `"namespace": "fixture-engine-db"`)
	createSQL, err := os.ReadFile(filepath.Join(dist, "sql", "fixture-engine-db", "create.sql"))
	require.NoError(t, err)
	assert.NotContains(t, string(createSQL), "ticket")

	schemaOut, err := runCommandWith(t, nil, "json-schema")
	require.NoError(t, err)
	var def struct {
		Defs struct {
			BehaviorRef struct {
				Properties struct {
					Name struct {
						Enum []string `json:"enum"`
					} `json:"name"`
				} `json:"properties"`
			} `json:"BehaviorRef"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal([]byte(schemaOut), &def))
	assert.Equal(t, []string{"Approvals", "Assignment", "Blueprint", "Branches", "Budget", "Calendar", "Comments", "Constants", "Dependencies", "Derived", "Holds", "Lease", "Links", "Numbering", "Payments", "Presence", "Provenance", "Queue", "Reactions", "Retries", "Revisions", "Rollups", "Search", "StateRules", "Stock", "Timers", "Variants", "Workflow"}, def.Defs.BehaviorRef.Properties.Name.Enum)

	// Dependencies and Links, whose configs name other schemas, load in both
	// forms to the same IR; the loader resolves none of those names.
	crossJSON := load(filepath.Join(loaderTestdata, "fixture-cross-instance-json")).Types["Task"]
	require.Len(t, crossJSON.Behaviors, 3)
	assert.Equal(t, []string{"Workflow", "Dependencies", "Links"}, []string{crossJSON.Behaviors[0].Name, crossJSON.Behaviors[1].Name, crossJSON.Behaviors[2].Name})
	assert.JSONEq(t, `{"schemas": ["tasks", "documents"], "gatedStates": ["done"]}`, string(crossJSON.Behaviors[1].Config))
	assert.JSONEq(t, `{"links": {"spec": {"schema": "documents", "pinned": true}, "parent": {"schema": "tasks"}, "project": {"schema": "projects", "required": true}}}`, string(crossJSON.Behaviors[2].Config))
	assert.Equal(t, crossJSON, load(filepath.Join(tsreaderTestdata, "fixture-cross-instance")).Types["Task"])

	// Rollups, whose config names the tasks' project link, loads in both
	// forms to the same IR; the loader resolves neither the schema nor the
	// link, which the engine checks when the schema is defined.
	rollupsJSON := load(filepath.Join(loaderTestdata, "fixture-rollups-json")).Types["Project"]
	require.Len(t, rollupsJSON.Behaviors, 2)
	assert.Equal(t, []string{"Workflow", "Rollups"}, []string{rollupsJSON.Behaviors[0].Name, rollupsJSON.Behaviors[1].Name})
	assert.JSONEq(t, `{"rollups": {
		"tasks": {"schema": "tasks", "link": "project", "function": "count"},
		"tasksByStatus": {"schema": "tasks", "link": "project", "function": "countBy", "field": "Workflow.status"},
		"tasksFinished": {"schema": "tasks", "link": "project", "function": "all", "gatedStates": ["done"]}}}`, string(rollupsJSON.Behaviors[1].Config))
	assert.Equal(t, rollupsJSON, load(filepath.Join(tsreaderTestdata, "fixture-rollups")).Types["Project"])

	// Search, whose config names the type's own fields, loads in both forms
	// to the same IR; the engine checks the names and their types.
	searchJSON := load(filepath.Join(loaderTestdata, "fixture-search-json")).Types["Note"]
	require.Len(t, searchJSON.Behaviors, 1)
	assert.Equal(t, "Search", searchJSON.Behaviors[0].Name)
	assert.JSONEq(t, `{"fields": ["title", "body"], "weights": {"title": 3},
		"vectors": {"dimensions": 384, "model": "minilm-l6", "permission": "notes.embed"}}`, string(searchJSON.Behaviors[0].Config))
	assert.Equal(t, searchJSON, load(filepath.Join(tsreaderTestdata, "fixture-search")).Types["Note"])

	// Reactions, whose rules name a link, a schema and outcomes of a
	// Workflow's terminal states, loads in both forms to the same IR.
	reactionsJSON := load(filepath.Join(loaderTestdata, "fixture-reactions-json")).Types["Project"]
	require.Len(t, reactionsJSON.Behaviors, 3)
	assert.Equal(t, "Reactions", reactionsJSON.Behaviors[2].Name)
	assert.JSONEq(t, `{"states": ["todo", "doing", "done", "failed"], "transitions": [{"from": "todo", "to": "doing"}, {"from": "doing", "to": "done"}, {"from": "doing", "to": "failed"}],
		"outcomes": {"failed": "failure"}}`, string(reactionsJSON.Behaviors[0].Config))
	assert.JSONEq(t, `{"rules": [{"when": {"enters": "doing"}, "then": {"link": "parent", "transition": "doing"}},
		{"when": {"allTerminal": {"schema": "projects", "link": "parent", "outcomes": ["success"]}}, "then": {"transition": "done"}},
		{"when": {"anyTerminal": {"schema": "projects", "link": "parent", "outcomes": ["failure"]}}, "then": {"transition": "failed"}}]}`, string(reactionsJSON.Behaviors[2].Config))
	assert.Equal(t, reactionsJSON, load(filepath.Join(tsreaderTestdata, "fixture-reactions")).Types["Project"])

	// Constants and Variants, whose configs name the type's fields and the
	// document's other types, load in both forms to the same IR; the engine
	// checks those names, and the types, when the schema is defined.
	variantsJSON := load(filepath.Join(loaderTestdata, "fixture-variants-json")).Types
	variantsTS := load(filepath.Join(tsreaderTestdata, "fixture-variants")).Types
	require.Len(t, variantsJSON["Step"].Behaviors, 2)
	assert.JSONEq(t, `{"fields": ["kind"]}`, string(variantsJSON["Step"].Behaviors[0].Config))
	assert.JSONEq(t, `{"field": "result", "by": "kind", "types": {"verify": "VerifyResult", "review": "ReviewResult"}}`, string(variantsJSON["Step"].Behaviors[1].Config))
	for _, name := range []string{"Step", "VerifyResult", "Check", "ReviewResult"} {
		assert.Equal(t, variantsJSON[name], variantsTS[name], name)
	}

	// Branches, whose config names the document's other types and their
	// fields, loads in both forms to the same IR; the engine checks those
	// names, and derives the graph's descriptor from them, when the schema
	// is defined.
	branchesJSON := load(filepath.Join(loaderTestdata, "fixture-branches-json")).Types
	branchesTS := load(filepath.Join(tsreaderTestdata, "fixture-branches")).Types
	require.Len(t, branchesJSON["Recipe"].Behaviors, 1)
	assert.JSONEq(t, `{"kinds": {"step": {"type": "Step", "order": "position", "units": {"timings": "keyed"}, "retentionDays": 365},
		"ingredient": {"type": "Ingredient", "parent": {"key": "stepKey", "of": "step"}}, "cover": {"type": "Cover", "singleton": true}},
		"sweep": {"intervalMs": 3600000, "abandonAfter": 2592000000}}`, string(branchesJSON["Recipe"].Behaviors[0].Config))
	for _, name := range []string{"Recipe", "Step", "Ingredient", "Cover"} {
		assert.Equal(t, branchesJSON[name], branchesTS[name], name)
	}

	// StateRules, whose config names states of the type's Workflow and the
	// type's own fields, loads in both forms to the same IR; the engine
	// checks those names when the schema is defined.
	stateRulesJSON := load(filepath.Join(loaderTestdata, "fixture-staterules-json")).Types["Invoice"]
	require.Len(t, stateRulesJSON.Behaviors, 2)
	assert.Equal(t, []string{"Workflow", "StateRules"}, []string{stateRulesJSON.Behaviors[0].Name, stateRulesJSON.Behaviors[1].Name})
	assert.JSONEq(t, `{"states": {"sent": {"required": ["customer", "amount", "lines"], "frozen": ["customer", "amount", "lines"]},
		"paid": {"frozen": "all"}, "void": {"frozen": "all"}}, "permission": "invoices.amend"}`, string(stateRulesJSON.Behaviors[1].Config))
	assert.Equal(t, stateRulesJSON, load(filepath.Join(tsreaderTestdata, "fixture-staterules")).Types["Invoice"])

	// Approvals, whose gates name the type's Workflow states, loads in both
	// forms to the same IR; the engine checks those states when the schema
	// is defined.
	approvalsJSON := load(filepath.Join(loaderTestdata, "fixture-approvals-json")).Types["Expense"]
	require.Len(t, approvalsJSON.Behaviors, 2)
	assert.Equal(t, []string{"Workflow", "Approvals"}, []string{approvalsJSON.Behaviors[0].Name, approvalsJSON.Behaviors[1].Name})
	assert.JSONEq(t, `{"gates": {"approved": {"count": 2, "permission": "expenses.approve"}, "paid": {"count": 1, "permission": "expenses.pay", "from": ["approved"]}}}`,
		string(approvalsJSON.Behaviors[1].Config))
	assert.Equal(t, approvalsJSON, load(filepath.Join(tsreaderTestdata, "fixture-approvals")).Types["Expense"])

	// Numbering, whose config names a link of the type's Links config,
	// loads in both forms to the same IR; the engine checks its format and
	// the link when the schema is defined.
	numberingJSON := load(filepath.Join(loaderTestdata, "fixture-numbering-json")).Types["Invoice"]
	require.Len(t, numberingJSON.Behaviors, 2)
	assert.Equal(t, []string{"Links", "Numbering"}, []string{numberingJSON.Behaviors[0].Name, numberingJSON.Behaviors[1].Name})
	assert.JSONEq(t, `{"format": "INV-{yyyy}-{seq:4}", "scope": {"link": "project"}, "reset": "yearly"}`, string(numberingJSON.Behaviors[1].Config))
	assert.Equal(t, numberingJSON, load(filepath.Join(tsreaderTestdata, "fixture-numbering")).Types["Invoice"])

	// Timers, whose deadlines name the type's Workflow states and a
	// date-time field, loads in both forms to the same IR; the engine checks
	// those names, and the field's format, when the schema is defined.
	timersJSON := load(filepath.Join(loaderTestdata, "fixture-timers-json")).Types["Request"]
	require.Len(t, timersJSON.Behaviors, 2)
	assert.Equal(t, []string{"Workflow", "Timers"}, []string{timersJSON.Behaviors[0].Name, timersJSON.Behaviors[1].Name})
	assert.JSONEq(t, `{"deadlines": {"respond": {"start": {"enters": "open"}, "afterMs": 14400000, "then": {"transition": "escalated"}, "until": {"leaves": "open"}},
		"lapse": {"start": {"enters": "waiting"}, "afterMs": 604800000, "then": {"transition": "resolved"}, "until": {"leaves": "waiting"}},
		"remind": {"start": {"field": "visitAt"}, "afterMs": -86400000, "until": {"enters": ["resolved"]}}},
		"sweepMs": 60000, "permission": "requests.timers"}`, string(timersJSON.Behaviors[1].Config))
	assert.Equal(t, timersJSON, load(filepath.Join(tsreaderTestdata, "fixture-timers")).Types["Request"])

	// Lease, Assignment, Queue, Budget and Retries, whose configs name the
	// type's fields and Workflow states, load in both forms to the same IR;
	// the engine checks those names when the schema is defined.
	jobsJSON := load(filepath.Join(loaderTestdata, "fixture-workqueue-json")).Types["Job"]
	require.Len(t, jobsJSON.Behaviors, 6)
	jobBehaviors := make([]string, 0, len(jobsJSON.Behaviors))
	for _, b := range jobsJSON.Behaviors {
		jobBehaviors = append(jobBehaviors, b.Name)
	}
	assert.Equal(t, []string{"Workflow", "Lease", "Assignment", "Queue", "Budget", "Retries"}, jobBehaviors)
	assert.JSONEq(t, `{"ttlMs": 30000, "maxHoldField": "timeLimitMs", "onExpiry": {"transition": "queued", "from": ["running"]}, "maxExpiries": 3,
		"escalate": {"transition": "failed", "from": ["running"]}, "overridePermission": "jobs.override"}`, string(jobsJSON.Behaviors[1].Config))
	assert.JSONEq(t, `{"permission": "jobs.assign"}`, string(jobsJSON.Behaviors[2].Config))
	assert.JSONEq(t, `{"claim": {"from": ["queued"], "to": "running"}, "priorityField": "priority", "match": ["topic"]}`, string(jobsJSON.Behaviors[3].Config))
	assert.JSONEq(t, `{"meters": {"cpuSeconds": {"limit": 3600, "reserve": 600, "reset": "daily"}}, "limitPermission": "jobs.budget"}`, string(jobsJSON.Behaviors[4].Config))
	assert.JSONEq(t, `{"classes": {"timeout": {"attempts": 3}, "invalidOutput": {"attempts": 2}, "rejected": "terminal"}, "totalAttempts": 4,
		"exhaustedState": "failed"}`, string(jobsJSON.Behaviors[5].Config))
	assert.Equal(t, jobsJSON, load(filepath.Join(tsreaderTestdata, "fixture-workqueue")).Types["Job"])

	// Presence and Blueprint, whose configs name the type's fields and
	// Workflow states, and other schemas and their links, load in both forms
	// to the same IR, each type from its own file.
	workqueueJSON := load(filepath.Join(loaderTestdata, "fixture-workqueue-json")).Types
	workqueueTS := load(filepath.Join(tsreaderTestdata, "fixture-workqueue")).Types
	require.Len(t, workqueueJSON["Worker"].Behaviors, 2)
	assert.JSONEq(t, `{"ttlMs": 30000, "principalField": "subject", "onMissed": {"transition": "missing", "from": ["idle", "busy"]},
		"onBeat": {"transition": "idle", "from": ["missing"]}, "releaseLeases": ["jobs"], "sweepMs": 5000}`, string(workqueueJSON["Worker"].Behaviors[1].Config))
	require.Len(t, workqueueJSON["Batch"].Behaviors, 1)
	assert.JSONEq(t, `{"schema": "steps", "parentLink": "batch", "keyField": "step", "copyFields": ["topic"], "steps": {"fetch": {},
		"check": {"after": ["fetch"], "when": {"field": "topic", "equals": "search"}}, "index": {"after": ["check"], "data": {"title": "Index what was fetched"}}}}`,
		string(workqueueJSON["Batch"].Behaviors[0].Config))
	for _, name := range []string{"Worker", "Batch", "Step"} {
		assert.Equal(t, workqueueJSON[name], workqueueTS[name], name)
	}

	// Stock, Calendar, Holds and Payments (D70), whose configs name the
	// type's fields, Workflow states and links, and other schemas, load in
	// both forms to the same IR, each type from its own file but the
	// checkout's line, beside the checkout.
	commerceJSON := load(filepath.Join(loaderTestdata, "fixture-commerce-json")).Types
	commerceTS := load(filepath.Join(tsreaderTestdata, "fixture-commerce")).Types
	behaviorNames := func(name string) []string {
		var names []string
		for _, b := range commerceJSON[name].Behaviors {
			names = append(names, b.Name)
		}
		return names
	}
	assert.Equal(t, []string{"Stock"}, behaviorNames("Inventory"))
	assert.Equal(t, []string{"Calendar"}, behaviorNames("CollectionPoint"))
	assert.Equal(t, []string{"Workflow", "Links", "Holds", "Timers", "Payments", "Reactions", "Numbering", "StateRules"}, behaviorNames("Checkout"))
	assert.Equal(t, []string{"Workflow", "Approvals", "Links", "Numbering", "StateRules", "Payments", "Reactions"}, behaviorNames("Refund"))
	assert.JSONEq(t, `{"price": {"field": "priceCents", "currency": "GBP"}, "lowAt": {"field": "reorderAt"}, "maxPerHold": 99, "backorder": false,
		"permission": "stock.adjust"}`, string(commerceJSON["Inventory"].Behaviors[0].Config))
	assert.JSONEq(t, `{"holds": {"lines": {"each": {"field": "lines", "target": "inventoryId"}, "schema": "Inventory", "quantity": {"field": "quantity"}},
		"pickup": {"link": "counter", "interval": {"start": "pickupAt", "durationMs": 900000}}},
		"takeOn": ["held"], "commitOn": ["placed"], "releaseOn": ["expired", "cancelled"], "maxItems": 50}`, string(commerceJSON["Checkout"].Behaviors[2].Config))
	assert.JSONEq(t, `{"amount": {"field": "amountCents"}, "currency": "GBP", "refundOf": {"link": "order"}, "issueOn": ["approved"],
		"record": {"permission": "payments.record"}}`, string(commerceJSON["Refund"].Behaviors[5].Config))
	for _, name := range []string{"Inventory", "CollectionPoint", "Checkout", "CheckoutLine", "Refund"} {
		assert.Equal(t, commerceJSON[name], commerceTS[name], name)
	}

	_, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine",
		"--out", filepath.Join("..", "runtime", "engine", "rust", "behaviors", "declarations"), "--check")
	require.NoError(t, err, "the engine's copies of the core declarations are stale; run make behaviors")
	_, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine-workqueue",
		"--out", filepath.Join("..", "runtime", "engine-workqueue", "rust", "declarations"), "--check")
	require.NoError(t, err, "the work queue's copies of the core declarations are stale; run make behaviors")
	_, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine-commerce",
		"--out", filepath.Join("..", "runtime", "engine-commerce", "rust", "declarations"), "--check")
	require.NoError(t, err, "the commerce crate's copies of the core declarations are stale; run make behaviors")
}

// Stock and Payments read a currency from a Finance.Currency own field
// (D70): the fixture loads in both forms to the same IR.
func TestCommerceCurrencyFields(t *testing.T) {
	if _, ok := registry.CoreScalars().Scalar("Finance.Currency"); !ok {
		t.Fatal("this build's superscalar has no Finance.Currency; superscalar.pin names the commit that added it")
	}
	load := func(service string) map[string]json.RawMessage {
		t.Helper()
		out, err := runCommandWith(t, nil, "build", service, "--emit-ir", "--out", t.TempDir())
		require.NoError(t, err)
		var schema struct {
			Types map[string]json.RawMessage `json:"types"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &schema))
		return schema.Types
	}
	fromJSON := load(filepath.Join(loaderTestdata, "fixture-commerce-currency-json"))
	fromTS := load(filepath.Join(tsreaderTestdata, "fixture-commerce-currency"))
	for _, name := range []string{"Product", "Invoice"} {
		var jsonType, tsType map[string]any
		require.NoError(t, json.Unmarshal(fromJSON[name], &jsonType))
		require.NoError(t, json.Unmarshal(fromTS[name], &tsType))
		delete(jsonType, "owner")
		delete(tsType, "owner")
		assert.Equal(t, jsonType, tsType, name)
		assert.Contains(t, jsonType["fields"], map[string]any{"name": "currency", "typeRef": map[string]any{"name": "Finance.Currency"}, "required": true}, name)
	}
}
