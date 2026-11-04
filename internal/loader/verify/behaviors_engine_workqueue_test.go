package verify_test

import (
	"strings"
	"testing"
)

// TestEngineTypesRefuseTheWorkQueuesTableMembers (D104): an engine type's
// @behavior("Lease", ...), ("Retries", ...), ("Presence", ...) or
// ("Budget", ...) naming a member a table's alone takes, which the shared
// declaration's configSchema accepts, is refused, one problem per member,
// naming the table form, as the engine refuses each: Lease's status;
// Retries' backoff per class in the canonical config's order, then
// retryInto, then, giveUpAfterMs, window, keyField and status; Presence's
// ttlByState then status; Budget's reset other than daily and window per
// meter in the canonical config's order, then observePermission and
// status. An engine type's configs without them load, a daily reset
// among them.
func TestEngineTypesRefuseTheWorkQueuesTableMembers(t *testing.T) {
	job := func(behaviors ...map[string]any) func(doc, _ map[string]any) {
		return func(doc, _ map[string]any) {
			list := []any{map[string]any{"name": "Workflow", "config": map[string]any{
				"states": []any{"queued", "running", "failed"}, "transitions": []any{
					map[string]any{"from": "queued", "to": "running"}, map[string]any{"from": "running", "to": "queued"}, map[string]any{"from": "running", "to": "failed"},
				},
			}}}
			for _, behavior := range behaviors {
				list = append(list, behavior)
			}
			doc["types"].(map[string]any)["Job"] = map[string]any{"name": "Job", "role": "DBTable", "behaviors": list, "fields": []any{
				taskField("id", "Identity.UUID", map[string]any{"required": true, "key": true}),
				taskField("title", "string", map[string]any{"required": true}),
				taskField("worker", "string", nil),
				taskField("revision", "Generic.Int64", nil),
			}}
		}
	}
	behavior := func(name string, config map[string]any) map[string]any {
		return map[string]any{"name": name, "config": config}
	}
	retries := func(members map[string]any) map[string]any {
		config := map[string]any{"classes": map[string]any{"timeout": map[string]any{"attempts": 3}, "rejected": "terminal"}, "totalAttempts": 3, "exhaustedState": "failed"}
		for k, v := range members {
			config[k] = v
		}
		return behavior("Retries", config)
	}
	presence := func(members map[string]any) map[string]any {
		config := map[string]any{"ttlMs": 30000, "principalField": "worker"}
		for k, v := range members {
			config[k] = v
		}
		return behavior("Presence", config)
	}
	budget := func(meters map[string]any, members map[string]any) map[string]any {
		config := map[string]any{"meters": meters}
		for k, v := range members {
			config[k] = v
		}
		return behavior("Budget", config)
	}
	backoff := map[string]any{"firstMs": 1000, "maxMs": 60000}
	const (
		leaseStatus    = "type Job: @behavior Lease names status, the Workflow field a table's Lease moves (implements Lease<Job, C>, D104); an engine type's Lease moves its one Workflow, so leave status out"
		classBackoff   = "type Job: @behavior Retries names backoff on the class %q, which a table's Retries takes (implements Retries<Job, C>, D104); an engine type's Retries keeps no time, so leave it out"
		retriesMember  = "type Job: @behavior Retries names %s, which a table's Retries takes (implements Retries<Job, C>, D104); an engine type's Retries %s, so leave it out"
		ttlByState     = "type Job: @behavior Presence names ttlByState, which a table's Presence takes (implements Presence<Job, C>, D104); an engine type's instance stays present ttlMs after each beat, so leave it out"
		presenceStatus = "type Job: @behavior Presence names status, the Workflow field a table's Presence reads (implements Presence<Job, C>, D104); an engine type's Presence reads its one Workflow, so leave status out"
		reset          = "type Job: @behavior Budget names reset %s on meter %q, which a table's Budget takes (implements Budget<Job, C>, D104); an engine type's meter resets daily or never, so leave it out"
		window         = "type Job: @behavior Budget names window on meter %q, which a table's Budget takes (implements Budget<Job, C>, D104); an engine type's meter counts its current day, or everything, so leave it out"
		observe        = "type Job: @behavior Budget names observePermission, which a table's Budget takes (implements Budget<Job, C>, D104); an engine type's meters count what its instances record, so leave it out"
		budgetStatus   = "type Job: @behavior Budget names status, the Workflow field a table's Budget reads (implements Budget<Job, C>, D104); an engine type's Budget reads its one Workflow, so leave status out"
	)
	quoted := func(format string, args ...string) string {
		out := format
		for _, arg := range args {
			at := strings.Index(out, "%")
			verb := out[at : at+2]
			if verb == "%q" {
				arg = `"` + arg + `"`
			}
			out = out[:at] + arg + out[at+2:]
		}
		return out
	}
	for _, tc := range []struct {
		name string
		edit func(doc, _ map[string]any)
		want []string
	}{
		{"Lease's status", job(behavior("Lease", map[string]any{"status": "status"})), []string{leaseStatus}},
		{"Retries' backoff, per class", job(retries(map[string]any{"classes": map[string]any{
			"timeout": map[string]any{"attempts": 3, "backoff": backoff}, "build": map[string]any{"attempts": 1, "backoff": backoff}, "rejected": "terminal",
		}})), []string{quoted(classBackoff, "build"), quoted(classBackoff, "timeout")}},
		{"Retries' waits, in order", job(retries(map[string]any{
			"status": "status", "keyField": "revision", "window": map[string]any{"attempts": 2, "ms": 60000}, "giveUpAfterMs": 60000,
			"then": map[string]any{"transition": "queued", "from": []any{"running"}}, "retryInto": []any{"running"},
		})), []string{
			quoted(retriesMember, "retryInto", "keeps no time"), quoted(retriesMember, "then", "arms no deadline"),
			quoted(retriesMember, "giveUpAfterMs", "exhausts by its caps alone"), quoted(retriesMember, "window", "keeps no attempt's time"),
			quoted(retriesMember, "keyField", "counts every attempt together"), quoted(retriesMember, "status", "reads its one Workflow"),
		}},
		{"Presence's ttlByState and status", job(presence(map[string]any{"status": "status", "ttlByState": map[string]any{"running": 60000}})), []string{ttlByState, presenceStatus}},
		{"Budget's periods, per meter, observePermission and status", job(budget(map[string]any{
			"tokens":    map[string]any{"limit": 10, "reset": map[string]any{"everyMs": 3600000}, "window": "rolling"},
			"questions": map[string]any{"limit": 10, "reset": "hourly"},
			"cpu":       map[string]any{"limit": 10, "reset": "daily"},
		}, map[string]any{"observePermission": "jobs.observe", "status": "status"})), []string{
			quoted(reset, `"hourly"`, "questions"), quoted(reset, `{"everyMs":3600000}`, "tokens"), quoted(window, "tokens"), observe, budgetStatus,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadTasks(t, tc.edit)
			if err == nil {
				t.Fatal("LoadService succeeded")
			}
			last := -1
			for _, want := range tc.want {
				at := strings.Index(err.Error(), want)
				if at < 0 || at < last {
					t.Fatalf("LoadService = %v, want %q, after the problems before it", err, want)
				}
				last = at
			}
			if got := strings.Count(err.Error(), "(implements "); got != len(tc.want) {
				t.Errorf("LoadService = %v, want %d problems", err, len(tc.want))
			}
		})
	}
	// An engine type's configs without them load.
	if _, err := loadTasks(t, job(
		behavior("Lease", map[string]any{"ttlMs": 30000, "onExpiry": map[string]any{"transition": "queued", "from": []any{"running"}}}),
		retries(nil),
		presence(map[string]any{"onMissed": map[string]any{"transition": "queued", "from": []any{"running"}}}),
		budget(map[string]any{"cpu": map[string]any{"limit": 10, "reset": "daily"}}, map[string]any{"limitPermission": "jobs.budget"}),
	)); err != nil {
		t.Errorf("an engine type's configs without the table's members: %v", err)
	}
}
