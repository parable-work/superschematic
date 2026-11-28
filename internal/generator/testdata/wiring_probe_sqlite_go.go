// The app's side of TestGoServerWiresTheRuntimeOnSQLiteEndToEnd (#717),
// as testdata/wiring_probe_go.go is TestGoServerWiresTheRuntimeEndToEnd's
// on Postgres: it makes fixture-agent-runs-db's SQLite file from the
// build's SQLite DDL, seeds the rows an app writes (people, roles, routes,
// an agent and a customer's grant), raises the event the notifier routes,
// and reads rows back as JSON for the test to check. The server and its
// workers are the generated ones, over the same file; this is the app's
// code, through the ORM's SQLite database and the runtime, as an app
// writes it.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/parable-work/superschematic/runtime/http/go/agents"
	"github.com/parable-work/superschematic/runtime/http/go/notify"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/orchestration"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"

	orm "example.com/schemas/orm/fixture-agent-runs-db"
	types "example.com/schemas/types/go/fixture-agent-runs-db"
)

// phoneAnn is the number Ann verified, which the notifier texts and her
// replies come from.
const phoneAnn = "+15550100001"

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	path, ok := strings.CutPrefix(os.Getenv("PROBE_DATABASE_URL"), "sqlite:")
	if !ok {
		return fmt.Errorf("probe: PROBE_DATABASE_URL is no sqlite:<path>")
	}
	raw, err := sql.Open("sqlite", orm.SQLiteDSN(path))
	if err != nil {
		return err
	}
	defer raw.Close()
	switch args[0] {
	case "setup":
		// setup <ddl>: the file, and the database's tables in it.
		ddl, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		_, err = raw.ExecContext(ctx, string(ddl))
		return err
	case "seed":
		return seed(ctx, raw)
	case "query":
		return query(ctx, raw, args[1], args[2:]...)
	}
	return fmt.Errorf("probe: no command %q", args[0])
}

func ptr[T any](v T) *T { return &v }

var arrayLiteral = regexp.MustCompile(`'\{([^}']*)\}'`)

// sqliteWords is a statement in Postgres's words in SQLite's: $n as ?n,
// now() as the clock the schema's defaults write, an array literal as JSON
// text, and no casts to jsonb.
func sqliteWords(text string) string {
	text = strings.ReplaceAll(text, "now()", "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')")
	text = strings.ReplaceAll(text, "::jsonb", "")
	text = arrayLiteral.ReplaceAllStringFunc(text, func(literal string) string {
		inner := strings.Trim(literal, "'{}")
		if inner == "" {
			return "'[]'"
		}
		parts := strings.Split(inner, ",")
		for i, p := range parts {
			parts[i] = `"` + p + `"`
		}
		return "'[" + strings.Join(parts, ",") + "]'"
	})
	for i := 9; i >= 1; i-- {
		text = strings.ReplaceAll(text, fmt.Sprintf("$%d", i), fmt.Sprintf("?%d", i))
	}
	return text
}

