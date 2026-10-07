package schemafile

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite runtime/schema/testdata/schema_file_parity*.json")

// The corpus the TypeScript strict loader asserts
// (runtime/schema/typescript/test/schema-file-parity.cjs), and the JSON
// Schema of the extended registry (parityRegistry), which the vectors with
// registry "extended" load against. The core vectors load against the core
// registry's, which @superschematic/schema-ir ships as schema-file.json.
var (
	parityCorpusPath     = filepath.Join("..", "..", "..", "runtime", "schema", "testdata", "schema_file_parity.json")
	parityMetaSchemaPath = filepath.Join("..", "..", "..", "runtime", "schema", "testdata", "schema_file_parity.meta-schema.json")
)

// parityCorpus is the file TestSchemaFileParityCorpus writes.
type parityCorpus struct {
	Comment string `json:"comment"`
	// Canonical holds ir.CanonicalJSON's output for arbitrary JSON text.
	Canonical []canonicalVector `json:"canonical"`
	// Vectors holds DecodeWith's verdict for schema-file payloads and, for
	// an accepted one, the decoded document as ir.CanonicalJSON writes it.
	Vectors []parityVector `json:"vectors"`
}

type canonicalVector struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type parityVector struct {
	Name      string `json:"name"`
	Registry  string `json:"registry"`
	Input     string `json:"input"`
	Accept    bool   `json:"accept"`
	Canonical string `json:"canonical,omitempty"`
	Error     string `json:"error,omitempty"`
}

