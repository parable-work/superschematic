package verify_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
)

// budgetSource is the core's Budget, whose INTEGER_SEGMENTS the build's are.
const budgetSource = "runtime/engine/rust/rules-behaviors/src/budget.rs"

// TestBudgetIntegerTypesAreTheCores (D104): the build's integer types of a
// meter's limitField and reserveField are the core's.
func TestBudgetIntegerTypesAreTheCores(t *testing.T) {
	holdCoreList(t, budgetSource, "INTEGER_SEGMENTS", verify.IntegerSegments)
}

// TestBudgetConfigIsHeldToTheTable (D104): each of a table's Budget meters'
// limitField and reserveField is an own integer field, its scope a to-one
// relation of the table whose target implements Budget with the meter,
// through a chain of scopes that ends, and its window a reset to count;
// onExceeded needs Lease; and the Workflow escalate moves is the one its
// status names, or the table's one, its move held to it: the core's words
// for these, the build's for a limitField or reserveField a behavior owns,
// a relation by its name, an audit field, a list and a field encrypted at
// rest, a to-many scope, and the scope's target and chain. The first
// problem alone is reported: each meter in the canonical config's order,
// then onExceeded, then escalate.
func TestBudgetConfigIsHeldToTheTable(t *testing.T) {
	const prefix = "type Account: behavior Budget config: "
	meter := func(name string, members map[string]any) func(doc map[string]any) {
		return func(doc map[string]any) {
			meters := agentsConfig(doc, "Account", "Budget")["meters"].(map[string]any)
			m, _ := meters[name].(map[string]any)
			if m == nil {
				m = map[string]any{}
				meters[name] = m
			}
			for k, v := range members {
				if v == nil {
					delete(m, k)
					continue
				}
				m[k] = v
			}
		}
	}
	budget := func(members map[string]any) func(doc map[string]any) {
		return func(doc map[string]any) {
			config := agentsConfig(doc, "Account", "Budget")
			for k, v := range members {
				if v == nil {
					delete(config, k)
					continue
				}
				config[k] = v
			}
		}
	}
	account := func(doc map[string]any) map[string]any { return agentsTable(doc, "Account") }
	holdAgentsRefusals(t, "Budget", []agentsCase{
		{"a limitField the table lacks", meter("questions", map[string]any{"limitField": "questionLimit"}),
			prefix + `meter questions: limitField "questionLimit" is not a field of Account (its fields: name, workspaceId, cap, estimate)`},
		{"a limitField that is not an integer", meter("questions", map[string]any{"limitField": "estimate"}),
			prefix + `meter questions: limitField "estimate" is not an integer field of Account`},
		{"a reserveField that is not an integer", meter("questions", map[string]any{"reserveField": "name"}),
			prefix + `meter questions: reserveField "name" is not an integer field of Account`},
		{"a limitField Workflow owns", meter("questions", map[string]any{"limitField": "statusEnteredAt"}),
			prefix + `meter questions: limitField "statusEnteredAt" is a field Workflow owns, which only its operations change: limitField names the table's own field`},
		{"a limitField relation by its name", meter("questions", map[string]any{"limitField": "workspace"}),
			prefix + `meter questions: limitField "workspace" is a relation: name the field that holds its key, workspaceId`},
		{"a limitField list", func(doc map[string]any) {
			addField(account(doc), map[string]any{"name": "caps", "typeRef": map[string]any{"name": "Generic.Int64", "isArray": true}})
			meter("questions", map[string]any{"limitField": "caps"})(doc)
		}, prefix + `meter questions: limitField "caps" is a list: limitField names a field that holds one integer`},
		{"a scope that is no relation", meter("questions", map[string]any{"scope": "team"}),
			prefix + "meter questions: scope team is not a to-one relation of Account (its relations: workspace)"},
		{"a to-many scope", func(doc map[string]any) {
			addField(account(doc), map[string]any{"name": "pools", "typeRef": map[string]any{"name": "Workspace", "isArray": true}, "relation": map[string]any{"type": "Workspace"}})
			meter("questions", map[string]any{"scope": "pools"})(doc)
		}, prefix + "meter questions: scope pools holds a list of rows, and a meter draws on one scope"},
		{"a scope whose target has no Budget", func(doc map[string]any) {
			agentsTable(doc, "Workspace")["behaviors"] = []any{}
		}, prefix + "meter questions: scope workspace: Workspace, which the relation points at, does not implement Budget"},
		{"a scope whose target lacks the meter", meter("calls", map[string]any{"limit": 5, "scope": "workspace"}),
			prefix + "meter calls: scope workspace: Workspace, which the relation points at, has no meter calls (its meters: questions, tokens)"},
		{"a window with no reset", meter("tokens", map[string]any{"reset": nil}),
			prefix + "meter tokens: window rolling counts the last period, and the meter names no reset to give it one"},
		{"onExceeded with no Lease", budget(map[string]any{"onExceeded": map[string]any{"direct": "overBudget"}}),
			prefix + "onExceeded sends a directive through Lease's direct, which the type does not list"},
		{"a status that is no Workflow", budget(map[string]any{"status": "phase"}),
			prefix + `status "phase" is not a Workflow of the type (its Workflows: status)`},
		{"escalate with no Workflow", func(doc map[string]any) {
			table := account(doc)
			table["behaviors"] = table["behaviors"].([]any)[:1]
		}, prefix + "escalate moves the status through Workflow, which the type does not list"},
		{"an escalate no transition makes", budget(map[string]any{"escalate": map[string]any{"transition": "out", "from": []any{"out"}}}),
			prefix + `escalate moves the status from "out" to itself`},
		{"the first meter alone", func(doc map[string]any) {
			meter("questions", map[string]any{"limitField": "estimate"})(doc)
			meter("tokens", map[string]any{"limitField": "name"})(doc)
		}, prefix + `meter questions: limitField "estimate" is not an integer field of Account`},
	})
	// A chain of scopes that comes back is refused on each table of it.
	_, err := loader.LoadService(writeAgents(t, func(doc map[string]any) {
		workspace := agentsTable(doc, "Workspace")
		addField(workspace, taskField("account", "Account", map[string]any{"relation": map[string]any{"type": "Account"}}))
		agentsConfig(doc, "Workspace", "Budget")["meters"].(map[string]any)["questions"].(map[string]any)["scope"] = "account"
	}))
	for _, want := range []string{
		prefix + "meter questions: scope workspace: its scopes come back to Account (Account, Workspace, Account), and a meter's chain of scopes ends",
		"type Workspace: behavior Budget config: meter questions: scope account: its scopes come back to Workspace (Workspace, Account, Workspace), and a meter's chain of scopes ends",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a chain of scopes that comes back: LoadService = %v, want an error holding %q", err, want)
		}
	}
	holdAgentsAccepted(t, map[string]func(doc map[string]any){
		"an integer limitField and reserveField, an everyMs reset and a status": func(doc map[string]any) {
			meter("questions", map[string]any{"limitField": "cap", "reserveField": "cap", "reset": map[string]any{"everyMs": 600000}})(doc)
			budget(map[string]any{"status": "status"})(doc)
		},
		"no scope": meter("questions", map[string]any{"scope": nil}),
	})
}
