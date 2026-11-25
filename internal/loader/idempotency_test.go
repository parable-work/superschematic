package loader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// idempotencyServices writes a DB service and an API service over it,
// side by side, in the data form, and returns the API's directory. keys
// is the DB schema's types, and operation the API's one operation.
func idempotencyServices(t *testing.T, keys, operation string) (db, api string) {
	t.Helper()
	root := t.TempDir()
	for rel, contents := range map[string]string{
		"keys-db/schema.config.json":      `{"name": "keys-db", "kind": "DB", "outputs": {}}`,
		"keys-db/src/keys.schema.json":    `{"name": "keys-db", "kind": "DB", "types": {` + keys + `}}`,
		"keys-api/schema.config.json":     `{"name": "keys-api", "kind": "API", "dependencies": [{"name": "keys-db", "kind": "DB"}], "outputs": {}}`,
		"keys-api/src/orders.schema.json": `{"name": "keys-api", "kind": "API", "types": {"Order": {"name": "Order", "role": "EmbeddedStruct", "fields": [{"name": "sku", "typeRef": {"name": "string"}, "required": true}]}}, "operationSets": [{"name": "Orders", "operations": [` + operation + `]}]}`,
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "keys-db"), filepath.Join(root, "keys-api")
}

const (
	reservedKeys = `"IdempotencyKey": {"name": "IdempotencyKey", "role": "DBTable", "idempotencyKeys": {}}`
	plainTable   = `"Note": {"name": "Note", "role": "DBTable", "fields": [{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true}]}`
)

func idempotentOperation(method, more string) string {
	return `{"name": "createOrder", "typeRef": {"name": "Order"}, "required": true, "httpMethod": "` + method + `", "restPath": "orders", "idempotent": {"ttlSeconds": 3600}` + more + `}`
}

// TestIdempotencyKeysTableIsExpanded: the class that implements
// IdempotencyKeys gets its key, its columns and its index from the loader
// (D140), each marked as added, and an API over the database with an
// @idempotent operation loads.
func TestIdempotencyKeysTableIsExpanded(t *testing.T) {
	db, api := idempotencyServices(t, reservedKeys, idempotentOperation("POST", ""))
	schema, err := LoadService(db)
	if err != nil {
		t.Fatalf("load the database: %v", err)
	}
	table := schema.IdempotencyKeysTable()
	if table == nil || table.Name != "IdempotencyKey" {
		t.Fatalf("IdempotencyKeysTable = %+v", table)
	}
	var names []string
	for _, field := range table.Fields {
		names = append(names, field.Name)
		if field.Origin != ir.OriginIdempotency {
			t.Errorf("field %s has origin %q", field.Name, field.Origin)
		}
	}
	if got := strings.Join(names, ","); got != "id,lookup,operation,principal,key,requestHash,state,status,body,startedAt,expiresAt" {
		t.Errorf("fields = %s", got)
	}
	byName := map[string]*ir.FieldDef{}
	for _, field := range table.Fields {
		byName[field.Name] = field
	}
	if !byName["id"].Key || !byName["lookup"].Unique || !byName["lookup"].Required || byName["status"].Required || byName["body"].Required || !byName["expiresAt"].Required {
		t.Errorf("the key, the unique lookup or the optional answer are not as declared: %+v", byName)
	}
	if len(table.Indexes) != 1 || strings.Join(table.Indexes[0].Keys, ",") != "expiresAt" {
		t.Errorf("indexes = %+v", table.Indexes)
	}

	loaded, err := LoadService(api)
	if err != nil {
		t.Fatalf("load the API: %v", err)
	}
	if got := loaded.IdempotentOperations(); len(got) != 1 || got[0] != "Orders.createOrder" {
		t.Errorf("IdempotentOperations = %v", got)
	}
	if op := loaded.OperationSets[0].Operations[0]; op.Idempotent == nil || op.Idempotent.TTL() != 3600 || op.Idempotent.Required {
		t.Errorf("createOrder's @idempotent = %+v", op.Idempotent)
	}
}

// TestIdempotentIsRefused: an @idempotent operation over a database that
// declares no table for its keys is refused with the line to add, and so
// is one on an operation a repeat cannot be answered for (D140).
func TestIdempotentIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, keys, operation, want string
	}{
		{"a database without the table", plainTable, idempotentOperation("POST", ""),
			"keys-api: its @idempotent operations (Orders.createOrder) keep their keys in keys-db, which declares no table for them; add to a schema file of keys-db, with IdempotencyKeys imported from @superschematic/db: export abstract class IdempotencyKey implements IdempotencyKeys {}"},
		{"a GET", reservedKeys, idempotentOperation("GET", ""), "Orders.createOrder: @idempotent on a GET, which a caller repeats freely; it marks a POST, PUT, PATCH or DELETE"},
		{"a file", reservedKeys, strings.Replace(idempotentOperation("POST", `, "fileDownload": {"contentType": "text/csv"}`), `"typeRef": {"name": "Order"}`, `"typeRef": {"name": ""}`, 1),
			"Orders.createOrder: @idempotent on a FileDownload result"},
		{"a manual route", reservedKeys, idempotentOperation("POST", `, "manualRouteRegistration": true`), "Orders.createOrder: @idempotent on a @manualRouteRegistration operation"},
		{"a ttl past the longest", reservedKeys, strings.Replace(idempotentOperation("POST", ""), `"ttlSeconds": 3600`, `"ttlSeconds": 7776001`, 1), "Orders.createOrder: @idempotent ttl is 7776001 seconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, api := idempotencyServices(t, tc.keys, tc.operation)
			if _, err := LoadService(api); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want an error containing %q", err, tc.want)
			}
		})
	}

	// The reserved class on a schema that is no DB, and twice in one.
	db, _ := idempotencyServices(t, reservedKeys+`, "OtherKeys": {"name": "OtherKeys", "role": "DBTable", "idempotencyKeys": {}}`, idempotentOperation("POST", ""))
	if _, err := LoadService(db); err == nil || !strings.Contains(err.Error(), "IdempotencyKey, OtherKeys all implement IdempotencyKeys; a schema keeps its idempotency keys in one table") {
		t.Errorf("two tables: %v", err)
	}
	db, _ = idempotencyServices(t, `"IdempotencyKey": {"name": "IdempotencyKey", "role": "DBTable", "idempotencyKeys": {}, "fields": [{"name": "lookup", "typeRef": {"name": "string"}}]}`, idempotentOperation("POST", ""))
	if _, err := LoadService(db); err == nil || !strings.Contains(err.Error(), "adds the field lookup, and declares one of that name") {
		t.Errorf("a class that declares a field the loader adds: %v", err)
	}
}