// parityRegistry is the core registry plus what a deployment's registry
// may add and the loader must read from its JSON Schema: a kind, extension
// decorators on every target that has a slot, documents with and without
// a schema, an invocation policy under another key, and behaviors
// (testdata/behaviors): acme.Stock takes a required config, acme.Audited
// none.
func parityRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg := fixtureRegistry(t)
	if err := reg.RegisterKind(registry.KindSpec{Name: "Catalog", Extension: "acme", StructRole: ir.RoleEmbeddedStruct}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterToolInvocationPolicy(registry.ToolInvocationPolicy{
		Extension: "acme", Key: "review", Values: []string{"never", "always"}, Default: "never",
	}); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"stock.behavior.json", "audited.behavior.json"} {
		declaration, err := os.ReadFile(filepath.Join("testdata", "behaviors", file))
		if err != nil {
			t.Fatal(err)
		}
		if err := reg.RegisterBehavior(registry.BehaviorSpec{Extension: "acme", Declaration: declaration}); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

// canonicalInputs are JSON texts for ir.CanonicalJSON alone: number
// literals it keeps as written, the strings it escapes, the key order it
// sorts by and the inputs it refuses.
var canonicalInputs = []struct{ name, input string }{
	{"numbers as written", `[1.0, -0, 1E2, 1e+2, 0.10, 12345678901234567890123, 1e400, -1.5e-7]`},
	{"key order by UTF-8 bytes", `{"b": 1, "a": 2, "B": 3, "` + "\u00e9" + `": 4, "z": 5, "` + "\U0001F600" + `": 6, "": 7, "\uffff": 8, "aa": 9}`},
	{"nested and whitespace", " {\n\t\"list\": [ {\"y\": null, \"x\": true}, [], {} ],\r\n \"s\": \"\" } "},
	{"escapes", `["<a&b>", "\u2028\u2029", "\"\\\/", "\b\f\n\r\t", "\u0001\u001f\u007f", "\u00e9\u00E9", "\ud83d\ude00", "\ud800", "\udc00x\ud800", "` + "\u00e9\u2028\u2029\U0001F600\x7f" + `"]`},
	{"top-level scalars", `"text"`},
	{"top-level number", `-0.0`},
	{"top-level null", `null`},
	{"duplicate keys keep the last", `{"a": 1, "a": {"b": 2}}`},
	{"invalid JSON", `{"a": }`},
	{"trailing data", `{} {}`},
	{"a closing bracket after the value", `{}]`},
	{"a closing brace after the value", `[1]}`},
	{"empty input", ``},
}

// parityInputs are schema-file payloads. Every one runs through DecodeWith
// with the core registry or with parityRegistry ("extended").
var parityInputs = []struct{ name, registry, input string }{
	// Every file shape.
	{"document with every collection", "core", `{
		"name": "catalog", "kind": "General", "description": "Catalog.", "comment": "top",
		"imports": [{"from": "../shared", "package": "shared", "types": ["Money"]}],
		"scalars": {"Local.Code": {"name": "Local.Code", "languagePrimitive": "string", "pattern": "^[A-Z]+$", "minLength": 1, "maxLength": 8}},
		"types": {"Item": {"name": "Item", "role": "EmbeddedStruct", "fields": [
			{"name": "code", "typeRef": {"name": "Local.Code"}, "required": true},
			{"name": "tags", "typeRef": {"name": "String", "isArray": true}},
			{"name": "grid", "typeRef": {"name": "Integer", "isArray": true, "isArrayOfArrays": true}}
		]}},
		"enums": {"Colour": {"name": "Colour", "values": [{"name": "RED", "serializedAs": "red"}, {"name": "BLUE"}]}},
		"unions": {"Shape": {"name": "Shape", "types": ["Item"]}},
		"operationSets": [{"name": "ItemOps", "operations": [{"name": "getItem", "typeRef": {"name": "Item"}, "httpMethod": "GET"}]}]
	}`},
	{"document by kind alone", "core", `{"kind": "API", "name": "orders"}`},
	{"document by a collection key alone", "core", `{"types": {"Item": {"name": "Item", "role": "DBTable"}}}`},
	{"documents-only payload", "core", `{"documents": {}}`},
	{"extensions-only payload", "core", `{"extensions": {}}`},
	{"imports-only payload", "core", `{"imports": [{"package": "shared", "types": []}]}`},
	{"type file", "core", `{"name": "Item", "role": "DBTable", "fields": [{"name": "id", "typeRef": {"name": "Identity.UUID"}, "key": true}]}`},
	{"enum file", "core", `{"kind": "Enum", "name": "Colour", "values": [{"name": "RED"}], "description": "Colours."}`},
	{"union file", "core", `{"kind": "Union", "name": "Shape", "types": ["Circle", "Square"]}`},
	{"scalar file", "core", `{"kind": "Scalar", "name": "Local.Code", "languagePrimitive": "string", "minimum": -9223372036854775808, "maximum": 9223372036854775807}`},
	{"operation set file", "core", `{"kind": "OperationSet", "name": "Ops", "operations": [], "encrypted": true}`},
	{"an encrypted operation argument", "core", `{"kind": "OperationSet", "name": "CardOps", "operations": [
		{"name": "storeCard", "typeRef": {"name": "Card"}, "httpMethod": "POST", "arguments": [
			{"name": "number", "typeRef": {"name": "String"}, "required": true, "encrypted": true}]}]}`},
	{"a non-boolean encrypted argument flag", "core", `{"kind": "OperationSet", "name": "CardOps", "operations": [
		{"name": "storeCard", "typeRef": {"name": "Card"}, "arguments": [{"name": "number", "typeRef": {"name": "String"}, "encrypted": "yes"}]}]}`},
	{"a service clause on a set and an operation", "core", `{"kind": "OperationSet", "name": "StockOps", "serviceCallers": {"mode": "require", "from": ["orders-api"]}, "operations": [
		{"name": "release", "typeRef": {"name": "Order"}, "auth": true, "serviceCallers": {"mode": "allow", "from": []}}]}`},
	{"a service clause mode outside the enum", "core", `{"kind": "OperationSet", "name": "StockOps", "serviceCallers": {"mode": "sometimes"}, "operations": []}`},
	// The user model's traits (D50): User's config and UserRole's empty
	// object, which a pointer keeps.
	{"the user model's traits", "core", `{"kind": "DB", "name": "accounts", "types": {
		"Account": {"name": "Account", "role": "DBTable", "user": {"login": "email", "name": "displayName"}},
		"Admin": {"name": "Admin", "role": "DBTable", "user": {"login": "handle", "name": ""}},
		"Role": {"name": "Role", "role": "DBTable", "userRole": {}}}}`},
	{"a user trait without its login", "core", `{"name": "Account", "role": "DBTable", "user": {"name": "displayName"}}`},
	{"an unknown key on a user trait", "core", `{"name": "Account", "role": "DBTable", "user": {"login": "email", "password": "secret"}}`},
	{"a key on a userRole trait", "core", `{"name": "Role", "role": "DBTable", "userRole": {"permissions": "permissions"}}`},
	{"null for a userRole trait", "core", `{"name": "Role", "role": "DBTable", "userRole": null}`},

	// The invocation policy.
	{"visible tool without a policy gets the default", "core", `{"kind": "OperationSet", "name": "Ops", "operations": [
		{"name": "deleteOrder", "typeRef": {"name": "Order"}, "httpMethod": "DELETE", "mcp": {"handle": "delete_order", "hidden": false}}]}`},
	{"visible tool keeps its policy", "core", `{"kind": "OperationSet", "name": "Ops", "operations": [
		{"name": "deleteOrder", "typeRef": {"name": "Order"}, "mcp": {"handle": "delete_order", "hidden": false, "invocationPolicy": "ask"}}]}`},
	{"visible tool keeps the default value written out", "core", `{"kind": "OperationSet", "name": "Ops", "operations": [
		{"name": "getOrder", "typeRef": {"name": "Order"}, "mcp": {"handle": "get_order", "hidden": false, "invocationPolicy": "auto"}}]}`},
	{"hidden tool gets no policy", "core", `{"operationSets": [{"name": "Ops", "operations": [
		{"name": "purge", "typeRef": {"name": "Order"}, "mcp": {"hidden": true, "hiddenReason": "Staff only."}}]}]}`},
	{"hidden tool keeps a policy it declares", "core", `{"operationSets": [{"name": "Ops", "operations": [
		{"name": "purge", "typeRef": {"name": "Order"}, "mcp": {"hidden": true, "hiddenReason": "Staff only.", "invocationPolicy": "auto"}}]}]}`},
	{"an mcp record on a type field gets no policy", "core", `{"name": "Order", "role": "APIView", "fields": [
		{"name": "id", "typeRef": {"name": "String"}, "mcp": {"handle": "order_id", "hidden": false}}]}`},
	{"mcp _meta holds any JSON", "core", `{"kind": "OperationSet", "name": "Ops", "operations": [
		{"name": "getOrder", "typeRef": {"name": "Order"}, "mcp": {"handle": "get_order", "hidden": false, "_meta": {"ui": {"order": 1.0, "weights": [-0, 2.50, 1e21]}, "flag": null}}}]}`},

	// Values the Go decoder cannot tell from absent keys are dropped; a
	// pointer keeps its zero value.
	{"zero values the decoder drops", "core", `{
		"name": "", "description": "", "comment": "", "imports": [], "scalars": {}, "enums": {}, "unions": {}, "operationSets": [], "documents": {}, "extensions": {},
		"types": {"Item": {"name": "Item", "role": "EmbeddedStruct", "description": "", "fields": [
			{"name": "a", "typeRef": {"name": "String", "isArray": false, "isArrayOfArrays": false, "isMap": false, "elemNonNull": false},
			 "required": false, "description": "", "permissions": [], "arguments": []}
		], "indexes": [], "implements": [{"name": "Named", "configArgs": {}}], "strictJSON": false}}
	}`},
	{"zero values a pointer keeps", "core", `{"name": "Item", "role": "EmbeddedStruct", "fields": [
		{"name": "a", "typeRef": {"name": "Integer"}, "default": "", "validateMin": 0, "validateMax": 0, "validateMinLength": 0, "validateMaxLength": 0, "validateListMin": 0}]}`},
	{"zero values of a scalar file", "core", `{"kind": "Scalar", "name": "Local.Code", "languagePrimitive": "string", "minLength": 0, "maxLength": 0, "minimum": 0, "reservedWords": [], "typeMappings": {}}`},
	{"zero values of an mcp record", "core", `{"kind": "OperationSet", "name": "Ops", "operations": [
		{"name": "getOrder", "typeRef": {"name": "Order"}, "mcp": {"handle": "", "hidden": false, "hiddenReason": "", "_meta": {}}}]}`},
	{"projection literals keep their zero values", "core", `{"name": "OrderView", "role": "Projection", "projection": {"pool": "main", "name": "order_view", "migration": "0001", "source": "Order",
		"predicates": [{"column": "archived", "equals": {"bool": false}}, {"column": "note", "equals": {"string": ""}}, {"column": "total", "equals": {"number": 0}}]}}`},

	// Number literals: a float field holds a Go float64, an integer field a
	// Go int or int64, and configArgs any JSON value.
	{"float fields", "core", `{"name": "Item", "role": "EmbeddedStruct", "fields": [
		{"name": "a", "typeRef": {"name": "Float"}, "validateMin": 1.0, "validateMax": 1E2},
		{"name": "b", "typeRef": {"name": "Float"}, "validateMin": -0, "validateMax": 1e21},
		{"name": "c", "typeRef": {"name": "Float"}, "validateMin": 1e-7, "validateMax": 0.000001},
		{"name": "d", "typeRef": {"name": "Float"}, "validateMin": 123456789012345678901234567890, "validateMax": 0.1},
		{"name": "e", "typeRef": {"name": "Float"}, "validateMin": 5e-324, "validateMax": 1.7976931348623157e308},
		{"name": "f", "typeRef": {"name": "Float"}, "validateMin": 1e-400, "validateMax": -1.5e-10},
		{"name": "g", "typeRef": {"name": "Float"}, "validateMin": 123456789.123456789, "validateMax": 2.50}]}`},
	{"integer fields", "core", `{"name": "Item", "role": "EmbeddedStruct", "fields": [
		{"name": "a", "typeRef": {"name": "String"}, "validateMinLength": -0, "validateMaxLength": 9007199254740993},
		{"name": "b", "typeRef": {"name": "File"}, "validateUploadMaxBytes": 9223372036854775807, "validateListMin": -9223372036854775808}]}`},
	{"any values in configArgs", "core", `{"name": "Item", "role": "EmbeddedStruct", "implements": [{"name": "Named", "configArgs": {
		"a": 1.0, "b": -0, "c": 12345678901234567890, "d": 1e21, "e": [0.10, {"f": 1E-7}], "g": null, "h": "x", "i": 1e-400}}]}`},
	{"an integer field refuses a fraction", "core", `{"name": "Item", "role": "EmbeddedStruct", "fields": [{"name": "a", "typeRef": {"name": "String"}, "validateMinLength": 1.0}]}`},
	{"an integer field refuses an exponent", "core", `{"name": "Item", "role": "EmbeddedStruct", "fields": [{"name": "a", "typeRef": {"name": "String"}, "validateMinLength": 1e2}]}`},
	{"an integer field refuses a value past int64", "core", `{"name": "Item", "role": "EmbeddedStruct", "fields": [{"name": "a", "typeRef": {"name": "File"}, "validateUploadMaxBytes": 9223372036854775808}]}`},
	{"an integer field refuses a value below int64", "core", `{"kind": "Scalar", "name": "Local.Code", "languagePrimitive": "number", "minimum": -9223372036854775809}`},
	{"an integer field refuses a fraction past the double precision", "core", `{"name": "Item", "role": "EmbeddedStruct", "fields": [{"name": "a", "typeRef": {"name": "String"}, "validateMinLength": 1.0000000000000001}]}`},
	{"a float past float64 fails anywhere", "core", `{"name": "Item", "role": "EmbeddedStruct", "implements": [{"name": "Named", "configArgs": {"a": 1e400}}]}`},
	{"a negative float past float64 fails", "core", `{"name": "Item", "role": "EmbeddedStruct", "fields": [{"name": "a", "typeRef": {"name": "Float"}, "validateMin": -1e309}]}`},

	// Strings and keys.
	{"strings", "core", `{"name": "Item", "role": "EmbeddedStruct", "description": "<a&b> \u2028\u2029 \"q\" \\ \/ \b\f\n\r\t \u0001 \u007f \u00e9 \ud83d\ude00 \ud800 \udc00", "fields": [
		{"name": "a", "typeRef": {"name": "String"}, "comment": "` + "\u00e9\U0001F600\u2028" + `"}]}`},
	{"map keys sort by UTF-8 bytes", "core", `{"types": {
		"` + "\U0001F600" + `": {"name": "` + "\U0001F600" + `", "role": "EmbeddedStruct"}, "\uffff": {"name": "\uffff", "role": "EmbeddedStruct"},
		"` + "\u00e9" + `": {"name": "` + "\u00e9" + `", "role": "EmbeddedStruct"}, "b": {"name": "b", "role": "EmbeddedStruct"}, "B": {"name": "B", "role": "EmbeddedStruct"}}}`},
	{"a key named __proto__", "core", `{"name": "Item", "role": "EmbeddedStruct", "implements": [{"name": "Named", "configArgs": {"__proto__": {"x": 1}, "constructor": 2}}]}`},

	// Unknown keys at several depths.
	{"unknown key on the document", "core", `{"kind": "General", "name": "x", "colour": "red"}`},
	{"unknown key on a type", "core", `{"name": "Item", "role": "EmbeddedStruct", "colour": "red"}`},
	{"unknown key on a field", "core", `{"types": {"Item": {"name": "Item", "role": "EmbeddedStruct", "fields": [{"name": "a", "typeRef": {"name": "String"}, "colour": "red"}]}}}`},
	{"unknown key on a type ref", "core", `{"name": "Item", "role": "EmbeddedStruct", "fields": [{"name": "a", "typeRef": {"name": "String", "isList": true}}]}`},
	{"unknown key on an enum value", "core", `{"kind": "Enum", "name": "Colour", "values": [{"name": "RED", "colour": "red"}]}`},
	{"unknown key on an mcp record", "core", `{"kind": "OperationSet", "name": "Ops", "operations": [{"name": "op", "typeRef": {"name": "Order"}, "mcp": {"handle": "op", "hidden": false, "review": "always"}}]}`},
	{"unknown key on a projection literal", "core", `{"name": "V", "role": "Projection", "projection": {"pool": "p", "name": "v", "migration": "m", "source": "s", "predicates": [{"column": "c", "equals": {"int": 1}}]}}`},
	{"a definition key on a single-definition file", "core", `{"kind": "Enum", "name": "Colour", "values": [], "types": {}}`},
	{"a type's own kind makes it no single-definition file", "core", `{"name": "Item", "role": "EmbeddedStruct", "kind": "object"}`},
	{"a type's own kind in a document", "core", `{"types": {"Item": {"name": "Item", "role": "EmbeddedStruct", "kind": "object"}}}`},
	{"an extension the core does not link", "core", `{"name": "Item", "role": "EmbeddedStruct", "extensions": {"acme": {"tagged": true}}}`},
	{"a document the core does not register", "core", `{"documents": {"catalogConfig": {}}}`},

	// Enum values and types.
	{"unknown role", "core", `{"name": "Item", "role": "Table"}`},
	{"unknown schema kind", "core", `{"kind": "Catalog", "name": "c"}`},
	{"empty kind", "core", `{"kind": "", "name": "c"}`},
	{"kind that is not a string", "core", `{"kind": 3, "types": {}}`},
	{"unknown language primitive", "core", `{"kind": "Scalar", "name": "S", "languagePrimitive": "Text"}`},
	{"unknown http method", "core", `{"kind": "OperationSet", "name": "Ops", "operations": [{"name": "op", "typeRef": {"name": "Order"}, "httpMethod": "FETCH"}]}`},
	{"unknown invocation policy value", "core", `{"kind": "OperationSet", "name": "Ops", "operations": [{"name": "op", "typeRef": {"name": "Order"}, "mcp": {"handle": "op", "hidden": false, "invocationPolicy": "always"}}]}`},
	{"unknown docs lifecycle", "core", `{"kind": "OperationSet", "name": "Ops", "operations": [{"name": "op", "typeRef": {"name": "Order"}, "docs": {"title": "T", "description": "D", "capability": "c", "lifecycle": "beta", "visibility": "public", "mappingStatus": "mapped"}}]}`},
	{"a string where a boolean belongs", "core", `{"name": "Item", "role": "EmbeddedStruct", "strictJSON": "yes"}`},
	{"null where a string belongs", "core", `{"name": "Item", "role": "EmbeddedStruct", "description": null}`},
	{"null for an mcp record", "core", `{"kind": "OperationSet", "name": "Ops", "operations": [{"name": "op", "typeRef": {"name": "Order"}, "mcp": null}]}`},
	{"a type without its name", "core", `{"role": "EmbeddedStruct"}`},
	{"an mcp record without hidden", "core", `{"kind": "OperationSet", "name": "Ops", "operations": [{"name": "op", "typeRef": {"name": "Order"}, "mcp": {"handle": "op"}}]}`},

	// Payloads with no schema-file shape.
	{"empty object", "core", `{}`},
	{"name alone", "core", `{"name": "x"}`},
	{"top-level array", "core", `[]`},
	{"top-level null", "core", `null`},
	{"top-level string", "core", `"x"`},
	{"invalid JSON", "core", `{"name": }`},
	{"trailing data", "core", `{"types": {}} x`},

	// Repeated keys: the validator reads the last copy, the decode every
	// copy, so the reader refuses them.
	{"a repeated collection key", "core", `{"types": {"Bad": {"name": "Bad", "role": "Table"}}, "types": {"Good": {"name": "Good", "role": "EmbeddedStruct"}}}`},
	{"a repeated field list", "core", `{"name": "Item", "role": "EmbeddedStruct",
		"fields": [{"name": "a", "typeRef": {"name": "String"}, "httpMethod": "FETCH"}], "fields": [{"name": "a", "typeRef": {"name": "String"}}]}`},
	{"a repeated extensions slot", "extended", `{"name": "Item", "role": "EmbeddedStruct", "fields": [
		{"name": "a", "typeRef": {"name": "String"}, "extensions": {"vendor": {}}, "extensions": {}}]}`},
	{"a repeated key in extension data", "extended", `{"extensions": {"acme": {"catalog": true, "catalog": true}}}`},
	{"a repeated key in configArgs", "core", `{"name": "Item", "role": "EmbeddedStruct", "implements": [{"name": "Named", "configArgs": {"a": 1, "a": 1}}]}`},
	{"a repeated scalar key with the same value", "core", `{"kind": "Enum", "name": "Colour", "values": [], "name": "Colour"}`},
	{"a repeated key written with an escape", "core", `{"name": "Item", "role": "EmbeddedStruct", "description": "x", "descr\u0069ption": "x"}`},
	{"the same key in sibling objects", "core", `{"types": {"A": {"name": "A", "role": "EmbeddedStruct"}, "B": {"name": "B", "role": "EmbeddedStruct"}}}`},

	// A registry's own kinds, extension data, documents and policy.
	{"a registry's kind", "extended", `{"kind": "Catalog", "name": "catalog"}`},
	{"extension data on every holder", "extended", `{
		"kind": "DB", "name": "shop",
		"extensions": {"acme": {"catalog": true, "n": 1.0, "big": 12345678901234567890123, "neg": -0, "e": 1E+2, "s": "<&>"}},
		"documents": {"catalogConfig": {"shelves": 3, "note": "x"}, "otherConfig": {"any": [1.0, -0]}},
		"types": {"Item": {"name": "Item", "role": "DBTable", "extensions": {"acme": {"tagged": true}},
			"fields": [{"name": "slug", "typeRef": {"name": "Identity.Slug"}, "extensions": {"acme": {"shelf": {"aisle": 3.0, "z": -0}}}}]}},
		"operationSets": [{"name": "ItemOps", "extensions": {"acme": {"audited": true}},
			"operations": [{"name": "getItem", "typeRef": {"name": "Item"}, "extensions": {"acme": {"audited": true}}}]}]
	}`},
	{"extension numbers stay as written", "extended", `{"name": "Item", "role": "EmbeddedStruct", "fields": [
		{"name": "a", "typeRef": {"name": "String"}, "extensions": {"acme": {"shelf": {"aisle": 12345678901234567890, "z": 1E2}}}},
		{"name": "b", "typeRef": {"name": "String"}, "extensions": {"acme": {"shelf": {"aisle": 0, "z": 1.50}}}}]}`},
	{"empty extension entries on the holders are dropped", "extended", `{
		"extensions": {"acme": {}, "other": {}},
		"types": {"Item": {"name": "Item", "role": "DBTable", "extensions": {"acme": {}},
			"fields": [{"name": "slug", "typeRef": {"name": "String"}, "extensions": {"acme": {}}}]}},
		"operationSets": [{"name": "Ops", "extensions": {"acme": {}}, "operations": [{"name": "op", "typeRef": {"name": "Item"}, "extensions": {"acme": {}}}]}]
	}`},
	{"an empty extension entry on a trait config field is kept", "extended", `{"name": "Named", "role": "Trait", "traitConfig": {"fields": [
		{"name": "label", "typeRef": {"name": "String"}, "extensions": {"acme": {}}}]}}`},
	{"documents keep an empty document", "extended", `{"documents": {"catalogConfig": {}, "cfg": {}}}`},
	{"a document against its schema", "extended", `{"documents": {"catalogConfig": {"shelves": "three"}}}`},
	{"a decorator argument against its schema", "extended", `{"name": "Item", "role": "EmbeddedStruct", "fields": [{"name": "a", "typeRef": {"name": "String"}, "extensions": {"acme": {"shelf": {"aisle": -1}}}}]}`},
	{"a decorator argument with a fraction", "extended", `{"name": "Item", "role": "EmbeddedStruct", "fields": [{"name": "a", "typeRef": {"name": "String"}, "extensions": {"acme": {"shelf": {"aisle": 1.5}}}}]}`},
	{"a decorator on the wrong target", "extended", `{"name": "Item", "role": "EmbeddedStruct", "extensions": {"acme": {"shelf": {"aisle": 1}}}}`},
	{"an extension the registry does not link", "extended", `{"name": "Item", "role": "EmbeddedStruct", "extensions": {"vendor": {}}}`},
	{"a float past float64 in extension data", "extended", `{"extensions": {"acme": {"n": 1e400}}}`},
	{"the registry's policy default", "extended", `{"kind": "OperationSet", "name": "Ops", "operations": [
		{"name": "deleteOrder", "typeRef": {"name": "Order"}, "mcp": {"handle": "delete_order", "hidden": false}}]}`},
	{"the registry's policy value", "extended", `{"kind": "OperationSet", "name": "Ops", "operations": [
		{"name": "deleteOrder", "typeRef": {"name": "Order"}, "mcp": {"handle": "delete_order", "hidden": false, "review": "always"}}]}`},
	{"the core policy key under a registry's policy", "extended", `{"kind": "OperationSet", "name": "Ops", "operations": [
		{"name": "deleteOrder", "typeRef": {"name": "Order"}, "mcp": {"handle": "delete_order", "hidden": false, "invocationPolicy": "ask"}}]}`},

	// A registry's behaviors: the names it registers, each config held to
	// its declaration's config schema and stored canonically, {} as none.
	// The Go reader checks names and configs before the JSON Schema runs;
	// the verdicts are the JSON Schema's.
	{"behaviors with and without a config", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [
		{"name": "acme.Stock", "config": {"unit": "box", "aisles": 2}}, {"name": "acme.Audited"}]}`},
	{"a behavior config of {} is stored as none", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [
		{"name": "acme.Stock", "config": {"aisles": 1}}, {"name": "acme.Audited", "config": { }}]}`},
	{"behavior config numbers and key order", "extended", `{"types": {"Item": {"name": "Item", "role": "DBTable", "behaviors": [
		{"config": {"unit": "<crate>", "aisles": 1E1}, "name": "acme.Stock"}]},
		"Bin": {"name": "Bin", "role": "DBTable", "behaviors": [{"name": "acme.Stock", "config": {"aisles": 12345678901234567890}}]},
		"Box": {"name": "Box", "role": "DBTable", "behaviors": [{"name": "acme.Stock", "config": {"aisles": 3.0, "unit": ""}}]}}}`},
	{"an empty behavior list is dropped", "extended", `{"name": "Item", "role": "DBTable", "behaviors": []}`},
	{"an empty behavior list under the core", "core", `{"name": "Item", "role": "DBTable", "behaviors": []}`},
	{"a behavior under the core", "core", `{"name": "Item", "role": "DBTable", "behaviors": [{"name": "acme.Audited"}]}`},
	// The core's own behaviors, which the core registry declares.
	{"the core's behaviors under the core", "core", `{"name": "Document", "role": "EmbeddedStruct", "behaviors": [
		{"name": "Workflow", "config": {"transitions": [{"to": "done", "from": "open", "permission": "documents.close"}], "states": ["open", "done"]}},
		{"name": "Comments", "config": {}}, {"name": "Revisions", "config": {"review": {"permission": "documents.review"}}}]}`},
	{"a core behavior config its schema rejects", "core", `{"name": "Document", "role": "EmbeddedStruct", "behaviors": [
		{"name": "Workflow", "config": {"states": [], "transitions": []}}]}`},
	{"a missing required core behavior config", "core", `{"name": "Document", "role": "EmbeddedStruct", "behaviors": [{"name": "Workflow"}]}`},
	{"a config for a core behavior that takes none", "core", `{"name": "Document", "role": "EmbeddedStruct", "behaviors": [{"name": "Comments", "config": {"threads": true}}]}`},
	{"an unregistered behavior", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [{"name": "acme.Ghost"}]}`},
	{"an unregistered behavior in a document", "extended", `{"types": {"Item": {"name": "Item", "role": "DBTable", "behaviors": [{"name": "acme.Ghost"}]}}}`},
	{"a behavior config its schema rejects", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [{"name": "acme.Stock", "config": {"aisles": 0}}]}`},
	{"a behavior config with a key its schema lacks", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [{"name": "acme.Stock", "config": {"aisles": 1, "colour": "red"}}]}`},
	{"a behavior config with a fraction for an integer", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [{"name": "acme.Stock", "config": {"aisles": 1.5}}]}`},
	{"a missing required behavior config", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [{"name": "acme.Stock"}]}`},
	{"a required behavior config of {}", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [{"name": "acme.Stock", "config": {}}]}`},
	{"a config for a behavior that takes none", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [{"name": "acme.Audited", "config": {"on": true}}]}`},
	{"a null behavior config", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [{"name": "acme.Audited", "config": null}]}`},
	{"an unknown key on a behavior entry", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [{"name": "acme.Audited", "extra": 1}]}`},
	{"a behavior entry without its name", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [{"config": {"aisles": 1}}]}`},
	{"a behavior name that is not a string", "extended", `{"name": "Item", "role": "DBTable", "behaviors": [{"name": 3}]}`},
}

