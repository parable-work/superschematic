package schemafile

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDefinitionGenerates(t *testing.T) {
	data, err := Definition()
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}

	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("definition is not valid JSON: %v", err)
	}
	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("definition has no $defs; keys: %v", mapKeys(root))
	}
	for _, want := range []string{
		"Document", "TypeDef", "FieldDef", "EnumDef", "UnionDef", "ScalarDef",
		"OperationSet", "EnumFile", "UnionFile", "ScalarFile", "OperationSetFile",
	} {
		if _, ok := defs[want]; !ok {
			t.Errorf("definition is missing $defs/%s; have: %v", want, mapKeys(defs))
		}
	}
	if _, ok := root["oneOf"]; !ok {
		t.Error("definition root has no oneOf")
	}
}

func TestDefinitionRejectsUnknownKeys(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr string
	}{
		{
			name:    "unknown top-level key",
			payload: `{"types": {}, "bogus": true}`,
			wantErr: "bogus",
		},
		{
			name: "unknown decorator name on a field",
			payload: `{
				"name": "Tenant", "role": "DBTable",
				"fields": [{"name": "id", "typeRef": {"name": "string"}, "sparkles": true}]
			}`,
			wantErr: "sparkles",
		},
		{
			// Origin marks what the loader expanded; no schema file writes it.
			name: "origin on a field",
			payload: `{
				"name": "Step", "role": "DBTable",
				"fields": [{"name": "ref", "typeRef": {"name": "string"}, "origin": "versionGraph"}]
			}`,
			wantErr: "origin",
		},
		{
			name:    "origin on a type",
			payload: `{"name": "RecipeRef", "role": "DBTable", "origin": "versionGraph"}`,
			wantErr: "origin",
		},
		{
			name:    "unknown role",
			payload: `{"name": "Tenant", "role": "Tablecloth"}`,
			wantErr: "'/role': value must be one of",
		},
		{
			name:    "unknown single-definition kind",
			payload: `{"name": "Color", "kind": "Enumeration", "values": []}`,
			wantErr: "Enumeration",
		},
		{
			name:    "unknown schema kind text is pinned",
			payload: `{"kind": "Bogus", "types": {}}`,
			wantErr: `test.schema.json: unknown kind "Bogus": expected a definition kind (Enum, Union, Scalar, OperationSet) or a schema kind`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode([]byte(tc.payload), "test.schema.json")
			if err == nil {
				t.Fatalf("Decode accepted invalid payload")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestDecodeSingleDefinitionForms(t *testing.T) {
	enumJSON := `{
		"name": "TenantUserStatus",
		"kind": "Enum",
		"comment": "Lifecycle of a tenant user.",
		"values": [
			{"name": "Active", "serializedAs": "active"},
			{"name": "Suspended", "serializedAs": "suspended"}
		]
	}`
	doc, err := Decode([]byte(enumJSON), "tenant-user-status.schema.json")
	if err != nil {
		t.Fatalf("Decode enum: %v", err)
	}
	enum, ok := doc.Enums["TenantUserStatus"]
	if !ok {
		t.Fatalf("decoded document has no TenantUserStatus enum")
	}
	if enum.Comment != "Lifecycle of a tenant user." {
		t.Errorf("enum comment = %q", enum.Comment)
	}
	if len(enum.Values) != 2 || enum.Values[1].SerializedAs != "suspended" {
		t.Errorf("enum values = %+v", enum.Values)
	}

	typeJSON := `{
		"name": "Tenant",
		"role": "DBTable",
		"versioned": true,
		"fields": [
			{"name": "id", "typeRef": {"name": "Identity.UUID"}, "required": true, "key": true}
		]
	}`
	doc, err = Decode([]byte(typeJSON), "tenant.schema.json")
	if err != nil {
		t.Fatalf("Decode type: %v", err)
	}
	td, ok := doc.Types["Tenant"]
	if !ok {
		t.Fatalf("decoded document has no Tenant type")
	}
	if !td.Fields[0].Key {
		t.Errorf("field decorators lost: %+v", td.Fields[0])
	}
	if !td.Versioned {
		t.Errorf("versioned flag lost: %+v", td)
	}

	scalarJSON := `{"name": "Network.Url", "kind": "Scalar", "languagePrimitive": "string"}`
	doc, err = Decode([]byte(scalarJSON), "url.schema.json")
	if err != nil {
		t.Fatalf("Decode scalar: %v", err)
	}
	if _, ok := doc.Scalars["Network.Url"]; !ok {
		t.Fatalf("decoded document has no Network.Url scalar")
	}

	unionJSON := `{"name": "Payload", "kind": "Union", "types": ["A", "B"]}`
	doc, err = Decode([]byte(unionJSON), "payload.schema.json")
	if err != nil {
		t.Fatalf("Decode union: %v", err)
	}
	if u := doc.Unions["Payload"]; u == nil || len(u.Types) != 2 {
		t.Fatalf("decoded union = %+v", doc.Unions)
	}

	opSetJSON := `{
		"name": "TenantOperations",
		"kind": "OperationSet",
		"operations": [
			{"name": "getTenant", "typeRef": {"name": "Tenant"}, "httpMethod": "GET", "auth": true}
		]
	}`
	doc, err = Decode([]byte(opSetJSON), "tenant-ops.schema.json")
	if err != nil {
		t.Fatalf("Decode operation set: %v", err)
	}
	if len(doc.OperationSets) != 1 || doc.OperationSets[0].Operations[0].HTTPMethod != "GET" {
		t.Fatalf("decoded operation sets = %+v", doc.OperationSets)
	}
}

func TestDecodeMultiDefinitionDocument(t *testing.T) {
	docJSON := `{
		"name": "fixture",
		"kind": "DB",
		"imports": [{"package": "@schemas/web-db", "types": ["Tenant"]}],
		"scalars": {
			"Identity.UUID": {"name": "Identity.UUID", "languagePrimitive": "string"}
		},
		"types": {
			"TenantUser": {
				"name": "TenantUser",
				"role": "DBTable",
				"fields": [{"name": "id", "typeRef": {"name": "Identity.UUID"}, "required": true}]
			}
		},
		"enums": {
			"Status": {"name": "Status", "values": [{"name": "Active"}]}
		}
	}`
	doc, err := Decode([]byte(docJSON), "fixture.schema.json")
	if err != nil {
		t.Fatalf("Decode document: %v", err)
	}
	if doc.Name != "fixture" || doc.Kind != "DB" {
		t.Errorf("doc identity = %q/%q", doc.Name, doc.Kind)
	}
	if len(doc.Imports) != 1 || doc.Imports[0].Types[0] != "Tenant" {
		t.Errorf("imports = %+v", doc.Imports)
	}
	if _, ok := doc.Types["TenantUser"]; !ok {
		t.Errorf("types = %v", mapKeys2(doc.Types))
	}
}

func TestDispatchAmbiguityErrors(t *testing.T) {
	if _, err := Decode([]byte(`{"name": "x"}`), "x.schema.json"); err == nil {
		t.Error("Decode accepted a payload with no discriminator and no collections")
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func mapKeys2[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestDefaultsAreWhatTheEncoderOmits: a property of the JSON Schema has a
// default exactly when the Go encoder leaves it out at one value, and the
// default is that value. For each field of each IR struct a document
// reaches, the test encodes the struct with that field at its empty value
// (a pointer to a zero value, an empty slice or map, a zero scalar) and
// looks for the key in the output, through the struct's own encoder. A raw
// JSON value's default is {}, which the readers store as the empty value.
func TestDefaultsAreWhatTheEncoderOmits(t *testing.T) {
	data, err := Definition()
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Defs map[string]struct {
			Properties map[string]map[string]any `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}

	seen := map[reflect.Type]bool{}
	sawDefault := false
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		props := root.Defs[typ.Name()].Properties
		for i := range typ.NumField() {
			field := typ.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			schemaTag, _, _ := strings.Cut(field.Tag.Get("jsonschema"), ",")
			if !field.IsExported() || name == "-" {
				continue
			}
			if schemaTag == "-" {
				// Loader output the data forms have no key for.
				if _, ok := props[name]; ok {
					t.Errorf("$defs/%s/properties/%s: a jsonschema:\"-\" field must have no property", typ.Name(), name)
				}
				continue
			}
			visit(field.Type)

			value := reflect.New(typ).Elem()
			empty := value.Field(i)
			switch empty.Kind() {
			case reflect.Pointer:
				empty.Set(reflect.New(empty.Type().Elem()))
			case reflect.Slice:
				empty.Set(reflect.MakeSlice(empty.Type(), 0, 0))
			case reflect.Map:
				empty.Set(reflect.MakeMap(empty.Type()))
			}
			encoded, err := json.Marshal(value.Interface())
			if err != nil {
				t.Fatalf("%s.%s: %v", typ.Name(), field.Name, err)
			}
			var keys map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &keys); err != nil {
				t.Fatal(err)
			}
			_, written := keys[name]
			fallback, hasDefault := props[name]["default"]
			switch {
			case written && hasDefault:
				t.Errorf("$defs/%s/properties/%s has default %v, but the encoder writes the key at its empty value", typ.Name(), name, fallback)
			case !written && !hasDefault:
				t.Errorf("$defs/%s/properties/%s has no default, but the encoder omits the key at its empty value", typ.Name(), name)
			case hasDefault:
				sawDefault = true
				want, _ := json.Marshal(empty.Interface())
				if field.Type == reflect.TypeFor[json.RawMessage]() {
					// Empty raw JSON is no JSON text; the readers store a
					// value of {} as it (TestDecodeBehaviors).
					want = []byte("{}")
				}
				if got, _ := json.Marshal(fallback); string(got) != string(want) {
					t.Errorf("$defs/%s/properties/%s default = %s, want %s", typ.Name(), name, got, want)
				}
			}
		}
	}
	visit(reflect.TypeFor[Document]())
	if !sawDefault {
		t.Fatal("no property of the JSON Schema has a default")
	}
}
