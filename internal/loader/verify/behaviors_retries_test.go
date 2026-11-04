package verify_test

import (
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
)

// retriesSource is the core's Retries, whose LIMITS_TYPES the build's are.
const retriesSource = "runtime/engine/rust/rules-behaviors/src/retries.rs"

// TestLimitsTypesAreTheCores (D104): the build's types of a limitsField are
// the core's.
func TestLimitsTypesAreTheCores(t *testing.T) {
	holdCoreList(t, retriesSource, "LIMITS_TYPES", verify.LimitsTypes)
}

// TestRetriesConfigIsHeldToTheTable (D104): a table's Retries' limitsField
// is an own object field and its resultField an own field; its Workflow is
// the one its status names, or the table's one, with exhaustedState and
// from its states, from leading to exhaustedState; its keyField an own
// scalar field; each backoff's maxMs at least its firstMs; retryInto's
// states and then's move held to the Workflow; something waits for each
// wait; and resultField is refused last: the core's words for these, the
// build's for a limitsField or keyField a behavior owns, a relation by its
// name, an audit field, a list and a field encrypted at rest. The first
// problem alone is reported, in the core's order.
func TestRetriesConfigIsHeldToTheTable(t *testing.T) {
	const prefix = "type Job: behavior Retries config: "
	const states = "queued, running, waiting, done, failed"
	retries := func(members map[string]any) func(doc map[string]any) {
		return func(doc map[string]any) {
			config := agentsConfig(doc, "Job", "Retries")
			for k, v := range members {
				if v == nil {
					delete(config, k)
					continue
				}
				config[k] = v
			}
		}
	}
	job := func(doc map[string]any) map[string]any { return agentsTable(doc, "Job") }
	// unleased drops Job's Lease, which the waits may rely on.
	unleased := func(doc map[string]any) {
		table := job(doc)
		table["behaviors"] = table["behaviors"].([]any)[1:]
	}
	plain := map[string]any{"timeout": map[string]any{"attempts": 5}}
	backoff := func(first, max int) map[string]any {
		return map[string]any{"timeout": map[string]any{"attempts": 5, "backoff": map[string]any{"firstMs": first, "maxMs": max}}}
	}
	holdAgentsRefusals(t, "Retries", []agentsCase{
		{"a limitsField the table lacks", retries(map[string]any{"limitsField": "limits"}),
			prefix + `limitsField "limits" is not a field of Job (its fields: ` + jobFields + ")"},
		{"a limitsField that is not an object", retries(map[string]any{"limitsField": "note"}),
			prefix + `limitsField "note" is not an object field of Job`},
		{"a limitsField Lease owns", retries(map[string]any{"limitsField": "leaseEnded"}),
			prefix + `limitsField "leaseEnded" is a field Lease owns: limitsField names the table's own field`},
		{"a limitsField encrypted at rest", func(doc map[string]any) {
			addField(job(doc), taskField("secretCaps", "Generic.JSON", map[string]any{"encryptedAtRest": true}))
			retries(map[string]any{"limitsField": "secretCaps"})(doc)
		}, prefix + `limitsField "secretCaps" is encrypted at rest: the core reads no value of it`},
		{"a resultField the table lacks", retries(map[string]any{"resultField": "result"}),
			prefix + `resultField "result" is not a field of Job (its fields: ` + jobFields + ")"},
		{"a status that is no Workflow", retries(map[string]any{"status": "phase"}),
			prefix + `status "phase" is not a Workflow of the type (its Workflows: status)`},
		{"an exhaustedState the Workflow lacks", retries(map[string]any{"exhaustedState": "dead"}),
			prefix + `exhaustedState "dead" is not a state of the type's Workflow (` + states + ")"},
		{"a from the Workflow lacks", retries(map[string]any{"from": []any{"running", "lost"}}),
			prefix + `from names "lost", which is not a state of the type's Workflow (` + states + ")"},
		{"a from naming exhaustedState", retries(map[string]any{"from": []any{"failed"}}),
			prefix + `from names exhaustedState "failed", which exhaustion cannot move the status from`},
		{"a from no transition leads from", retries(map[string]any{"from": []any{"done"}}),
			prefix + `from: no transition of the type's Workflow leads from "done" to "failed"`},
		{"a keyField the table lacks", retries(map[string]any{"keyField": "version"}),
			prefix + `keyField "version" is not a field of Job (its fields: ` + jobFields + ")"},
		{"a keyField of JSON", retries(map[string]any{"keyField": "caps"}),
			prefix + `keyField "caps" is a Generic.JSON field of Job: the value that keys the counts is one scalar`},
		{"a keyField Workflow owns", retries(map[string]any{"keyField": "statusEnteredAt"}),
			prefix + `keyField "statusEnteredAt" is a field Workflow owns: keyField names the table's own field`},
		{"a keyField audit field", retries(map[string]any{"keyField": "createdBy"}),
			prefix + `keyField "createdBy" is an audit field the writes stamp: keyField names the table's own field`},
		{"a keyField list", func(doc map[string]any) {
			addField(job(doc), map[string]any{"name": "tags", "typeRef": map[string]any{"name": "string", "isArray": true}})
			retries(map[string]any{"keyField": "tags"})(doc)
		}, prefix + `keyField "tags" is a list: keyField names a field that holds one scalar`},
		{"a backoff shorter at most than at first", retries(map[string]any{"classes": backoff(60000, 1000)}),
			prefix + "classes.timeout.backoff: maxMs (1000) is shorter than firstMs (60000)"},
		{"a retryInto state the Workflow lacks", retries(map[string]any{"retryInto": []any{"paused"}}),
			prefix + `retryInto names "paused", which is not a state of the type's Workflow (` + states + ")"},
		{"a retryInto naming exhaustedState", retries(map[string]any{"retryInto": []any{"failed"}}),
			prefix + `retryInto names exhaustedState "failed", which an exhausted row moves to and stays in`},
		{"a then to a state the Workflow lacks", retries(map[string]any{"then": map[string]any{"transition": "paused", "from": []any{"waiting"}}}),
			prefix + `then.transition "paused" is not a state of the type's Workflow (` + states + ")"},
		{"a then no transition makes", retries(map[string]any{"then": map[string]any{"transition": "queued", "from": []any{"done"}}}),
			prefix + `then: no transition of the type's Workflow leads from "done" to "queued"`},
		{"a then to itself", retries(map[string]any{"then": map[string]any{"transition": "queued", "from": []any{"queued"}}}),
			prefix + `then moves the status from "queued" to itself`},
		{"a then with no backoff", retries(map[string]any{"classes": plain}),
			prefix + "then moves the row when its backoff ends, and no class sets a backoff"},
		{"a retryInto with neither", retries(map[string]any{"classes": plain, "then": nil, "window": nil}),
			prefix + "retryInto names the states an attempt starts in, which wait for a backoff or the window, and the config sets neither"},
		{"a backoff nothing waits for", func(doc map[string]any) {
			unleased(doc)
			retries(map[string]any{"retryInto": nil, "then": nil, "window": nil})(doc)
		}, prefix + "classes.timeout.backoff sets when the row runs again (retryAt), which a move into retryInto, then and Lease's acquire wait for: the config names no retryInto and no then, and the type has no Lease"},
		{"a window nothing waits for", func(doc map[string]any) {
			unleased(doc)
			retries(map[string]any{"classes": plain, "retryInto": nil, "then": nil})(doc)
		}, prefix + "window caps the attempts that start, a move into retryInto or Lease's acquire: the config names no retryInto, and the type has no Lease"},
		{"resultField, last", retries(map[string]any{"resultField": "note"}),
			prefix + "resultField writes a kept result to a field of the row through an update an operation makes, which waits for the core to run one (D103, step 4: Revisions)"},
		{"the first problem alone", retries(map[string]any{"keyField": "caps", "resultField": "note"}),
			prefix + `keyField "caps" is a Generic.JSON field of Job: the value that keys the counts is one scalar`},
	})
	holdAgentsAccepted(t, map[string]func(doc map[string]any){
		"a JSON limitsField, a status and a from": retries(map[string]any{"limitsField": "caps", "status": "status", "from": []any{"running", "queued"}}),
		"a backoff Lease's acquire waits for":     retries(map[string]any{"retryInto": nil, "then": nil, "window": nil}),
		"a window Lease's acquire waits for":      retries(map[string]any{"classes": plain, "retryInto": nil, "then": nil}),
		"a string keyField":                       retries(map[string]any{"keyField": "note"}),
	})
}
