package apigen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// encryptedArgumentAPI is a TypeScript schema local to apigen's testdata:
// the CardMutations set is not Encrypted, storeCard takes an
// EncryptedField<string> argument and renameCard takes none.
const encryptedArgumentAPI = "encrypted-argument-api"

func loadEncryptedArgumentAPI(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("testdata", "services", encryptedArgumentAPI))
	if err != nil {
		t.Fatalf("load %s: %v", encryptedArgumentAPI, err)
	}
	return schema
}

func generateEncryptedArgumentAPI(schema *ir.Schema) (*apigen.APIOutput, error) {
	return apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: encryptedArgumentAPI,
		Clock:      goModuleClock,
	})
}

// TestAnEncryptedArgumentEncryptsItsOperation: the loader keeps the
// EncryptedField<T> of storeCard's number argument, and the operation is
// encrypted as one in an Encrypted set is, for the Go server and every SDK
// generator that reads the endpoint. renameCard in the same set is not.
func TestAnEncryptedArgumentEncryptsItsOperation(t *testing.T) {
	schema := loadEncryptedArgumentAPI(t)
	encrypted := map[string]bool{}
	for _, op := range schema.OperationSets[0].Operations {
		for _, arg := range op.Arguments {
			encrypted[op.Name+"."+arg.Name] = arg.Encrypted
		}
	}
	want := map[string]bool{
		"storeCard.customerId": false, "storeCard.number": true, "storeCard.label": false,
		"renameCard.id": false, "renameCard.label": false,
	}
	for key, w := range want {
		if encrypted[key] != w {
			t.Errorf("argument %s: Encrypted = %t, want %t", key, encrypted[key], w)
		}
	}

	output, err := generateEncryptedArgumentAPI(schema)
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	if !output.HasEncryptedEndpoints {
		t.Error("HasEncryptedEndpoints = false, want true")
	}
	for _, ep := range output.Endpoints {
		if ep.Encrypted != (ep.Name == "storeCard") {
			t.Errorf("endpoint %s: Encrypted = %t", ep.Name, ep.Encrypted)
		}
	}
}

// TestAnEncryptedArgumentOutsideTheBodyIsRefused: the envelope is the
// request body of a POST, PUT or PATCH operation. The loader refuses an
// EncryptedField<T> path parameter it can name from the rest path, a query
// parameter and an argument of another method; apigen refuses them too,
// and also a path parameter an operation without a rest path takes from
// its type (a string here).
func TestAnEncryptedArgumentOutsideTheBodyIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(op *ir.FieldDef, number *ir.ArgumentDef)
		want   string
	}{
		{"path parameter without a rest path", func(op *ir.FieldDef, _ *ir.ArgumentDef) { op.RestPath = "" },
			"argument number is EncryptedField<T> but is a path parameter"},
		{"query parameter", func(_ *ir.FieldDef, number *ir.ArgumentDef) { number.IsQuery = true },
			"argument number is EncryptedField<T> but is a query parameter"},
		{"GET", func(op *ir.FieldDef, _ *ir.ArgumentDef) { op.HTTPMethod = "GET" },
			"argument number is EncryptedField<T>, which only a POST, PUT or PATCH request body carries encrypted; GET is not one"},
		{"DELETE", func(op *ir.FieldDef, _ *ir.ArgumentDef) { op.HTTPMethod = "DELETE" },
			"argument number is EncryptedField<T>, which only a POST, PUT or PATCH request body carries encrypted; DELETE is not one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := loadEncryptedArgumentAPI(t)
			op := schema.OperationSets[0].Operations[0]
			if op.Name != "storeCard" || op.Arguments[1].Name != "number" {
				t.Fatalf("fixture changed: %s(%s)", op.Name, op.Arguments[1].Name)
			}
			tc.mutate(op, op.Arguments[1])
			_, err := generateEncryptedArgumentAPI(schema)
			if err == nil || !strings.Contains(err.Error(), "operation card.storeCard "+tc.want) {
				t.Fatalf("Generate = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestEncryptedArgumentRoutesDecrypt generates the Go types and API modules
// of encrypted-argument-api, copies
// testdata/encrypted_argument_routes_test.go into the API module and runs
// it: storeCard refuses a plain body and decodes its arguments from the
// decrypted envelope, and renameCard stays a plain JSON route.
func TestEncryptedArgumentRoutesDecrypt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	apiDir := writeGoAPIModule(t, loadEncryptedArgumentAPI(t), encryptedArgumentAPI)
	test, err := os.ReadFile(filepath.Join("testdata", "encrypted_argument_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "encrypted_argument_routes_test.go"), test, 0o644); err != nil {
		t.Fatal(err)
	}
	runGoAPIModule(t, apiDir)
}
