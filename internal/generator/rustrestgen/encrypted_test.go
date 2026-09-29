package rustrestgen

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
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

func generateSecretAPI(schema *ir.Schema) (*APIOutput, error) {
	return Generate(schema, Options{
		AuthProvider: sessionauth.Provider{},
		SchemaName:   "secret-api",
		TypesCrate:   "schemas-secret-api-types",
		Clock:        codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
}

// TestEncryptedOperationsAreRefused: the Rust router has no step that
// decrypts a request body, so it would hand the envelope to the
// implementation as if it were the body. An encrypted operation (in an
// Encrypted operation set, @encrypted, or taking an EncryptedField<T>
// argument) fails the build with the operation and the fix named, as the
// TypeScript server's generator does.
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
			_, err := generateSecretAPI(secretAPI(tc.setEncrypted, tc.opEncrypted, tc.argEncrypted, false))
			if err == nil {
				t.Fatal("Generate accepted an encrypted operation")
			}
			for _, want := range []string{"rustrestgen: operation secret.saveSecret is encrypted", "@manualRouteRegistration", "decrypt"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

// TestEncryptedManualOperationReachesItsImplementation: an encrypted
// operation declared @manualRouteRegistration builds. The Rust router
// mounts it as it mounts every operation and hands the request body, the
// envelope, to the implementation, which decrypts it.
func TestEncryptedManualOperationReachesItsImplementation(t *testing.T) {
	output, err := generateSecretAPI(secretAPI(true, false, false, true))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(output.Endpoints) != 1 || output.Endpoints[0].Name != "saveSecret" || !output.Endpoints[0].HasInput {
		t.Errorf("endpoints %+v; want saveSecret with its body", output.Endpoints)
	}
}

// TestFixtureAPIIsRefusedForItsEncryptedMutations: fixture-api as declared
// has an Encrypted TenantMutations set with two operations that are not
// @manualRouteRegistration, so the Rust server refuses it at the first,
// createTenant. The golden tests generate it without the encryption.
func TestFixtureAPIIsRefusedForItsEncryptedMutations(t *testing.T) {
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	_, err = Generate(apiSchema, Options{
		AuthProvider:   sessionauth.Provider{},
		SchemaName:     "fixture-api",
		IsPublic:       true,
		UpstreamSchema: "fixture-db",
		UpstreamIR:     dbSchema,
		TypesCrate:     "schemas-fixture-api-types",
		Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err == nil || !strings.Contains(err.Error(), "operation tenant.createTenant is encrypted") {
		t.Fatalf("Generate = %v, want the refusal of tenant.createTenant", err)
	}
}
