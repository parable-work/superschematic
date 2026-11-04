package verify_test

import (
	"slices"
	"testing"
)

// TestPresenceConfigIsHeldToTheTable (D104): a table's Presence takes no
// sweepMs; its principalField is an own field that holds a subject; the
// Workflow onMissed, onBeat and ttlByState read is the one its status
// names, or the table's one, each move held to it and each ttlByState
// state one of its states; and each releaseLeases name is a table of the
// database that implements Lease with an overridePermission: the core's
// words for the first three, the build's for a principalField a behavior
// owns, a relation by its name, an audit field, a list and a field
// encrypted at rest, and for releaseLeases. The first problem alone is
// reported, in the core's order, then releaseLeases.
func TestPresenceConfigIsHeldToTheTable(t *testing.T) {
	const prefix = "type Session: behavior Presence config: "
	const states = "idle, turn, missing"
	presence := func(members map[string]any) func(doc map[string]any) {
		return func(doc map[string]any) {
			config := agentsConfig(doc, "Session", "Presence")
			for k, v := range members {
				if v == nil {
					delete(config, k)
					continue
				}
				config[k] = v
			}
		}
	}
	session := func(doc map[string]any) map[string]any { return agentsTable(doc, "Session") }
	noWorkflow := func(doc map[string]any) {
		table := session(doc)
		table["behaviors"] = table["behaviors"].([]any)[:1]
	}
	move := func(to string, from ...any) map[string]any { return map[string]any{"transition": to, "from": from} }
	holdAgentsRefusals(t, "Presence", []agentsCase{
		{"sweepMs", presence(map[string]any{"sweepMs": 5000}),
			prefix + "sweepMs is the engine's runner's, which missed the instances past their deadline every sweepMs: on a table the API's @behaviorWorker runs each row's miss as its deadline falls due, so a table's Presence takes no sweepMs (D104)"},
		{"a principalField the table lacks", presence(map[string]any{"principalField": "worker"}),
			prefix + `principalField "worker" is not a field of Session (its fields: device, agent, seats)`},
		{"a principalField that holds no subject", presence(map[string]any{"principalField": "seats"}),
			prefix + `principalField "seats" is not a string field of Session`},
		{"a principalField Presence owns", presence(map[string]any{"principalField": "presenceDeadline"}),
			prefix + `principalField "presenceDeadline" is a field Presence owns, which only its operations change: principalField names the table's own field that holds the principal`},
		{"a principalField relation by its name", func(doc map[string]any) {
			person(doc)
			addField(session(doc), taskField("owner", "Person", map[string]any{"relation": map[string]any{"type": "Person"}}))
			presence(map[string]any{"principalField": "owner"})(doc)
		}, prefix + `principalField "owner" is a relation: name the field that holds its key, ownerId`},
		{"a principalField audit field", func(doc map[string]any) {
			addField(session(doc), taskField("createdBy", "Identity.UUID", nil))
			presence(map[string]any{"principalField": "createdBy"})(doc)
		}, prefix + `principalField "createdBy" is an audit field the writes stamp: principalField names the table's own field that holds the principal`},
		{"a principalField list", func(doc map[string]any) {
			addField(session(doc), map[string]any{"name": "devices", "typeRef": map[string]any{"name": "string", "isArray": true}})
			presence(map[string]any{"principalField": "devices"})(doc)
		}, prefix + `principalField "devices" is a list: principalField names a field that holds one subject`},
		{"a principalField encrypted at rest", func(doc map[string]any) {
			addField(session(doc), taskField("secret", "string", map[string]any{"encryptedAtRest": true}))
			presence(map[string]any{"principalField": "secret"})(doc)
		}, prefix + `principalField "secret" is encrypted at rest: the core reads no value of it`},
		{"a status that is no Workflow", presence(map[string]any{"status": "phase"}),
			prefix + `status "phase" is not a Workflow of the type (its Workflows: status)`},
		{"onMissed with no Workflow", noWorkflow,
			prefix + "onMissed moves the status through Workflow, which the type does not list"},
		{"onBeat with no Workflow", func(doc map[string]any) {
			noWorkflow(doc)
			presence(map[string]any{"onMissed": nil})(doc)
		}, prefix + "onBeat moves the status through Workflow, which the type does not list"},
		{"ttlByState with no Workflow", func(doc map[string]any) {
			noWorkflow(doc)
			presence(map[string]any{"onMissed": nil, "onBeat": nil})(doc)
		}, prefix + "ttlByState times the states of Workflow, which the type does not list"},
		{"an onMissed no transition makes", presence(map[string]any{"onMissed": move("turn", "missing")}),
			prefix + `onMissed: no transition of the type's Workflow leads from "missing" to "turn"`},
		{"an onBeat to a state the Workflow lacks", presence(map[string]any{"onBeat": move("away", "missing")}),
			prefix + `onBeat.transition "away" is not a state of the type's Workflow (` + states + ")"},
		{"a ttlByState state the Workflow lacks", presence(map[string]any{"ttlByState": map[string]any{"lunch": 600000, "turn": 900000}}),
			prefix + `ttlByState: "lunch" is not a state of the type's Workflow (` + states + ")"},
		{"releaseLeases naming no table", presence(map[string]any{"releaseLeases": []any{"Jobs"}}),
			prefix + "releaseLeases: Jobs is not a table of agents-db"},
		{"releaseLeases naming a table without Lease", presence(map[string]any{"releaseLeases": []any{"Account"}}),
			prefix + "releaseLeases: Account does not implement Lease, so it holds no lease to expire"},
		{"releaseLeases naming a Lease without overridePermission", func(doc map[string]any) {
			delete(agentsConfig(doc, "Job", "Lease"), "overridePermission")
		}, prefix + "releaseLeases: Job's Lease names no overridePermission, which a miss's expiry of the principal's leases needs (D104)"},
	})
	holdAgentsAccepted(t, map[string]func(doc map[string]any){
		"a string principalField and a status": func(doc map[string]any) {
			presence(map[string]any{"principalField": "agent", "status": "status"})(doc)
		},
		"no Workflow and no move": func(doc map[string]any) {
			noWorkflow(doc)
			presence(map[string]any{"onMissed": nil, "onBeat": nil, "ttlByState": nil})(doc)
		},
		// The core reads no Workflow for a config with no move, no
		// ttlByState and no status, so two are no choice to make.
		"two Workflows and no move": func(doc map[string]any) {
			review(session(doc))
			presence(map[string]any{"onMissed": nil, "onBeat": nil, "ttlByState": nil, "status": nil})(doc)
		},
	})
}

