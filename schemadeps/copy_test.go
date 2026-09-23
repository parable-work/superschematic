package schemadeps

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmitFromDist_WritesCopy(t *testing.T) {
	dist := t.TempDir()
	writeTSPackage(t, dist, "types/typescript/enums", "@schemas/enums-types", nil)
	copyPath := filepath.Join(t.TempDir(), "schemas", "deps.json")

	if err := EmitFromDist(dist, map[string]string{"types/typescript/enums": "enums"}, copyPath); err != nil {
		t.Fatalf("EmitFromDist: %v", err)
	}
	distBytes, err := os.ReadFile(DepsPath(dist))
	if err != nil {
		t.Fatal(err)
	}
	copyBytes, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(distBytes, copyBytes) {
		t.Fatalf("copy differs from the dist graph:\n%s\n---\n%s", distBytes, copyBytes)
	}
	if !strings.Contains(string(copyBytes), `"service": "enums"`) {
		t.Fatalf("copy lacks the service field:\n%s", copyBytes)
	}
}

func TestEmitFromDist_EmptyCopyPathWritesDistOnly(t *testing.T) {
	dist := t.TempDir()
	writeTSPackage(t, dist, "types/typescript/enums", "@schemas/enums-types", nil)

	if err := EmitFromDist(dist, nil, ""); err != nil {
		t.Fatalf("EmitFromDist: %v", err)
	}
	if _, err := os.Stat(DepsPath(dist)); err != nil {
		t.Fatalf("dist graph missing: %v", err)
	}
}

func TestEmitFromDist_OrphanWritesNothing(t *testing.T) {
	dist := t.TempDir()
	writeTSPackage(t, dist, "types/typescript/removed", "@schemas/removed-types", nil)
	copyPath := filepath.Join(t.TempDir(), "deps.json")

	if err := EmitFromDist(dist, map[string]string{}, copyPath); err == nil {
		t.Fatal("expected an error for a package no schema service produced")
	}
	for _, path := range []string{DepsPath(dist), copyPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s must not be written when collection fails (stat err %v)", path, err)
		}
	}
}

func TestSyncCopy(t *testing.T) {
	dir := t.TempDir()
	distPath := filepath.Join(dir, "dist", DepsFileName)
	copyPath := filepath.Join(dir, "deps.json")
	g := &Graph{Packages: []Package{{
		ID: "enums-types", Language: "go", Kind: "types",
		Name: "example.com/schemas/types/go/enums",
		Path: "types/go/enums", Service: "enums",
	}}}
	if err := Write(distPath, g); err != nil {
		t.Fatal(err)
	}

	// Check mode with no copy fails, names the file and the fix, and writes
	// nothing.
	err := SyncCopy(distPath, copyPath, true)
	if err == nil {
		t.Fatal("check mode must fail when the copy is missing")
	}
	for _, want := range []string{"deps.json", "run build-all"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should mention %q", err, want)
		}
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("check mode wrote the copy (stat err %v)", err)
	}

	// Sync mode writes it byte-identical.
	if err := SyncCopy(distPath, copyPath, false); err != nil {
		t.Fatalf("sync: %v", err)
	}
	want, err := os.ReadFile(distPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("copy differs from the dist graph")
	}

	// Check mode with an identical copy passes.
	if err := SyncCopy(distPath, copyPath, true); err != nil {
		t.Fatalf("check with an identical copy: %v", err)
	}

	// A stale copy fails check mode and is rewritten by sync mode.
	if err := os.WriteFile(copyPath, []byte("{\"version\": 1, \"packages\": []}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SyncCopy(distPath, copyPath, true); err == nil {
		t.Fatal("check mode must fail when the copy is stale")
	}
	if err := SyncCopy(distPath, copyPath, false); err != nil {
		t.Fatalf("resync: %v", err)
	}
	got, err = os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("resync did not restore the copy")
	}
}

func TestSyncCopy_MissingDistIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := SyncCopy(filepath.Join(dir, DepsFileName), filepath.Join(dir, "deps.json"), false); err == nil {
		t.Fatal("expected an error when the dist graph is missing")
	}
}
