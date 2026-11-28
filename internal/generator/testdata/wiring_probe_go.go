// The app's side of TestGoServerWiresTheRuntimeEndToEnd, which writes it
// into the generated server's module as probe/main.go and runs it beside
// the server and its workers: it makes the test's schema of
// fixture-agent-runs-db, seeds the rows an app writes (people, roles,
// routes, an agent and a customer's grant), raises the event the
// notifier routes, and reads rows back as JSON for the test to check. The
// server and its workers are the generated ones; this is the app's code,
// through the ORM and the runtime, as an app writes it.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
	pool, err := pgxpool.New(ctx, os.Getenv("PROBE_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	switch args[0] {
	case "setup":
		// setup <schema> <create.sql>: the schema, and the database's
		// tables in it.
		if _, err := pool.Exec(ctx, "CREATE SCHEMA "+args[1]); err != nil {
			return err
		}
		createSQL, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		_, err = pool.Exec(ctx, "SET search_path TO "+args[1]+", public; "+string(createSQL))
		return err
	case "drop":
		_, err := pool.Exec(ctx, "DROP SCHEMA "+args[1]+" CASCADE")
		return err
	case "seed":
		return seed(ctx, pool)
	case "query":
		return query(ctx, pool, args[1], args[2:]...)
	}
	return fmt.Errorf("probe: no command %q", args[0])
}

func ptr[T any](v T) *T { return &v }

// seed writes what the app writes, raises agent.budgetWarning on the
// control row, and prints the ids and the customer's grant token.
func seed(ctx context.Context, pool *pgxpool.Pool) error {
	db, err := orm.ConnectWithPool(pool)
	if err != nil {
		return err
	}
	system := orm.AsSystem(ctx)
	exec := func(sql string, args ...any) error {
		_, err := pool.Exec(ctx, sql, args...)
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

// query prints the rows of sql, each a map by column, as JSON.
func query(ctx context.Context, pool *pgxpool.Pool, sql string, args ...string) error {
	values := make([]any, len(args))
	for i, a := range args {
		values[i] = a
	}
	rows, err := pool.Query(ctx, sql, values...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		row, err := rows.Values()
		if err != nil {
			return err
		}
		m := map[string]any{}
		for i, f := range rows.FieldDescriptions() {
			m[f.Name] = fmt.Sprint(row[i])
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
