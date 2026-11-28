package schemaconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocateFindsEachForm: a directory with one config gives its path and
// form.
func TestLocateFindsEachForm(t *testing.T) {
	for _, form := range Forms {
		dir := writeConfig(t, form.FileName(), "")
		path, got, ok, err := Locate(dir)
		if err != nil || !ok {
			t.Fatalf("%s: ok %v, err %v; want found", form, ok, err)
		}
		if got != form || path != filepath.Join(dir, form.FileName()) {
			t.Errorf("%s: got %s at %s", form, got, path)
		}
	}
}

// TestLocateFindsNone: a directory with no config, one whose config name
// is a directory, and a missing directory are no service, with no error.
func TestLocateFindsNone(t *testing.T) {
	empty := t.TempDir()
	named := t.TempDir()
	if err := os.Mkdir(filepath.Join(named, FormJSON.FileName()), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{empty, named, filepath.Join(empty, "missing")} {
		if path, form, ok, err := Locate(dir); ok || err != nil || path != "" || form != "" {
			t.Errorf("%s: got %q %q %v %v; want nothing", dir, path, form, ok, err)
		}
	}
}

// TestLocateRefusesTwoForms: a directory with two forms is an error naming
// both, and ReadFile reports it rather than os.ErrNotExist.
func TestLocateRefusesTwoForms(t *testing.T) {
	dir := writeConfig(t, FormTS.FileName(), "")
	if err := os.WriteFile(filepath.Join(dir, FormYAML.FileName()), []byte("name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, ok, err := Locate(dir)
	if ok || err == nil {
		t.Fatalf("ok %v, err %v; want an error", ok, err)
	}
	if !strings.Contains(err.Error(), "schema.config.ts and schema.config.yaml") {
		t.Errorf("error %q names neither file", err)
	}
	if _, err := ReadFile(dir, coreKinds); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadFile: %v; want the two-forms error", err)
	}
}

// TestReadFileLeavesTheTypeScriptForm: ReadFile reads no schema.config.ts
// and says so with os.ErrNotExist.
func TestReadFileLeavesTheTypeScriptForm(t *testing.T) {
	dir := writeConfig(t, FormTS.FileName(), "")
	if _, err := ReadFile(dir, coreKinds); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadFile: %v; want os.ErrNotExist", err)
	}
}
