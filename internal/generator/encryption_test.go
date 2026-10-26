package generator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// vaultDB is a DB schema with an Encrypted<T> field (D85).
func vaultDB() *ir.Schema {
	return &ir.Schema{Name: "vault-db", Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{
		"PlaidItem": {Name: "PlaidItem", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
			{Name: "accessToken", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true, EncryptedAtRest: true},
		}},
	}}
}

// vaultAPI is an API whose @envVars class holds fields.
func vaultAPI(fields ...*ir.FieldDef) *ir.Schema {
	return &ir.Schema{Name: "vault-api", Kind: ir.SchemaKindAPI, Types: map[string]*ir.TypeDef{
		"VaultApiConfig": {Name: "VaultApiConfig", EnvVars: true, Fields: fields},
	}}
}

// vaultRun is a run of api whose database's config holds outputs.
func vaultRun(t *testing.T, api *ir.Schema, dbOutputs string) run {
	t.Helper()
	reg, err := assembleCore(naming.Default())
	if err != nil {
		t.Fatal(err)
	}
	var outputs map[string]any
	if err := json.Unmarshal([]byte(dbOutputs), &outputs); err != nil {
		t.Fatal(err)
	}
	cfg := &schemaconfig.SchemaConfig{Name: "vault-db", Kind: "DB", Outputs: outputs}
	return run{registry.GenerateContext{
		Schema:   api,
		Config:   &schemaconfig.SchemaConfig{Name: api.Name},
		Registry: reg,
		Options: registry.Options{DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
			return cfg, name == "vault-db"
		}},
	}}
}

// TestAnAPIOnAnEncryptedDatabaseHoldsItsKey: an API whose database has
// Encrypted<T> fields opens its ORM with the secret the database's config
// names, by the type that declares the field and its name, so the API's
// @envVars must hold that field, a required Secret<string>, and the build
// refuses it otherwise, naming the fix; a database whose config names no
// key is refused too, at its own build and at the API's (D85).
func TestAnAPIOnAnEncryptedDatabaseHoldsItsKey(t *testing.T) {
	key := `{"sql": {"encryption": {"key": "VaultKeys.VAULT_DB_FIELD_KEYS"}}}`
	inherited := func(f ir.FieldDef) *ir.FieldDef { return &f }
	good := ir.FieldDef{Name: "VAULT_DB_FIELD_KEYS", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true, InheritedFrom: "VaultKeys"}

	r := vaultRun(t, vaultAPI(inherited(good)), key)
	field, err := r.fieldKeysField("vault-db", vaultDB())
	if err != nil || field == nil || field.Name != "VAULT_DB_FIELD_KEYS" {
		t.Fatalf("fieldKeysField = %v, %v", field, err)
	}
	if field, err := r.fieldKeysField("vault-db", &ir.Schema{Name: "vault-db", Kind: ir.SchemaKindDB}); field != nil || err != nil {
		t.Errorf("a database without encrypted fields needs no key: %v, %v", field, err)
	}

	declaredHere := good
	declaredHere.InheritedFrom = ""
	notSecret := good
	notSecret.Secret = false
	optional := good
	optional.Required = false
	for _, tc := range []struct {
		name    string
		api     *ir.Schema
		outputs string
		want    string
	}{
		{"no field", vaultAPI(), key, "its @envVars class holds no field VAULT_DB_FIELD_KEYS; declare VAULT_DB_FIELD_KEYS: Secret<string> in a class VaultKeys, and make vault-api's @envVars class VaultKeys or extend it"},
		{"another secret", vaultAPI(&declaredHere), key, "its @envVars field VAULT_DB_FIELD_KEYS is declared by VaultApiConfig, so it is the secret VaultApiConfig.VAULT_DB_FIELD_KEYS"},
		{"no secret", vaultAPI(&notSecret), key, "is no required Secret<string>"},
		{"optional", vaultAPI(&optional), key, "is no required Secret<string>"},
		{"no key", vaultAPI(inherited(good)), `{"sql": {}}`, `vault-db has Encrypted<T> fields, whose keys come from an @envVars Secret<string> its config names; set outputs.sql.encryption.key in vault-db's config to the field's declaring type and name, such as "FieldKeys.VAULT_DB_FIELD_KEYS"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := vaultRun(t, tc.api, tc.outputs).fieldKeysField("vault-db", vaultDB())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("fieldKeysField = %v, want it to say %q", err, tc.want)
			}
			if tc.name != "no key" && !strings.Contains(err.Error(), "vault-api opens the ORM of vault-db, whose Encrypted<T> fields take their keys from the secret VaultKeys.VAULT_DB_FIELD_KEYS (outputs.sql.encryption.key in vault-db's config)") {
				t.Errorf("the error does not say why: %v", err)
			}
		})
	}

	db := run{registry.GenerateContext{Schema: vaultDB(), Config: &schemaconfig.SchemaConfig{Name: "vault-db"}, Outputs: &registry.Outputs{}}}
	if err := db.checkEncryptionKey(); err == nil || !strings.Contains(err.Error(), "set outputs.sql.encryption.key in vault-db's config") {
		t.Errorf("the database's build = %v", err)
	}
	db.Outputs = &registry.Outputs{SQL: &registry.SQLOutputConfig{Encryption: &registry.SQLEncryptionConfig{Key: "VaultKeys.VAULT_DB_FIELD_KEYS"}}}
	if err := db.checkEncryptionKey(); err != nil {
		t.Errorf("the database's build with a key = %v", err)
	}
}

// TestTheEncryptionKeyIsATypeAndAField: outputs.sql.encryption.key names a
// secret as a stack does, its declaring type, a dot and its field.
func TestTheEncryptionKeyIsATypeAndAField(t *testing.T) {
	reg, err := assembleCore(naming.Default())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		key string
		ok  bool
	}{
		{"VaultKeys.VAULT_DB_FIELD_KEYS", true},
		{"VAULT_DB_FIELD_KEYS", false},
		{"Vault-Keys.KEY", false},
		{"VaultKeys.KEY.MORE", false},
		{".KEY", false},
	} {
		outputs := map[string]any{"sql": map[string]any{"encryption": map[string]any{"key": tc.key}}}
		parsed, err := registry.ParseOutputs(outputs, reg)
		if (err == nil) != tc.ok {
			t.Errorf("key %q: %v", tc.key, err)
		}
		if tc.ok && parsed.EncryptionKey() != tc.key {
			t.Errorf("key %q reads %q", tc.key, parsed.EncryptionKey())
		}
	}
	if _, err := registry.ParseOutputs(map[string]any{"sql": map[string]any{"encryption": map[string]any{}}}, reg); err == nil {
		t.Error("an encryption with no key parses")
	}
}
