package apigen_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAnEncryptedUploadIsRefused: the encrypted envelope carries a JSON
// body, so no server opens a multipart one sent as an envelope, and every
// SDK sends a file upload as multipart without encrypting it. apigen
// refuses an operation that is both, however it is encrypted, and builds
// the same upload unencrypted.
func TestAnEncryptedUploadIsRefused(t *testing.T) {
	const refused = "operation vault.uploadSecret is encrypted (an Encrypted operation set, @encrypted, or an EncryptedField<T> result or argument) and uploads files (file)"
	for _, tc := range []struct {
		name    string
		encrypt func(set *ir.OperationSet, op *ir.FieldDef)
		refused bool
	}{
		{"Encrypted set", func(*ir.OperationSet, *ir.FieldDef) {}, true},
		{"@encrypted", func(set *ir.OperationSet, op *ir.FieldDef) {
			set.Encrypted = false
			op.Encrypted = true
		}, true},
		{"EncryptedField<T> input argument", func(set *ir.OperationSet, op *ir.FieldDef) {
			set.Encrypted = false
			op.Arguments[0].Encrypted = true
		}, true},
		{"not encrypted", func(set *ir.OperationSet, _ *ir.FieldDef) { set.Encrypted = false }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := loader.LoadService(filepath.Join("testdata", "services", encryptedAPI))
			if err != nil {
				t.Fatalf("load %s: %v", encryptedAPI, err)
			}
			const fileScalar = "Upload.File"
			schema.Scalars[fileScalar] = &ir.ScalarDef{Name: fileScalar, LanguagePrimitive: ir.LanguageString, FileUpload: &ir.FileUploadConfig{}}
			schema.Types["SecretFile"] = &ir.TypeDef{
				Name:   "SecretFile",
				Role:   ir.RoleAPIInput,
				Fields: []*ir.FieldDef{{Name: "file", TypeRef: ir.TypeRef{Name: fileScalar}, Required: true}},
			}
			set := schema.OperationSets[0]
			if set.Name != "VaultMutations" || !set.Encrypted {
				t.Fatalf("fixture changed: %s (Encrypted %t)", set.Name, set.Encrypted)
			}
			// Only the upload: the set's other operations are encrypted
			// JSON routes, which tc.encrypt may leave encrypted.
			op := &ir.FieldDef{
				Name:       "uploadSecret",
				TypeRef:    ir.TypeRef{Name: "SecretReceipt"},
				Required:   true,
				HTTPMethod: "POST",
				RestPath:   "secrets/file",
				Arguments:  []*ir.ArgumentDef{{Name: "input", TypeRef: ir.TypeRef{Name: "SecretFile"}, Required: true}},
			}
			set.Operations = []*ir.FieldDef{op}
			tc.encrypt(set, op)
			output, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: encryptedAPI, Clock: goModuleClock})
			if !tc.refused {
				if err != nil {
					t.Fatalf("Generate: %v", err)
				}
				if !output.HasFileUpload {
					t.Fatal("the unencrypted upload has no file upload")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), refused) {
				t.Fatalf("Generate = %v, want an error containing %q", err, refused)
			}
		})
	}
}
