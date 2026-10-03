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
// converts between the data forms; build refuses it, naming the generator
// that cannot render the behavior. The core binary refuses the behavior.
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

	_, err = runCommandWith(t, acme, "build", service, "--out", t.TempDir())
	require.ErrorContains(t, err, "generator: types does not render behaviors yet: type Item composes behavior acme.Stock")

	_, err = runCommandWith(t, nil, "build", service, "--emit-ir", "--out", t.TempDir())
	require.ErrorContains(t, err, `behavior "acme.Stock" on type "Item" is not a registered behavior (registered: Assignment, Blueprint, Budget, Comments, Dependencies, Lease, Links, Presence, Queue, Reactions, Retries, Revisions, Rollups, Search, Workflow)`)

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
	assert.Equal(t, []string{"Assignment", "Blueprint", "Budget", "Comments", "Dependencies", "Lease", "Links", "Presence", "Queue", "Reactions", "Retries", "Revisions", "Rollups", "Search", "Workflow", "acme.Audited", "acme.Stock"}, def.Defs.BehaviorRef.Properties.Name.Enum)
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
	require.EqualError(t, err, `behaviors: extension "shop" registers no behavior in this binary (registered: Assignment (core), Blueprint (core), Budget (core), Comments (core), Dependencies (core), Lease (core), Links (core), Presence (core), Queue (core), Reactions (core), Retries (core), Revisions (core), Rollups (core), Search (core), Workflow (core), acme.Audited (extension acme), acme.Stock (extension acme))`)

	_, err = runCommandWith(t, acme, "behaviors", "--out", out, "--extension", "acme", "--check")
	require.NoError(t, err)

	core := filepath.Join(t.TempDir(), "core")
	log, err := runCommandWith(t, nil, "behaviors", "--out", core)
	require.NoError(t, err)
	assert.Equal(t, "behaviors: 15 declaration(s) in "+core+"\n", log)
	written, err := behaviorFiles(core)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"Assignment.behavior.json", "Blueprint.behavior.json", "Budget.behavior.json", "Comments.behavior.json", "Dependencies.behavior.json", "Lease.behavior.json",
		"Links.behavior.json", "Presence.behavior.json", "Queue.behavior.json", "Reactions.behavior.json", "Retries.behavior.json", "Revisions.behavior.json", "Rollups.behavior.json",
		"Search.behavior.json", "Workflow.behavior.json"}, sortedKeys(written))
	_, err = runCommandWith(t, nil, "behaviors", "--out", out, "--check")
	require.ErrorContains(t, err, "acme.Audited.behavior.json is no registered behavior's declaration")

	_, err = runCommandWith(t, nil, "behaviors")
	require.ErrorContains(t, err, `required flag(s) "out" not set`)
}

