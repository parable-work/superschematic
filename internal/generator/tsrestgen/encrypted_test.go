package tsrestgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// secretAPI is a schema with one operation, saveSecret, in the operation set
// SecretMutations: in an Encrypted set, @encrypted itself, taking its secret
// argument as an EncryptedField<string>, or @manualRouteRegistration as the
// flags say.
func secretAPI(setEncrypted, opEncrypted, argEncrypted, manual bool) *ir.Schema {
	schema := ir.NewSchema("secret-api", ir.SchemaKindAPI)
	schema.OperationSets = []*ir.OperationSet{{
		Name:      "SecretMutations",
		Encrypted: setEncrypted,
		Operations: []*ir.FieldDef{{
			Name:                    "saveSecret",
			HTTPMethod:              "POST",
			RestPath:                "secrets",
			TypeRef:                 ir.TypeRef{Name: "boolean"},
			Required:                true,
			Encrypted:               opEncrypted,
			ManualRouteRegistration: manual,
			Arguments:               []*ir.ArgumentDef{{Name: "secret", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Encrypted: argEncrypted}},
		}},
	}}
	return schema
}

func generateSecretAPI(t *testing.T, schema *ir.Schema) (*APIOutput, error) {
	t.Helper()
	endpoints, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: "secret-api", Clock: fixedClock})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return Generate(schema, endpoints, Options{SchemaName: "secret-api", Clock: fixedClock})
}

// TestEncryptedOperationsAreRefused: the TypeScript router has no step that
// decrypts a request body, so it would hand the ciphertext to the body
// parser. An encrypted operation (in an Encrypted operation set, @encrypted,
// or taking an EncryptedField<T> argument) fails the build with the
// operation and the fix named, as a file upload does.
func TestEncryptedOperationsAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		setEncrypted, opEncrypted, argEncrypted bool
	}{
		{"Encrypted operation set", true, false, false},
		{"@encrypted operation", false, true, false},
		{"EncryptedField<T> argument", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := generateSecretAPI(t, secretAPI(tc.setEncrypted, tc.opEncrypted, tc.argEncrypted, false))
			if err == nil {
				t.Fatal("Generate accepted an encrypted operation")
			}
			for _, want := range []string{"tsrestgen: operation secret.saveSecret is encrypted", "@manualRouteRegistration", "decrypt"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

// TestEncryptedManualOperationIsMountedByHand: an encrypted operation
// declared @manualRouteRegistration builds. The router gates it and hands
// the request to the service's handler, which decrypts the payload.
func TestEncryptedManualOperationIsMountedByHand(t *testing.T) {
	output, err := generateSecretAPI(t, secretAPI(true, false, false, true))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(output.ManualEndpoints) != 1 || output.ManualEndpoints[0].Name != "saveSecret" || len(output.Namespaces) != 0 {
		t.Errorf("manual endpoints %+v, namespaces %+v; want saveSecret mounted by hand only", output.ManualEndpoints, output.Namespaces)
	}
}

// TestFixtureAPIIsRefusedForItsEncryptedMutations: fixture-api as declared
// has an Encrypted TenantMutations set with two routed operations, so the
// TypeScript server refuses it at the first, createTenant.
func TestFixtureAPIIsRefusedForItsEncryptedMutations(t *testing.T) {
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	_, err = Generate(apiSchema, extractEndpoints(t, apiSchema, dbSchema), Options{
		SchemaName:   "fixture-api",
		Dependencies: map[string]*ir.Schema{"fixture-db": dbSchema},
		Clock:        fixedClock,
	})
	if err == nil || !strings.Contains(err.Error(), "operation tenant.createTenant is encrypted") {
		t.Fatalf("Generate = %v, want the refusal of tenant.createTenant", err)
	}
}
