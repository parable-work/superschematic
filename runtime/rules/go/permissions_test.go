package rules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// The core's permissions read (D200) through the adapter: an Approvals
// requirement with recheckPermission asks for the permissions each signer
// holds now, which the adapter reads from the layout's identity tables, as
// the identity runtime's store reads a principal's roles. A user holds the
// permissions of every role a grant gives them, each once; a user with no
// grant holds none; a disabled or a pending user holds none; and a subject
// no user row holds, or that is no key of the table, is left out. A layout
// that names no identity tables answers no subject, so a requirement that
// reads one fails closed rather than the command failing.

// The identity runtime's users, as their key columns hold them.
const (
	annColumn  = "0e5c7d2a-4b1f-4c3e-9a8d-7f6e5d4c3b2a"
	benColumn  = "1f6d8e3b-5c2a-4d4f-8b9e-8a7f6e5d4c3b"
	camColumn  = "2a7e9f4c-6d3b-4e5a-9caf-9b8a7f6e5d4c"
	dovColumn  = "3b8fa05d-7e4c-4f6b-8db0-ac9b8a7f6e5d"
	eveColumn  = "4c9ab16e-8f5d-4a7c-9ec1-bdac9b8a7f6e"
	noneColumn = "5dabc27f-9a6e-4b8d-8fd2-cebdac9b8a7f"
)

// identityPermissions is where fixture-approvals-identity-db's layout
// names its identity tables (behaviorlayout.Permissions).
func identityPermissions() *rules.Permissions {
	return &rules.Permissions{
		Users:           "account",
		User:            rules.Column{Field: "id", Column: "id", Type: "Identity.UUID"},
		Credentials:     "user_credential",
		CredentialUser:  "user_id",
		DisabledAt:      "disabled_at",
		PendingAt:       "pending_at",
		Grants:          "user_role_grant",
		GrantUser:       "user_id",
		GrantRole:       "role_id",
		Roles:           "role",
		RoleKey:         "id",
		RolePermissions: "permissions",
	}
}

// createIdentity writes the identity tables as sqlgen writes them, their
// keys UUID and the roles' permissions TEXT[] on Postgres, TEXT on SQLite:
// ann holds two roles, which share a permission; ben holds none; cam holds
// one and is disabled; dov holds one and waits for approval; eve holds one
// and has no credential row.
func createIdentity(t *testing.T, db *rulestest.DB) {
	t.Helper()
	types := db.ColumnTypes()
	uuid, texts := "TEXT", "TEXT"
	if db.Dialect == rules.DialectPostgres {
		uuid, texts = "UUID", "TEXT[]"
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "account" (id %s PRIMARY KEY, email %s)`, uuid, types.Text))
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "user_credential" (id %s PRIMARY KEY, user_id %s, disabled_at %s, pending_at %s)`, uuid, uuid, types.Timestamp, types.Timestamp))
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "role" (id %s PRIMARY KEY, name %s, permissions %s)`, uuid, types.Text, texts))
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "user_role_grant" (id %s PRIMARY KEY, user_id %s, role_id %s)`, uuid, uuid, uuid))
	for i, user := range []string{annColumn, benColumn, camColumn, dovColumn, eveColumn} {
		db.Exec(t, fmt.Sprintf(`INSERT INTO "account" (id, email) VALUES (%s, %s)`, db.P(1), db.P(2)), user, fmt.Sprintf("user%d@example.com", i))
	}
	at := db.Timestamp(db.Now(t))
	credential := fmt.Sprintf(`INSERT INTO "user_credential" (id, user_id, disabled_at, pending_at) VALUES (%s, %s, %s, %s)`, db.P(1), db.P(2), db.P(3), db.P(4))
	db.Exec(t, credential, "00000000-0000-4000-8000-000000000001", annColumn, nil, nil)
	db.Exec(t, credential, "00000000-0000-4000-8000-000000000002", benColumn, nil, nil)
	db.Exec(t, credential, "00000000-0000-4000-8000-000000000003", camColumn, at, nil)
	db.Exec(t, credential, "00000000-0000-4000-8000-000000000004", dovColumn, nil, at)
	role := fmt.Sprintf(`INSERT INTO "role" (id, name, permissions) VALUES (%s, %s, %s)`, db.P(1), db.P(2), db.P(3))
	reviewer, lead := "00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000a2"
	db.Exec(t, role, reviewer, "reviewer", heldList(t, db, []string{"changes.read", "changes.approve"}))
	db.Exec(t, role, lead, "lead", heldList(t, db, []string{"changes.approve", "changes.lead"}))
	grant := fmt.Sprintf(`INSERT INTO "user_role_grant" (id, user_id, role_id) VALUES (%s, %s, %s)`, db.P(1), db.P(2), db.P(3))
	for i, g := range [][2]string{{annColumn, reviewer}, {annColumn, lead}, {camColumn, reviewer}, {dovColumn, reviewer}, {eveColumn, lead}} {
		db.Exec(t, grant, fmt.Sprintf("00000000-0000-4000-8000-0000000000b%d", i), g[0], g[1])
	}
}

