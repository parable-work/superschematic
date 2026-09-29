package apigen_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// encryptedAPI is a schema local to apigen's testdata: an Encrypted
// operation set whose routes also carry a permission, a body limit, a rate
// limit and a timeout.
const encryptedAPI = "encrypted-api"

// TestEncryptedRoutesRefuseBeforeDecrypting generates the Go types and API
// modules of encrypted-api, copies testdata/encrypted_routes_test.go into
// the API module and runs it. A route runs the webhook signature check, the
// rate limit, the body limit and the permission check before the payload
// decryptor, so a request one of them refuses never reaches the decryptor,
// and the timeout after it.
func TestEncryptedRoutesRefuseBeforeDecrypting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join("testdata", "services", encryptedAPI))
	if err != nil {
		t.Fatalf("load %s: %v", encryptedAPI, err)
	}
	apiDir := writeGoAPIModule(t, schema, encryptedAPI)
	test, err := os.ReadFile(filepath.Join("testdata", "encrypted_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "encrypted_routes_test.go"), test, 0o644); err != nil {
		t.Fatal(err)
	}
	runGoAPIModule(t, apiDir)
}
