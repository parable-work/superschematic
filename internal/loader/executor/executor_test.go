package executor

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

func requireBun(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bun"); err != nil {
		testpaths.RequireOrSkipTS(t, "bun not on PATH; execution tests need it")
	}
}

func TestRunDocument_SideEffectingModuleFails(t *testing.T) {
	requireBun(t)
	servicePath := filepath.Join("testdata", "services", "fixture-sideeffect")

	_, err := RunDocument(servicePath, "deploy.values.ts")
	if err == nil {
		t.Fatal("RunDocument() succeeded for a module that fetches at import")
	}
	if !strings.Contains(err.Error(), "side-effect-free at import") {
		t.Errorf("error does not name the guard: %v", err)
	}
}
