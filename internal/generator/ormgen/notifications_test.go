package ormgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteORMGoldenNotifications pins notifications.go (D153, D190):
// fixture-notifications-db's notifier tables by part with their columns,
// each table's key, events and to-one relations, its NoticeWork dispatch
// queue, and the repositories a record is read, found and updated
// through. A database that does not use the notifier writes none.
// Regenerate with
// go test ./internal/generator/ormgen -run TestWriteORMGoldenNotifications -update
func TestWriteORMGoldenNotifications(t *testing.T) {
	const svc = "fixture-notifications-db"
	output, dir := crossTablesOutput(t, svc)
	if output.Notifications == nil || !output.RequiresSchemaRuntime() {
		t.Fatalf("%s: Notifications %v", svc, output.Notifications)
	}
	if output.Notifications.Dispatch != "NoticeWork" || output.Notifications.Roles != "Role" || output.Notifications.Grants != "UserRoleGrant" {
		t.Errorf("the model: dispatch %q, roles %q, grants %q", output.Notifications.Dispatch, output.Notifications.Roles, output.Notifications.Grants)
	}
	var defs notifierDefinitions
	if err := json.Unmarshal([]byte(output.Notifications.Definitions), &defs); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"inbox", "deliveries", "routes", "preferences", "subscriptions", "channelTrust", "replies", "channelIdentities"} {
		if _, ok := defs.Parts[part]; !ok {
			t.Errorf("no part %s: %v", part, defs.Parts)
		}
	}
	if recipient := defs.Parts["inbox"].Columns["recipient"]; recipient.Name != "recipient" || !recipient.UUID {
		t.Errorf("the inbox's recipient column: %+v", recipient)
	}
	if events := defs.Tables["Ticket"].Events; len(events) != 4 || events[1].Kind != "ticket.overdue" || events[1].Urgency != "urgent" {
		t.Errorf("Ticket's events: %+v", events)
	}
	// The database lists sqlite (#618): each column says how a SQLite
	// column holds it.
	inbox := defs.Parts["inbox"].Columns
	if inbox["createdAt"].SQLite != "time" || inbox["payload"].SQLite != "json" || inbox["recipient"].SQLite != "" {
		t.Errorf("the inbox's columns on SQLite: %+v", inbox)
	}
	for _, name := range []string{"notifications.go", "notifications_sqlite.go"} {
		checkGoldenFile(t, dir, svc, name)
	}

	output, dir = crossTablesOutput(t, "fixture-db")
	if output.Notifications != nil {
		t.Errorf("fixture-db's Notifications: %+v", output.Notifications)
	}
	if _, err := os.Stat(filepath.Join(dir, "notifications.go")); !os.IsNotExist(err) {
		t.Errorf("fixture-db wrote notifications.go: %v", err)
	}
}

// checkGoldenFile compares the file name dir holds, the ORM of svc, with
// its golden copy, or rewrites the copy with -update.
func checkGoldenFile(t *testing.T, dir, svc, name string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", svc, name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil || string(got) != string(want) {
		t.Errorf("%s of %s differs from its golden (run with -update to accept): %v", name, svc, err)
	}
}