// parityFiles are data-form files the JSON writer wrote for the loader's
// fixture services (testdata/parity): operation docs, MCP records, SQL
// projections, arrays of arrays and a single-definition enum file.
func parityFiles(t *testing.T) []struct{ name, registry, input string } {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "parity", "*.schema.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no parity files: %v", err)
	}
	var files []struct{ name, registry, input string }
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, struct{ name, registry, input string }{"file " + filepath.Base(path), "core", string(data)})
	}
	return files
}

// TestSchemaFileParityCorpus runs canonicalInputs through ir.CanonicalJSON,
// and parityInputs and parityFiles through DecodeWith, writing each accepted
// document as ir.CanonicalJSON of its JSON encoding, and compares the result
// with runtime/schema/testdata/schema_file_parity.json, which the TypeScript
// strict loader's suite asserts: the same verdict and, for an accepted
// payload, the same bytes. -update rewrites the file.
func TestSchemaFileParityCorpus(t *testing.T) {
	extended := parityRegistry(t)
	metaSchema, err := DefinitionFor(extended)
	if err != nil {
		t.Fatal(err)
	}
	corpus := parityCorpus{
		Comment: "Written by TestSchemaFileParityCorpus (internal/loader/schemafile); regenerate with go test ./internal/loader/schemafile -run TestSchemaFileParityCorpus -update. The extended vectors load against schema_file_parity.meta-schema.json.",
	}

	for _, in := range canonicalInputs {
		vector := canonicalVector{Name: in.name, Input: in.input}
		out, err := ir.CanonicalJSON(json.RawMessage(in.input))
		if err != nil {
			vector.Error = err.Error()
		} else {
			vector.Output = string(out)
		}
		corpus.Canonical = append(corpus.Canonical, vector)
	}

	for _, in := range append(parityInputs, parityFiles(t)...) {
		reg := (*registry.Registry)(nil)
		switch in.registry {
		case "core":
		case "extended":
			reg = extended
		default:
			t.Fatalf("%s: unknown registry %q", in.name, in.registry)
		}
		vector := parityVector{Name: in.name, Registry: in.registry, Input: in.input}
		doc, err := DecodeWith([]byte(in.input), in.name, reg)
		if err != nil {
			vector.Error = err.Error()
		} else {
			encoded, err := json.Marshal(doc)
			if err != nil {
				t.Fatalf("%s: encoding the decoded document: %v", in.name, err)
			}
			canonical, err := ir.CanonicalJSON(encoded)
			if err != nil {
				t.Fatalf("%s: %v", in.name, err)
			}
			vector.Accept = true
			vector.Canonical = string(canonical)
		}
		corpus.Vectors = append(corpus.Vectors, vector)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(corpus); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{parityCorpusPath: buf.Bytes(), parityMetaSchemaPath: metaSchema} {
		if *update {
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s (run with -update): %v", path, err)
		}
		if !bytes.Equal(data, want) {
			t.Errorf("%s differs from what the reader produces; run with -update and review the diff", path)
		}
	}
}
