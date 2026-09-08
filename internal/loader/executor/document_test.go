package executor

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDocument_Fixture(t *testing.T) {
	requireBun(t)
	servicePath := filepath.Join("testdata", "services", "fixture-deploy-values")

	res, err := RunDocument(servicePath, "deploy.values.ts")
	if err != nil {
		t.Fatalf("RunDocument() error: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(res.Document, &doc); err != nil {
		t.Fatalf("captured document is not JSON: %v", err)
	}

	// The harness crawl reports the module graph; at minimum the entry
	// module itself (EDR-0087 amendment 2).
	foundEntry := false
	for _, imp := range res.AuthoringImports {
		if strings.HasSuffix(imp, "deploy.values.ts") {
			foundEntry = true
		}
	}
	if !foundEntry {
		t.Errorf("authoringImports = %v, want the entry module listed", res.AuthoringImports)
	}

	config, ok := doc["config"].(map[string]any)
	if !ok || config["name"] != "fixture-deploy-values" {
		t.Fatalf("config = %v, want name fixture-deploy-values", doc["config"])
	}
	services, ok := doc["services"].([]any)
	if !ok || len(services) != 2 {
		t.Fatalf("services = %v, want two entries", doc["services"])
	}

	// The computed perService entries prove the module was executed, not
	// statically read: Object.fromEntries over the services list with a
	// derived port per service.
	base := doc["base"].(map[string]any)
	perService := base["perService"].(map[string]any)
	admin, ok := perService["web-admin-api"].(map[string]any)
	if !ok {
		t.Fatalf("perService = %v, want a web-admin-api entry", perService)
	}
	if port := admin["env"].(map[string]any)["PORT"]; port != float64(8081) {
		t.Errorf("web-admin-api PORT = %v, want 8081 (basePort + 1)", port)
	}
}

func TestRunDocument_MissingDefaultExportFails(t *testing.T) {
	requireBun(t)
	servicePath := filepath.Join("testdata", "services", "fixture-deploy-nodefault")

	_, err := RunDocument(servicePath, "deploy.values.ts")
	if err == nil {
		t.Fatal("RunDocument() succeeded for a module with no default export")
	}
	if !strings.Contains(err.Error(), "default-export") {
		t.Errorf("error does not name the missing default export: %v", err)
	}
}
