package pysdkgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestAnEncryptedArgumentEncryptsTheBody: storeCard in apigen's
// encrypted-argument-api takes an EncryptedField<string> argument outside
// an Encrypted operation set, so the Python SDK sends its body as an
// encrypted envelope and takes the per-request key option; renameCard, in
// the same set, sends plain JSON. The TypeScript and Go SDK tests run the
// same operation against a server.
func TestAnEncryptedArgumentEncryptsTheBody(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", "encrypted-argument-api"))
	if err != nil {
		t.Fatalf("load encrypted-argument-api: %v", err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: "encrypted-argument-api",
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, "encrypted_argument_api_sdk", "encrypted_argument_api_types", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	encrypted := map[string]bool{}
	for _, ns := range sdkOutput.Namespaces {
		for _, ep := range ns.Endpoints {
			encrypted[ep.MethodName] = ep.HasEncryptedBody
		}
	}
	if len(encrypted) != 2 {
		t.Fatalf("endpoints = %v, want storeCard and renameCard", encrypted)
	}
	for name, got := range encrypted {
		want := name == "store_card"
		if got != want {
			t.Errorf("%s: HasEncryptedBody = %t, want %t", name, got, want)
		}
	}
}