// --package keeps the behaviors one npm package implements: the core's
// split between the engine and the work-queue package, each of which gets
// only its own, and --check holds each directory to its package. With
// --extension too, a behavior must match both. A package that implements
// nothing the binary registers is refused, naming the packages there are.
func TestBehaviorsCommand_Package(t *testing.T) {
	engine := filepath.Join(t.TempDir(), "engine")
	log, err := runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine", "--out", engine)
	require.NoError(t, err)
	assert.Equal(t, "behaviors: 8 declaration(s) in "+engine+"\n", log)
	files, err := behaviorFiles(engine)
	require.NoError(t, err)
	assert.Equal(t, []string{"Comments.behavior.json", "Dependencies.behavior.json", "Links.behavior.json", "Reactions.behavior.json",
		"Revisions.behavior.json", "Rollups.behavior.json", "Search.behavior.json", "Workflow.behavior.json"}, sortedKeys(files))

	workqueue := filepath.Join(t.TempDir(), "workqueue")
	_, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine-workqueue", "--out", workqueue)
	require.NoError(t, err)
	files, err = behaviorFiles(workqueue)
	require.NoError(t, err)
	assert.Equal(t, []string{"Assignment.behavior.json", "Blueprint.behavior.json", "Budget.behavior.json", "Lease.behavior.json", "Presence.behavior.json", "Queue.behavior.json",
		"Retries.behavior.json"}, sortedKeys(files))

	// Each directory is current for its own package and stale for the other.
	_, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine-workqueue", "--out", workqueue, "--check")
	require.NoError(t, err)
	_, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine", "--out", workqueue, "--check")
	require.EqualError(t, err, "behaviors: "+workqueue+": Comments.behavior.json is missing; Dependencies.behavior.json is missing; Links.behavior.json is missing; "+
		"Reactions.behavior.json is missing; Revisions.behavior.json is missing; Rollups.behavior.json is missing; Search.behavior.json is missing; "+
		"Workflow.behavior.json is missing; Assignment.behavior.json is no registered behavior's declaration; Blueprint.behavior.json is no registered behavior's declaration; "+
		"Budget.behavior.json is no registered behavior's declaration; Lease.behavior.json is no registered behavior's declaration; "+
		"Presence.behavior.json is no registered behavior's declaration; Queue.behavior.json is no registered behavior's declaration; "+
		"Retries.behavior.json is no registered behavior's declaration; run: superschematic behaviors --out "+workqueue+" --package @superschematic/engine")

	_, err = runCommandWith(t, nil, "behaviors", "--package", "@acme/behaviors", "--out", t.TempDir())
	require.EqualError(t, err, `behaviors: no behavior this binary registers is implemented by package "@acme/behaviors" (packages: @superschematic/engine, @superschematic/engine-workqueue)`)
	acme := []registry.Extension{registrytest.Acme{}}
	_, err = runCommandWith(t, acme, "behaviors", "--extension", "acme", "--package", "@superschematic/engine", "--out", t.TempDir())
	require.EqualError(t, err, `behaviors: no behavior this binary registers for extension acme is implemented by package "@superschematic/engine" (packages: @superschematic/engine, @superschematic/engine-workqueue)`)
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
	require.ErrorContains(t, err, "generator: types does not render behaviors yet: type Document composes behavior Workflow")

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
	assert.Equal(t, []string{"Assignment", "Blueprint", "Budget", "Comments", "Dependencies", "Lease", "Links", "Presence", "Queue", "Reactions", "Retries", "Revisions", "Rollups", "Search", "Workflow"}, def.Defs.BehaviorRef.Properties.Name.Enum)

	// Dependencies and Links, whose configs name other schemas, load in both
	// forms to the same IR; the loader resolves none of those names.
	crossJSON := load(filepath.Join(loaderTestdata, "fixture-cross-instance-json")).Types["Task"]
	require.Len(t, crossJSON.Behaviors, 3)
	assert.Equal(t, []string{"Workflow", "Dependencies", "Links"}, []string{crossJSON.Behaviors[0].Name, crossJSON.Behaviors[1].Name, crossJSON.Behaviors[2].Name})
	assert.JSONEq(t, `{"schemas": ["tasks", "documents"], "gatedStates": ["done"]}`, string(crossJSON.Behaviors[1].Config))
	assert.JSONEq(t, `{"links": {"spec": {"schema": "documents", "pinned": true}, "parent": {"schema": "tasks", "required": true}, "project": {"schema": "projects"}}}`, string(crossJSON.Behaviors[2].Config))
	assert.Equal(t, crossJSON, load(filepath.Join(tsreaderTestdata, "fixture-cross-instance")).Types["Task"])

	// Rollups, whose config names the tasks' project link, loads in both
	// forms to the same IR; the loader resolves neither the schema nor the
	// link, which the engine checks when the schema is defined.
	rollupsJSON := load(filepath.Join(loaderTestdata, "fixture-rollups-json")).Types["Project"]
	require.Len(t, rollupsJSON.Behaviors, 2)
	assert.Equal(t, []string{"Workflow", "Rollups"}, []string{rollupsJSON.Behaviors[0].Name, rollupsJSON.Behaviors[1].Name})
	assert.JSONEq(t, `{"rollups": {
		"tasks": {"schema": "tasks", "link": "project", "function": "count"},
		"tasksByStatus": {"schema": "tasks", "link": "project", "function": "countBy", "field": "status"},
		"tasksFinished": {"schema": "tasks", "link": "project", "function": "all", "gatedStates": ["done"]}}}`, string(rollupsJSON.Behaviors[1].Config))
	assert.Equal(t, rollupsJSON, load(filepath.Join(tsreaderTestdata, "fixture-rollups")).Types["Project"])

	// Search, whose config names the type's own fields, loads in both forms
	// to the same IR; the engine checks the names and their types.
	searchJSON := load(filepath.Join(loaderTestdata, "fixture-search-json")).Types["Note"]
	require.Len(t, searchJSON.Behaviors, 1)
	assert.Equal(t, "Search", searchJSON.Behaviors[0].Name)
	assert.JSONEq(t, `{"fields": ["title", "body"], "weights": {"title": 3}}`, string(searchJSON.Behaviors[0].Config))
	assert.Equal(t, searchJSON, load(filepath.Join(tsreaderTestdata, "fixture-search")).Types["Note"])

	// Reactions, whose rules name a link and a schema, loads in both forms
	// to the same IR.
	reactionsJSON := load(filepath.Join(loaderTestdata, "fixture-reactions-json")).Types["Project"]
	require.Len(t, reactionsJSON.Behaviors, 3)
	assert.Equal(t, "Reactions", reactionsJSON.Behaviors[2].Name)
	assert.JSONEq(t, `{"rules": [{"when": {"enters": "doing"}, "then": {"link": "parent", "transition": "doing"}}, {"when": {"allTerminal": {"schema": "projects", "link": "parent"}}, "then": {"transition": "done"}}]}`, string(reactionsJSON.Behaviors[2].Config))
	assert.Equal(t, reactionsJSON, load(filepath.Join(tsreaderTestdata, "fixture-reactions")).Types["Project"])

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

	_, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine",
		"--out", filepath.Join("..", "runtime", "engine", "typescript", "src", "behaviors", "core", "declarations"), "--check")
	require.NoError(t, err, "the engine's copies of the core declarations are stale; run make behaviors")
	_, err = runCommandWith(t, nil, "behaviors", "--package", "@superschematic/engine-workqueue",
		"--out", filepath.Join("..", "runtime", "engine-workqueue", "typescript", "src", "declarations"), "--check")
	require.NoError(t, err, "the work-queue package's copies of the core declarations are stale; run make behaviors")
}
