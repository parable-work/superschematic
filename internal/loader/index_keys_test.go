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

// TestVerifyIndexTablesTS: a base class, a @jsonField class and a @trait get
// no table, and the walker copies their fields, not their indexes, onto the
// tables that use them, so the verify pass refuses an @index on each.
func TestVerifyIndexTablesTS(t *testing.T) {
	_, err := LoadService(filepath.Join("tsreader", "testdata", "services", "broken-index-table"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	for _, want := range []string{
		`Auditable: @index(["createdAt"]) is on a base class, which gets no table; declare it on each table that extends Auditable (Member, Tenant)`,
		`Address: @index(["city"]) is on a @jsonField type, which is stored as JSON and gets no table`,
		`SoftDeletable: @index(["deletedAt"]) is on a @trait, which gets no table; declare it on each table that implements SoftDeletable`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err.Error(), want)
		}
	}
}

// TestVerifyIndexTablesJSON: the data forms carry a base class's and a
// @jsonField type's indexes as written; the verify pass refuses both.
func TestVerifyIndexTablesJSON(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-db", "kind": "DB", "outputs": {}}`,
		"src/tenant.schema.json": `{"types": {
			"Auditable": {"name": "Auditable", "role": "DBTable", "fields": [
				{"name": "createdAt", "typeRef": {"name": "string"}, "required": true}
			], "indexes": [{"keys": ["createdAt"]}]},
			"Tenant": {"name": "Tenant", "role": "DBTable", "extends": "Auditable", "fields": [
				{"name": "createdAt", "typeRef": {"name": "string"}, "required": true, "inheritedFrom": "Auditable"},
				{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
				{"name": "address", "typeRef": {"name": "Address"}, "required": true}
			], "indexes": [{"keys": ["createdAt"]}]},
			"Address": {"name": "Address", "role": "DBTable", "jsonField": true, "fields": [
				{"name": "city", "typeRef": {"name": "string"}, "required": true}
			], "indexes": [{"keys": ["city"], "unique": true}]}}}`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("expected schema errors")
	}
	for _, want := range []string{
		`Auditable: @index(["createdAt"]) is on a base class, which gets no table; declare it on each table that extends Auditable (Tenant)`,
		`Address: @index(["city"]) is on a @jsonField type, which is stored as JSON and gets no table`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "Tenant:") {
		t.Errorf("error %q refuses Tenant's own index", err.Error())
	}
}
