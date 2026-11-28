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
		// The database lists sqlite (#618): orchestration_sqlite.go runs
		// it there, and each column says how a SQLite column holds it.
		if messages := defs.Parts["messages"].Columns; messages["at"].SQLite != "time" {
			t.Errorf("%s: the messages' columns on SQLite: %+v", svc, messages)
		}
		names := []string{"orchestration.go", "orchestration_sqlite.go"}
		if svc == "fixture-agent-runs-db" {
			// The definitions are @versioned: on SQLite their repository
			// serves the rows and refuses the history reads (#618).
			names = append(names, "sqlite_agent.go")
		}
		for _, name := range names {
			checkGoldenFile(t, dir, svc, name)
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
