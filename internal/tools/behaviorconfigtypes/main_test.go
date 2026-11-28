package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestCommittedFileIsCurrent holds the committed file to what the command
// writes, as -check does in CI.
func TestCommittedFileIsCurrent(t *testing.T) {
	want, err := render()
	if err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile(filepath.Join(testpaths.RepoRoot(t), outputPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(have) != string(want) {
		t.Errorf("%s is out of date; run: go run ./internal/tools/behaviorconfigtypes", outputPath)
	}
}

// TestBehaviorsAreTheEngines: the file covers the behaviors an engine
// runs that D103 has not folded, and none the behaviors' core alone runs.
func TestBehaviorsAreTheEngines(t *testing.T) {
	names := map[string]bool{}
	for _, b := range engineBehaviors() {
		names[b.Name] = true
	}
	for _, name := range []string{"Workflow", "Comments", "Lease", "Payments"} {
		if !names[name] {
			t.Errorf("%s is missing", name)
		}
	}
	for _, name := range []string{"Links", "Search", "Queue", "Branches", "Seats", "Schedules", "Recurrence"} {
		if names[name] {
			t.Errorf("%s is listed", name)
		}
	}
	if len(names) != 24 {
		t.Errorf("%d behaviors, want the 24 BehaviorConfigs lists", len(names))
	}
}
