package verify_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// poolSource is the core's Pool, whose ORDER_TYPES are the types its order
// field may have (D104).
const poolSource = "runtime/engine/rust/rules-behaviors/src/pool.rs"

// TestOrderTypesAreTheCores (D104): the build's types of Pool's order field
// are the core's ORDER_TYPES, in order, so the build refuses the order
// fields the core refuses at a define, in the same words, and accepts the
// rest.
func TestOrderTypesAreTheCores(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testpaths.RepoRoot(t), filepath.FromSlash(poolSource)))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`pub const ORDER_TYPES: \[&str; \d+\] = \[([^\]]*)\];`).FindSubmatch(data)
	if match == nil {
		t.Fatalf("%s declares no ORDER_TYPES", poolSource)
	}
	var core []string
	for _, quoted := range regexp.MustCompile(`"([^"]*)"`).FindAllSubmatch(match[1], -1) {
		core = append(core, string(quoted[1]))
	}
	if !slices.Equal(verify.OrderTypes, core) {
		t.Errorf("the build's ORDER_TYPES = %v, the core's = %v (%s)", verify.OrderTypes, core, poolSource)
	}
}

// pool gives Task the implements Pool use of config alone, ranked by
// attempts, as fixture-pool-db's Account implements it (D104).
func pool(config map[string]any) func(doc, task map[string]any) {
	return func(_, task map[string]any) {
		task["behaviors"] = []any{map[string]any{"name": "Pool", "implements": true, "config": config}}
	}
}

// TestPoolOrderRefused (D104): Pool's order names an own field of the
// table, in the core's words for a field the table lacks, and one that
// holds a number (number, Generic.Int64 or Generic.Float64), in the core's;
// and the build refuses, in the words Presence's principalField gets, a
// field a behavior owns, a relation by its name, an audit field, a list and
// a field encrypted at rest.
func TestPoolOrderRefused(t *testing.T) {
	const prefix = "type Task: behavior Pool config: "
	order := func(field string) map[string]any { return map[string]any{"order": field} }
	holdRefusals(t, "Pool", []refusalCase{
		{"a field the table lacks", pool(order("score")),
			// createdBy is an audit field, which the spec lists only for a
			// table whose rules read it, and Task's Pool reads none.
			prefix + `order "score" is not a field of Task (its fields: title, owner, reviewer, plan, proof, urgent, attempts)`},
		{"a string field", pool(order("title")),
			prefix + `order "title" is not a number field of Task: it ranks the members by a number (number, Generic.Int64 or Generic.Float64)`},
		{"a boolean field", pool(order("urgent")),
			prefix + `order "urgent" is not a number field of Task: it ranks the members by a number (number, Generic.Int64 or Generic.Float64)`},
		{"a decimal field", edits(pool(order("price")), func(_, task map[string]any) { addField(task, taskField("price", "Generic.Decimal", nil)) }),
			prefix + `order "price" is not a number field of Task: it ranks the members by a number (number, Generic.Int64 or Generic.Float64)`},
		{"a field Pool owns", pool(order("poolOutUntil")),
			prefix + `order "poolOutUntil" is a field Pool owns, which only its operations change: order names the table's own field whose value ranks the members`},
		{"a relation by its name", edits(func(doc, task map[string]any) {
			person(doc)
			addField(task, taskField("sponsor", "Person", map[string]any{"relation": map[string]any{"type": "Person"}}))
		}, pool(order("sponsor"))),
			prefix + `order "sponsor" is a relation: name the field that holds its key, sponsorId`},
		{"an audit field", pool(order("createdBy")),
			prefix + `order "createdBy" is an audit field the writes stamp: order names the table's own field whose value ranks the members`},
		{"a list", edits(pool(order("scores")), func(_, task map[string]any) {
			addField(task, map[string]any{"name": "scores", "typeRef": map[string]any{"name": "Generic.Int64", "isArray": true}})
		}),
			prefix + `order "scores" is a list: order names a field that holds one number`},
		{"a field encrypted at rest", edits(pool(order("secret")), func(_, task map[string]any) {
			addField(task, taskField("secret", "Generic.Int64", map[string]any{"encryptedAtRest": true}))
		}),
			prefix + `order "secret" is encrypted at rest: the core reads no value of it`},
	})
	holdAccepted(t, map[string]func(doc, task map[string]any){
		"a number field": pool(order("attempts")),
		"an integer field": edits(pool(order("rank")), func(_, task map[string]any) {
			addField(task, taskField("rank", "Generic.Int64", nil))
		}),
		"a float field and every member": edits(pool(map[string]any{"order": "weight", "outForMs": 1800000, "recheckAfterMs": 120000, "permission": "tasks.pool"}), func(_, task map[string]any) {
			addField(task, taskField("weight", "Generic.Float64", nil))
		}),
	})
}