func TestAPermissionsReadAnswersFromTheIdentityTables(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createNotes(t, db)
		createIdentity(t, db)
		db.Exec(t, fmt.Sprintf(`INSERT INTO "note" (id, title, status) VALUES (%s, %s, 'draft')`, db.P(1), db.P(2)), uuidColumn, "Which lamp?")
		// ann by her wire form, ben hyphenated, and four the read leaves out:
		// a subject no user row holds, one that is no UUID, and an agent's.
		ann := wire(t, annColumn)
		read := fmt.Sprintf(`{"read":[{"kind":"permissions","subjects":[%q,%q,%q,%q,%q,%q,%q,"no-uuid","an-agent"]}]}`, ann, benColumn, wire(t, camColumn), dovColumn, eveColumn, noneColumn, uuidKey)
		answered := func(t *testing.T, layout *rules.Layout) json.RawMessage {
			t.Helper()
			core := script(t, read, changed(""))
			if err := inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
				host.Core = core
				_, err := rules.Execute(context.Background(), host, "Note", transition(uuidKey, "paid"), nil)
				return err
			}); err != nil {
				t.Fatalf("a command whose core reads permissions: %v", err)
			}
			if len(core.answers) != 2 || len(core.answers[1].Reads) != 1 {
				t.Fatalf("the core was answered %+v", core.answers)
			}
			answer := core.answers[1].Reads[0].Answer
			if answer.Kind != rules.ReadKindPermissions {
				t.Fatalf("the answer is of kind %q", answer.Kind)
			}
			text, err := answer.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			return json.RawMessage(text)
		}
		layout := &rules.Layout{Types: map[string]*rules.Table{"Note": noteTable()}, Permissions: identityPermissions()}
		want, _ := json.Marshal(map[string]any{"permissions": map[string][]string{
			ann: {"changes.read", "changes.approve", "changes.lead"}, benColumn: {}, wire(t, camColumn): {}, dovColumn: {}, eveColumn: {"changes.approve", "changes.lead"},
		}})
		wantJSON(t, "the permissions read", answered(t, layout), string(want))

		// An Agent is answered through its agent role, its definition row's
		// role naming a role by its name (#625); one whose role no row
		// names holds none, and one no definition row holds is left out.
		db.Exec(t, fmt.Sprintf(`CREATE TABLE "agent" (id %s PRIMARY KEY, name %s, role %s)`, agentKeyType(db), db.ColumnTypes().Text, db.ColumnTypes().Text))
		agent := fmt.Sprintf(`INSERT INTO "agent" (id, name, role) VALUES (%s, %s, %s)`, db.P(1), db.P(2), db.P(3))
		const triage, roleless = "6ebcd380-ab7f-4c9e-90e3-dfcebdac9b8a", "80def5a2-cd91-4eb0-92a5-f1e0dfcebdac"
		db.Exec(t, agent, triage, "triage", "reviewer")
		db.Exec(t, agent, roleless, "roleless", "no-such-role")
		withAgents := identityPermissions()
		withAgents.Agents, withAgents.Agent, withAgents.AgentRole, withAgents.RoleName = "agent", &rules.Column{Field: "id", Column: "id", Type: "Identity.UUID"}, "role", "name"
		read = fmt.Sprintf(`{"read":[{"kind":"permissions","subjects":[%q],"agents":[%q,%q,%q,"no-uuid"]}]}`, benColumn, wire(t, triage), roleless, noneColumn)
		layout.Permissions = withAgents
		want, _ = json.Marshal(map[string]any{"permissions": map[string][]string{
			benColumn: {}, wire(t, triage): {"changes.read", "changes.approve"}, roleless: {},
		}})
		wantJSON(t, "the permissions read of Agents", answered(t, layout), string(want))
		layout.Permissions = identityPermissions()
		wantJSON(t, "the permissions read of Agents with no agents table", answered(t, layout), `{"permissions":{"`+benColumn+`":[]}}`)

		// A layout that names no identity tables answers no subject.
		layout.Permissions = nil
		wantJSON(t, "the permissions read of a layout with no identity tables", answered(t, layout), `{"permissions":{}}`)
	})
}

// agentKeyType is the agents table's key column's type: UUID on Postgres,
// TEXT on SQLite, as sqlgen writes an Identity.UUID key.
func agentKeyType(db *rulestest.DB) string {
	if db.Dialect == rules.DialectPostgres {
		return "UUID"
	}
	return "TEXT"
}
