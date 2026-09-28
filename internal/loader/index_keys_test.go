package loader

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifyIndexKeysTS: an @index type argument is checked by the compiler
// against its own keys, not the decorated class, and keyof admits a list
// relation, so both reach the verify pass, which refuses them.
func TestVerifyIndexKeysTS(t *testing.T) {
	_, err := LoadService(filepath.Join("tsreader", "testdata", "services", "broken-index-key"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	for _, want := range []string{
		`Author: @index(["name", "posts"]) key "posts" is a list relation, which has no column in table author`,
		`Post: @index(["name"]) key "name" names no field of Post`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err.Error(), want)
		}
	}
}

// TestVerifyIndexKeysJSON: the data forms have no compiler, so any string
// can be a key; the verify pass refuses one that names no field.
func TestVerifyIndexKeysJSON(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-db", "kind": "DB", "outputs": {}}`,
		"src/tenant.schema.json": `{"types": {"Tenant": {"name": "Tenant", "role": "DBTable", "fields": [
			{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
			{"name": "slug", "typeRef": {"name": "string"}, "required": true}
		], "indexes": [{"keys": ["slug"]}, {"keys": ["slugg"], "unique": true}]}}}`,
	})
	_, err := LoadService(dir)
	if err == nil || !strings.Contains(err.Error(), `Tenant: @index(["slugg"]) key "slugg" names no field of Tenant`) {
		t.Fatalf("error = %v", err)
	}
}