// TestPoolConfigShapeIsTheDeclarations (D104): a config of the wrong shape
// is refused by the declaration's configSchema, before the table's checks:
// order is needed, outForMs and recheckAfterMs are at least 1000, a
// permission is not empty, and a config names no member but its own.
func TestPoolConfigShapeIsTheDeclarations(t *testing.T) {
	for name, config := range map[string]map[string]any{
		"no order":                 {"outForMs": 1800000},
		"an empty order":           {"order": ""},
		"an outForMs under 1000":   {"order": "attempts", "outForMs": 10},
		"a recheckAfterMs of 0":    {"order": "attempts", "recheckAfterMs": 0},
		"an empty permission":      {"order": "attempts", "permission": ""},
		"a member it does not own": {"order": "attempts", "ttlMs": 1000},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadTasks(t, pool(config))
			if err == nil || !strings.Contains(err.Error(), `type "Task": behavior Pool config: jsonschema validation failed`) {
				t.Fatalf("LoadService = %v, want Pool's config refused", err)
			}
		})
	}
}

// TestPoolOnAnEngineTypeRefused (D104): Pool is the behaviors' core's
// alone, which no engine implements, so an engine type's @behavior use of
// it is refused, naming the table form.
func TestPoolOnAnEngineTypeRefused(t *testing.T) {
	const want = "type Note: behavior Pool is implemented by the behaviors' core alone, which runs it on a table, and no engine implements it: write implements Pool<Note, { ... }> on a DB table (D104)"
	_, err := loadTasks(t, func(doc, _ map[string]any) {
		doc["types"].(map[string]any)["Note"] = map[string]any{"name": "Note", "role": "DBTable", "behaviors": []any{
			map[string]any{"name": "Pool", "config": map[string]any{"order": "rank"}},
		}, "fields": []any{
			taskField("id", "Identity.UUID", map[string]any{"required": true, "key": true}),
		}}
	})
	if err == nil || !strings.Contains(err.Error(), want) || strings.Count(err.Error(), "behaviors' core alone") != 1 {
		t.Fatalf("LoadService = %v, want one problem, %q", err, want)
	}
}

// TestPoolTableWithoutBudgetLoads (D104): a table that implements Pool and
// no behavior that implements Capacity loads with no warning, since
// Capacity answers room for every member of it; it keeps Pool's four owned
// fields, a deadline queue and an event queue, and no change queue.
func TestPoolTableWithoutBudgetLoads(t *testing.T) {
	schema, err := loadTasks(t, pool(map[string]any{"order": "attempts"}))
	if err != nil {
		t.Fatal(err)
	}
	var owned []string
	for _, fd := range schema.Types["Task"].Fields {
		if fd.Owned() {
			owned = append(owned, fd.Name+":"+fd.TypeRef.Name)
		}
	}
	if want := []string{"poolAvailability:string", "poolOutUntil:Temporal.DateTime", "poolOutReason:string", "poolCheckedAt:Temporal.DateTime"}; !slices.Equal(owned, want) {
		t.Errorf("Task's owned fields = %v, want %v", owned, want)
	}
	for kind, want := range map[string]string{ir.BehaviorQueueDeadlines: "TaskDeadline", ir.BehaviorQueueEvents: "TaskEvent", ir.BehaviorQueueChanges: ""} {
		queue := schema.BehaviorQueueOf("Task", kind)
		switch {
		case want == "" && queue != nil:
			t.Errorf("Task's %s queue = %s, want none", kind, queue.Name)
		case want != "" && (queue == nil || queue.Name != want || !slices.Equal(queue.BehaviorQueue.Behaviors, []string{"Pool"})):
			t.Errorf("Task's %s queue = %+v, want %s, Pool's", kind, queue, want)
		}
	}
}
