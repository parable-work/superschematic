package verify_test

import (
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
)

// leaseSource is the core's Lease, whose HOLD_TYPES the build's are.
const leaseSource = "runtime/engine/rust/rules-behaviors/src/lease.rs"

// TestHoldTypesAreTheCores (D104): the build's types of a maxHoldField are
// the core's.
func TestHoldTypesAreTheCores(t *testing.T) {
	holdCoreList(t, leaseSource, "HOLD_TYPES", verify.HoldTypes)
}

// jobFields are Job's fields as the core's refusal of a field it lacks
// names them.
const jobFields = "title, revision, holdMs, caps, note"

// TestLeaseConfigIsHeldToTheTable (D104): a table's Lease's heartbeatMs is
// at most half of its ttlMs; its maxHoldField is an own integer field; the
// Workflow onExpiry and escalate move is the one its status names, or the
// table's one, each move held to it; escalate needs maxExpiries; each
// exempt entry names another behavior the table lists; and directOn and
// sweepMs are refused: the core's words for these, the build's for a
// maxHoldField a behavior owns, a relation by its name, an audit field, a
// list and a field encrypted at rest. The first problem alone is reported,
// in the core's order.
func TestLeaseConfigIsHeldToTheTable(t *testing.T) {
	const prefix = "type Job: behavior Lease config: "
	lease := func(members map[string]any) func(doc map[string]any) {
		return func(doc map[string]any) {
			config := agentsConfig(doc, "Job", "Lease")
			for k, v := range members {
				config[k] = v
			}
		}
	}
	job := func(doc map[string]any) map[string]any { return agentsTable(doc, "Job") }
	move := func(to string, from ...any) map[string]any { return map[string]any{"transition": to, "from": from} }
	holdAgentsRefusals(t, "Lease", []agentsCase{
		{"a heartbeat longer than half the ttl", lease(map[string]any{"ttlMs": 10000, "heartbeatMs": 6000}),
			prefix + "heartbeatMs (6000) is more than half of ttlMs (10000): one late heartbeat would lose the lease"},
		{"a maxHoldField the table lacks", lease(map[string]any{"maxHoldField": "limit"}),
			prefix + `maxHoldField "limit" is not a field of Job (its fields: ` + jobFields + ")"},
		{"a maxHoldField that is not an integer", lease(map[string]any{"maxHoldField": "note"}),
			prefix + `maxHoldField "note" is not an integer field of Job`},
		{"a maxHoldField Retries owns", lease(map[string]any{"maxHoldField": "retriesTotal"}),
			prefix + `maxHoldField "retriesTotal" is a field Retries owns: maxHoldField names the table's own field`},
		{"a maxHoldField relation by its name", func(doc map[string]any) {
			person(doc)
			addField(job(doc), taskField("sponsor", "Person", map[string]any{"relation": map[string]any{"type": "Person"}}))
			lease(map[string]any{"maxHoldField": "sponsor"})(doc)
		}, prefix + `maxHoldField "sponsor" is a relation: name the field that holds its key, sponsorId`},
		{"a maxHoldField audit field", lease(map[string]any{"maxHoldField": "createdBy"}),
			prefix + `maxHoldField "createdBy" is an audit field the writes stamp: maxHoldField names the table's own field`},
		{"a maxHoldField list", func(doc map[string]any) {
			addField(job(doc), map[string]any{"name": "holds", "typeRef": map[string]any{"name": "Generic.Int64", "isArray": true}})
			lease(map[string]any{"maxHoldField": "holds"})(doc)
		}, prefix + `maxHoldField "holds" is a list: maxHoldField names a field that holds one integer`},
		{"a maxHoldField encrypted at rest", func(doc map[string]any) {
			addField(job(doc), taskField("secretMs", "Generic.Int64", map[string]any{"encryptedAtRest": true}))
			lease(map[string]any{"maxHoldField": "secretMs"})(doc)
		}, prefix + `maxHoldField "secretMs" is encrypted at rest: the core reads no value of it`},
		{"a status that is no Workflow", lease(map[string]any{"status": "phase"}),
			prefix + `status "phase" is not a Workflow of the type (its Workflows: status)`},
		{"two Workflows and no status", func(doc map[string]any) {
			review(job(doc))
			agentsConfig(doc, "Job", "Retries")["status"] = "status"
		}, prefix + "it reads the status of Workflow, and the type has several (status, review): status names the field whose Workflow it reads"},
		{"onExpiry with no Workflow", func(doc map[string]any) {
			table := job(doc)
			table["behaviors"] = table["behaviors"].([]any)[:1]
		}, prefix + "onExpiry moves the status through Workflow, which the type does not list"},
		{"an onExpiry to a state the Workflow lacks", lease(map[string]any{"onExpiry": move("paused", "running")}),
			prefix + `onExpiry.transition "paused" is not a state of the type's Workflow (queued, running, waiting, done, failed)`},
		{"an onExpiry from a state the Workflow lacks", lease(map[string]any{"onExpiry": move("queued", "lost")}),
			prefix + `onExpiry.from names "lost", which is not a state of the type's Workflow (queued, running, waiting, done, failed)`},
		{"an onExpiry to itself", lease(map[string]any{"onExpiry": move("queued", "queued")}),
			prefix + `onExpiry moves the status from "queued" to itself`},
		{"an escalate no transition makes", lease(map[string]any{"escalate": move("failed", "done")}),
			prefix + `escalate: no transition of the type's Workflow leads from "done" to "failed"`},
		{"an escalate with no maxExpiries", func(doc map[string]any) { delete(agentsConfig(doc, "Job", "Lease"), "maxExpiries") },
			prefix + "escalate applies at the expiry that reaches maxExpiries, which the config does not give"},
		{"an exempt operation of Lease's own", lease(map[string]any{"exempt": []any{"Lease.heartbeat"}}),
			prefix + "exempt names Lease.heartbeat: Lease's own operations check their callers themselves"},
		{"an exempt behavior the table lacks", lease(map[string]any{"exempt": []any{"Timers.fireTimer"}}),
			prefix + "exempt names Timers.fireTimer, but the type does not list Timers (it lists Lease, Retries, Workflow)"},
		{"directOn", lease(map[string]any{"directOn": []any{map[string]any{"revised": map[string]any{"link": "spec"}, "name": "rebase"}}}),
			prefix + "directOn sends a directive when the target of a link moves on, a revision of Revisions or a release of Branches, which waits for Revisions and Branches on the core (D103, step 4)"},
		{"sweepMs", lease(map[string]any{"sweepMs": 1000}),
			prefix + "sweepMs is how often the engine's runner swept the lapsed leases: a table's lease expires at its own deadline, which the API's behavior worker runs when it falls due (D103), so a table's Lease takes no sweepMs"},
		{"the first problem alone", lease(map[string]any{"maxHoldField": "note", "sweepMs": 1000}),
			prefix + `maxHoldField "note" is not an integer field of Job`},
	})
	holdAgentsAccepted(t, map[string]func(doc map[string]any){
		"an integer maxHoldField, a status and an exempt operation": lease(map[string]any{"maxHoldField": "holdMs", "status": "status", "exempt": []any{"Retries.recordAttempt"}, "ttlMs": 10000, "heartbeatMs": 5000}),
		"no move on a table with no Workflow": func(doc map[string]any) {
			table := job(doc)
			table["behaviors"] = table["behaviors"].([]any)[:1]
			config := agentsConfig(doc, "Job", "Lease")
			delete(config, "onExpiry")
			delete(config, "escalate")
		},
	})
}
