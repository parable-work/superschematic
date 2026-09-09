package ormgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRelationHydrationGuardBehavior compiles the generated utils.go (stdlib
// only) and asserts the cycle / explicit-nested / depth halves of
// enterRelationHydration, including observer events and sibling-path isolation.
func TestRelationHydrationGuardBehavior(t *testing.T) {
	output := generateFixtureDB(t)

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read generated orm dir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == "utils.go" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(outDir, entry.Name())); err != nil {
			t.Fatalf("remove %s: %v", entry.Name(), err)
		}
	}

	if err := os.WriteFile(filepath.Join(outDir, "go.mod"), []byte("module hydrationtest\n\ngo 1.26.4\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "hydration_guard_test.go"), []byte(relationHydrationGuardPackageTest), 0o644); err != nil {
		t.Fatalf("write hydration_guard_test.go: %v", err)
	}

	cmd := exec.Command("go", "test", ".", "-count=1", "-run", "TestRelationHydrationGuard")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=go1.26.4")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test hydration guard failed: %v\n%s", err, out)
	}
}

const relationHydrationGuardPackageTest = `package orm

import (
	"context"
	"fmt"
	"testing"
)

func TestRelationHydrationGuard_DefaultSelfRefRefusesCycle(t *testing.T) {
	defer SetRelationHydrationObserver(nil)

	var events []RelationHydrationGuardEvent
	SetRelationHydrationObserver(func(event RelationHydrationGuardEvent) {
		events = append(events, event)
	})

	ctx, allowed := enterRelationHydration(context.Background(), "Commit", "parentCommit", "Commit", true)
	if !allowed {
		t.Fatal("first default self-ref expansion must be allowed")
	}

	_, allowed = enterRelationHydration(ctx, "Commit", "parentCommit", "Commit", true)
	if allowed {
		t.Fatal("second default self-ref expansion must be refused")
	}
	if len(events) != 1 {
		t.Fatalf("observer events = %d, want 1", len(events))
	}
	if events[0].Reason != RelationHydrationCycle {
		t.Fatalf("reason = %q, want %q", events[0].Reason, RelationHydrationCycle)
	}
	if events[0].OwnerType != "Commit" || events[0].Relation != "parentCommit" || events[0].TargetType != "Commit" {
		t.Fatalf("event = %+v", events[0])
	}
}

func TestRelationHydrationGuard_ExplicitNestedStillExpands(t *testing.T) {
	defer SetRelationHydrationObserver(nil)

	var events []RelationHydrationGuardEvent
	SetRelationHydrationObserver(func(event RelationHydrationGuardEvent) {
		events = append(events, event)
	})

	ctx, allowed := enterRelationHydration(context.Background(), "Commit", "parentCommit", "Commit", false)
	if !allowed {
		t.Fatal("first explicit nested expansion must be allowed")
	}
	_, allowed = enterRelationHydration(ctx, "Commit", "parentCommit", "Commit", false)
	if !allowed {
		t.Fatal("second explicit nested expansion must still be allowed")
	}
	if len(events) != 0 {
		t.Fatalf("explicit nested must not fire the cycle observer, got %+v", events)
	}
}

func TestRelationHydrationGuard_DepthLimitRefuses(t *testing.T) {
	defer SetRelationHydrationObserver(nil)

	var events []RelationHydrationGuardEvent
	SetRelationHydrationObserver(func(event RelationHydrationGuardEvent) {
		events = append(events, event)
	})

	ctx := context.Background()
	for i := 0; i < MaxRelationHydrationDepth; i++ {
		owner := fmt.Sprintf("Type%d", i)
		target := fmt.Sprintf("Type%d", i+1)
		next, allowed := enterRelationHydration(ctx, owner, "rel", target, false)
		if !allowed {
			t.Fatalf("depth %d expansion must be allowed", i)
		}
		ctx = next
	}

	_, allowed := enterRelationHydration(ctx, "TypeOverflow", "rel", "TypePastLimit", false)
	if allowed {
		t.Fatal("expansion past MaxRelationHydrationDepth must be refused")
	}
	if len(events) != 1 {
		t.Fatalf("observer events = %d, want 1", len(events))
	}
	if events[0].Reason != RelationHydrationDepth {
		t.Fatalf("reason = %q, want %q", events[0].Reason, RelationHydrationDepth)
	}
}

func TestRelationHydrationGuard_SiblingBranchesIsolatePath(t *testing.T) {
	defer SetRelationHydrationObserver(nil)

	root, allowed := enterRelationHydration(context.Background(), "Parent", "child", "Child", true)
	if !allowed {
		t.Fatal("root expansion must be allowed")
	}

	branchOne, allowed := enterRelationHydration(root, "Child", "grand", "Grand", true)
	if !allowed {
		t.Fatal("first sibling branch must be allowed")
	}
	_, allowed = enterRelationHydration(branchOne, "Grand", "leaf", "Leaf", true)
	if !allowed {
		t.Fatal("deeper first sibling must be allowed")
	}

	// Sibling starts from root's path, not branchOne's -- Grand is not visible.
	_, allowed = enterRelationHydration(root, "Child", "other", "Other", true)
	if !allowed {
		t.Fatal("sibling branch must not inherit the other branch's path")
	}
}
`