// seed writes what the app writes, raises agent.budgetWarning on the
// control row, and prints the ids and the customer's grant token, as the
// Postgres probe does.
func seed(ctx context.Context, raw *sql.DB) error {
	db, err := orm.OpenSQLite(ctx, raw)
	if err != nil {
		return err
	}
	system := orm.AsSystem(ctx)
	exec := func(text string, args ...any) error {
		_, err := raw.ExecContext(ctx, sqliteWords(text), args...)
		return err
	}
	ann, err := db.GetAccountRepository().CreateOne(system, &types.Account{Email: "ann@example.test"})
	if err != nil {
		return err
	}
	boss, err := db.GetAccountRepository().CreateOne(system, &types.Account{Email: "boss@example.test"})
	if err != nil {
		return err
	}
	role := func(name string, permissions, reaches []string) (*types.Role, error) {
		return db.GetRoleRepository().CreateOne(system, &types.Role{Name: types.IdentitySlug(name), Permissions: permissions, Reaches: reaches})
	}
	approver, err := role("approver", []string{"agents.approve", "agents"}, nil)
	if err != nil {
		return err
	}
	for _, r := range []struct {
		name           string
		perms, reaches []string
	}{{"worker-agent", []string{"tasks.read"}, []string{"TaskNote:task"}}, {"customer", []string{"tasks.read"}, []string{}}} {
		if _, err := role(r.name, r.perms, r.reaches); err != nil {
			return err
		}
	}
	if _, err := db.GetUserRoleGrantRepository().CreateOne(system, &types.UserRoleGrant{User: types.Account{Id: ptr(*ann.Id)}, Role: types.Role{Id: approver.Id}, GrantedAt: types.TemporalDateTime(time.Now())}); err != nil {
		return err
	}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO channel_identity (kind, address, principal_kind, principal, trust, verified_at) VALUES ('phone', $1, 'user', $2, 'verified', now())`, []any{phoneAnn, rowrefs.UUIDToDB(ann.Id.String())}},
		{`INSERT INTO notice_preference (principal, event_kind, urgency, channels) VALUES (NULL, '*', '*', '{sms}')`, nil},
		{`INSERT INTO channel_trust (channel_kind, level, permissions) VALUES ('sms', 'standard', '{agents.approve}')`, nil},
		{`INSERT INTO notice_route (name, event_kind, recipients) VALUES ('Budget warnings go to approvers', 'agent.budgetWarning', '[{"role":"approver"}]'::jsonb), ('Budget warnings wake the reviewer', 'agent.budgetWarning', '[{"agent":"reviewer"}]'::jsonb)`, nil},
	} {
		if err := exec(statement.sql, statement.args...); err != nil {
			return fmt.Errorf("%s: %w", statement.sql, err)
		}
	}
	limit := types.GenericInt64(100000)
	total, err := db.GetAgentBudgetRepository().CreateOne(system, &orm.AgentBudgetCreate{Name: "every agent", BudgetLimit: &limit})
	if err != nil {
		return err
	}
	control, err := db.GetAgentControlRepository().CreateOne(system, &orm.AgentControlCreate{Total: &types.AgentBudget{Id: total.Id}, WarnAt: 80})
	if err != nil {
		return err
	}
	// The agent the second route wakes, defined as the app defines one.
	definer, err := agents.New(db.Orchestration(), agents.Options{Endpoint: "http://127.0.0.1/api/agents"})
	if err != nil {
		return err
	}
	manager := orchestration.Principal{Principal: access.Principal{Subject: boss.Id.String(), Kind: access.KindUser}, Permissions: []string{"agents"}}
	if _, err := definer.Define(ctx, manager, map[string]any{"name": "reviewer", "role": "worker-agent", "tools": []string{}}); err != nil {
		return err
	}
	// A task, which its write puts on the search index's queue, and a
	// customer whose grant reaches it.
	task, err := db.GetTaskRepository().CreateOne(system, &orm.TaskCreate{Title: "Pump overhaul", Details: "replace the pump seal", State: "open"})
	if err != nil {
		return err
	}
	customer, err := db.GetCustomerRepository().CreateOne(system, &types.Customer{Name: "Cy"})
	if err != nil {
		return err
	}
	_, token, err := orm.IssueGrant(system, db, access.IssueGrantInput{Holder: customer.Id.String(), HolderKind: "party", Role: "customer", TargetType: "Task", TargetID: task.Id.String()})
	if err != nil {
		return err
	}
	// The app raises the event; the generated worker routes it.
	raiser, err := notify.New(db.Notifications(), notify.Options{})
	if err != nil {
		return err
	}
	event, err := raiser.Raise(ctx, nil, notify.RaiseInput{Table: "AgentControl", Key: control.Id.String(), Kind: "agent.budgetWarning"})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"ann": ann.Id.String(), "task": task.Id.String(), "token": token, "event": event, "control": control.Id.String()})
}

// query prints the rows of text, each a map by column, as JSON.
func query(ctx context.Context, raw *sql.DB, text string, args ...string) error {
	values := make([]any, len(args))
	for i, a := range args {
		values[i] = a
	}
	rows, err := raw.QueryContext(ctx, sqliteWords(text), values...)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for rows.Next() {
		cells := make([]any, len(columns))
		targets := make([]any, len(cells))
		for i := range cells {
			targets[i] = &cells[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return err
		}
		m := map[string]any{}
		for i, name := range columns {
			if b, ok := cells[i].([]byte); ok {
				cells[i] = string(b)
			}
			m[name] = fmt.Sprint(cells[i])
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
