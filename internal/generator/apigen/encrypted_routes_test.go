package apigen_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
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

// TestAnEncryptedOperationWithoutABodyIsRefused: the envelope is the body
// of a POST, PUT or PATCH request, so an operation that its Encrypted set
// or @encrypted makes encrypted cannot be a GET or DELETE, whether the
// method is declared or is its set's default. The loader refuses a
// declared one; apigen refuses both. An operation without a method in a
// Mutations set is a POST and builds.
func TestAnEncryptedOperationWithoutABodyIsRefused(t *testing.T) {
	const refused = "operation vault.storeSecret is encrypted (an Encrypted operation set, @encrypted, or an EncryptedField<T> result), but a %s request has no body to encrypt"
	for _, tc := range []struct {
		name   string
		mutate func(set *ir.OperationSet, op *ir.FieldDef)
		method string
	}{
		{"Encrypted set, GET", func(_ *ir.OperationSet, op *ir.FieldDef) { op.HTTPMethod = "GET" }, "GET"},
		{"Encrypted set, DELETE", func(_ *ir.OperationSet, op *ir.FieldDef) { op.HTTPMethod = "DELETE" }, "DELETE"},
		{"Encrypted set, the default GET of a Queries set", func(set *ir.OperationSet, op *ir.FieldDef) {
			set.Name = "VaultQueries"
			op.HTTPMethod = ""
		}, "GET"},
		{"@encrypted, GET", func(set *ir.OperationSet, op *ir.FieldDef) {
			set.Encrypted = false
			op.Encrypted = true
			op.HTTPMethod = "GET"
		}, "GET"},
		{"Encrypted set, the default POST of a Mutations set", func(_ *ir.OperationSet, op *ir.FieldDef) { op.HTTPMethod = "" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := loader.LoadService(filepath.Join("testdata", "services", encryptedAPI))
			if err != nil {
				t.Fatalf("load %s: %v", encryptedAPI, err)
			}
			set := schema.OperationSets[0]
			op := set.Operations[0]
			if set.Name != "VaultMutations" || !set.Encrypted || op.Name != "storeSecret" {
				t.Fatalf("fixture changed: %s (Encrypted %t) %s", set.Name, set.Encrypted, op.Name)
			}
			tc.mutate(set, op)
			_, err = apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: encryptedAPI, Clock: goModuleClock})
			if tc.method == "" {
				if err != nil {
					t.Fatalf("Generate: %v", err)
				}
				return
			}
			want := fmt.Sprintf(refused, tc.method)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Generate = %v, want an error containing %q", err, want)
			}
		})
	}
}