// TestReleaseLeasesWorkerPermissionWarns (D104): a @behaviorWorker of a
// table whose Presence releases leases on a table whose Lease names an
// overridePermission the worker's permissions do not cover is warned of,
// never refused, since every miss would fail; a worker that holds the
// permission, or one that covers it, is not.
func TestReleaseLeasesWorkerPermissionWarns(t *testing.T) {
	const warning = "agents-api: SessionPresence runs Session's misses, which expire the leases Session's Presence releases on Job through Lease's expire, which needs Job's Lease overridePermission jobs.override: list it in the worker's permissions, or every miss fails (D104)"
	worker := func(permissions string) string {
		return `[{"name": "SessionPresence", "behaviorWorker": {"type": "Session"` + permissions + `}}]`
	}
	for name, tc := range map[string]struct {
		workers string
		want    []string
	}{
		"no permissions":     {worker(""), []string{warning}},
		"another permission": {worker(`, "permissions": ["jobs.write"]`), []string{warning}},
		"the permission":     {worker(`, "permissions": ["jobs.override"]`), nil},
		"one that covers it": {worker(`, "permissions": ["jobs"]`), nil},
	} {
		t.Run(name, func(t *testing.T) {
			if got := agentsWarnings(t, nil, []string{"Session"}, tc.workers); !slices.Equal(got, tc.want) {
				t.Errorf("warnings =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}
