package ormgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteORMGoldenOrchestration pins orchestration.go (D157, D158,
// D192): fixture-parties-db's conversations, its Contact party and its
// RunRequest queue, and fixture-agent-runs-db's agents' tables by part
// with their columns, its runs' Lease, Retries and status uses, its
// controls' Budget and the relation to every agent's budget, its
// versioned definitions and its AgentRunRequest queue. A database with
// neither parties nor agents writes none. Regenerate with
// go test ./internal/generator/ormgen -run TestWriteORMGoldenOrchestration -update
func TestWriteORMGoldenOrchestration(t *testing.T) {
	for _, svc := range []string{"fixture-parties-db", "fixture-agent-runs-db"} {
		output, dir := crossTablesOutput(t, svc)
		if output.Orchestration == nil {
			t.Fatalf("%s: no Orchestration", svc)
		}
		var defs orchestrationDefinitions
		if err := json.Unmarshal([]byte(output.Orchestration.Definitions), &defs); err != nil {
			t.Fatal(err)
		}
		if defs.RunRequests != "RunRequest" {
			t.Errorf("%s: the run requests queue %q", svc, defs.RunRequests)
		}
		for _, part := range []string{"conversations", "messages"} {
			if _, ok := defs.Parts[part]; !ok {
				t.Errorf("%s: no part %s", svc, part)
			}
		}
		if svc == "fixture-parties-db" {
			if defs.Parties["Contact"].Title != "name" || defs.Requests != "" {
				t.Errorf("%s: the parties %+v, the requests %q", svc, defs.Parties, defs.Requests)
			}
		} else {
			for _, part := range []string{"definitions", "runs", "policies", "approvals", "controls"} {
				if _, ok := defs.Parts[part]; !ok {
					t.Errorf("%s: no part %s", svc, part)
				}
			}
			runs := defs.Tables["AgentRun"]
			if runs.Uses["lease"] != "Lease" || runs.Uses["retries"] != "Retries" || runs.Uses["status"] != "status" {
				t.Errorf("the runs' uses: %+v", runs.Uses)
			}
			controls := defs.Tables["AgentControl"]
			if controls.Uses["budget"] != "Budget" || controls.Relations["total"].Target != "AgentBudget" || controls.Relations["total"].Column != "total" {
				t.Errorf("the controls: %+v", controls)
			}
			if !defs.Tables["Agent"].Versioned || defs.Requests != "AgentRunRequest" {
				t.Errorf("the definitions %+v, the requests %q", defs.Tables["Agent"], defs.Requests)
			}
		}
		got, err := os.ReadFile(filepath.Join(dir, "orchestration.go"))
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join("testdata", "golden", svc, "orchestration.go")
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil || string(got) != string(want) {
			t.Errorf("orchestration.go of %s differs from its golden (run with -update to accept): %v", svc, err)
		}
	}

	output, dir := crossTablesOutput(t, "fixture-notifications-db")
	if output.Orchestration != nil {
		t.Errorf("fixture-notifications-db's Orchestration: %+v", output.Orchestration)
	}
	if _, err := os.Stat(filepath.Join(dir, "orchestration.go")); !os.IsNotExist(err) {
		t.Errorf("fixture-notifications-db wrote orchestration.go: %v", err)
	}
}

// TestOrchestrationBesideSQLite checks that a database that also lists the
// sqlite dialect, as fixture-parties-db and fixture-agent-runs-db do for
// TypeScript's runs on SQLite (D189), still writes orchestration.go, the
// same as its golden: the Go host is Database's, on Postgres (D192), and
// the tables on SQLite are SQLiteDatabase's, which hosts none.
func TestOrchestrationBesideSQLite(t *testing.T) {
	for _, svc := range []string{"fixture-parties-db", "fixture-agent-runs-db"} {
		output, dir := ormOutputOf(t, svc, true)
		if output.Orchestration == nil || !output.SQLite {
			t.Fatalf("%s on SQLite too: Orchestration %v, SQLite %v", svc, output.Orchestration, output.SQLite)
		}
		got, err := os.ReadFile(filepath.Join(dir, "orchestration.go"))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join("testdata", "golden", svc, "orchestration.go"))
		if err != nil || string(got) != string(want) {
			t.Errorf("orchestration.go of %s beside SQLite differs from its golden: %v", svc, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "sqlite.go")); err != nil {
			t.Errorf("%s on SQLite too wrote no sqlite.go: %v", svc, err)
		}
	}
}
