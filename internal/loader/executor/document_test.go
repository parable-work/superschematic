package executor

import (
	"encoding/json"
	"path/filepath"
	"slices"
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
	// module itself.
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

// TestRunDocument_SeesNoCredential: a sidecar document is the project's
// code, and the harness runs it with no credential the command holds in
// its environment (D47, amended).
func TestRunDocument_SeesNoCredential(t *testing.T) {
	requireBun(t)
	for _, name := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "CLOUDFLARE_API_TOKEN", "PULUMI_CONFIG_PASSPHRASE", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_URL"} {
		t.Setenv(name, "secret")
	}
	res, err := RunDocument(filepath.Join("testdata", "services", "fixture-deploy-env"), "deploy.values.ts")
	if err != nil {
		t.Fatalf("RunDocument() error: %v", err)
	}
	var doc struct {
		Env []string `json:"env"`
	}
	if err := json.Unmarshal(res.Document, &doc); err != nil {
		t.Fatalf("captured document is not JSON: %v", err)
	}
	for _, name := range doc.Env {
		if strings.HasPrefix(name, "GOOGLE_") || strings.HasPrefix(name, "CLOUDSDK_") || strings.HasPrefix(name, "CLOUDFLARE_") || strings.HasPrefix(name, "PULUMI_") || strings.HasPrefix(name, "ACTIONS_") {
			t.Errorf("the document reads %s", name)
		}
	}
	if !slices.Contains(doc.Env, "PATH") {
		t.Errorf("the document's environment %v has no PATH", doc.Env)
	}
}
