package ir

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestToolSchemaTypeAcceptsBothJSONSchemaSpellings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		encoded string
		want    ToolSchemaType
	}{
		{name: "scalar", encoded: `"string"`, want: ToolSchemaType{"string"}},
		{name: "union", encoded: `["string","null"]`, want: ToolSchemaType{"string", "null"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got ToolSchemaType
			if err := json.Unmarshal([]byte(tc.encoded), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decoded %#v, want %#v", got, tc.want)
			}
			reencoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(reencoded) != tc.encoded {
				t.Fatalf("re-encoded %s, want %s", reencoded, tc.encoded)
			}
		})
	}

	var rejected ToolSchemaType
	if err := json.Unmarshal([]byte(`[]`), &rejected); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty type array was accepted: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"type":"string"}`), &rejected); err == nil {
		t.Fatal("object-valued type was accepted")
	}
}

func TestToolSchemaTypeIsAndMissing(t *testing.T) {
	if !(ToolSchemaType{"object"}).Is("object") {
		t.Fatal("scalar type does not report itself")
	}
	if (ToolSchemaType{"object", "null"}).Is("object") {
		t.Fatal("union reports as one scalar type")
	}
	for _, missing := range []ToolSchemaType{nil, {}, {""}, {"string", " "}} {
		if !missing.Missing() {
			t.Fatalf("%#v should be missing", missing)
		}
	}
	for _, present := range []ToolSchemaType{{"string"}, {"object", "null"}} {
		if present.Missing() {
			t.Fatalf("%#v should not be missing", present)
		}
	}
}

func TestToolSchemaAdditionalPropertiesForms(t *testing.T) {
	var closed, typed ToolSchemaAdditionalProperties
	if err := json.Unmarshal([]byte(`false`), &closed); err != nil || !closed.IsFalse() || closed.IsTrue() {
		t.Fatalf("false decoded as %+v (%v)", closed, err)
	}
	if err := json.Unmarshal([]byte(`{"type":"string"}`), &typed); err != nil || typed.Schema == nil || !typed.Schema.Type.Is("string") {
		t.Fatalf("typed map decoded as %+v (%v)", typed, err)
	}
	for value, want := range map[*ToolSchemaAdditionalProperties]string{&closed: `false`, &typed: `{"type":"string"}`, {}: `true`} {
		if got, err := json.Marshal(value); err != nil || string(got) != want {
			t.Errorf("re-encoded %s (%v), want %s", got, err, want)
		}
	}
	if err := json.Unmarshal([]byte(`"yes"`), &closed); err == nil {
		t.Fatal("a string additionalProperties was accepted")
	}
}

func TestToolManifestTypesDecodeGeneratedShapes(t *testing.T) {
	const manifest = `{
	  "$schema": "https://json-schema.org/draft/2020-12/schema",
	  "title": "FixtureMcpSDK Tool Definitions",
	  "description": "LLM tool calling bindings for fixture-mcp API",
	  "tools": [{
	    "name": "order.getOrder", "operationId": "OrderGetOrderHandler", "title": "Get an order",
	    "mcp": {"hidden": false, "name": "Get an order", "handle": "get_order", "description": "Returns one order.",
	            "_meta": {"ui": {"resourceUri": "ui://orders/detail"}},
	            "icon": {"name": "receipt"}},
	    "capability": "orders.get", "lifecycle": "active", "visibility": "public", "audience": "shoppers",
	    "guidance": {"useWhen": "Use it.", "doNotUseWhen": "Do not.", "success": "Returns it.",
	                 "errors": [{"code": "order_not_found", "description": "No order.", "commonCorrection": "List first."}]},
	    "replay": {"mode": "read_only", "idempotencyKeyPointers": [], "expectedRevisionPointers": []},
	    "description": "Returns one order.", "namespace": "order", "methodName": "getOrder",
	    "httpMethod": "GET", "httpPath": "/api/orders/{id}", "requiresAuth": false, "requiredPermissions": [],
	    "isScoped": false, "bindingStatus": "ready",
	    "inputSchemaDigest": "sha256:00",
	    "parameters": {"type": "object", "additionalProperties": false,
	      "properties": {"id": {"type": "string", "x-superschematic-scalar": "Identity.UUID", "description": "id parameter"},
	                     "labels": {"type": "object", "additionalProperties": {"type": "string"}},
	                     "payload": {"type": ["object", "array", "string", "number", "boolean", "null"], "x-superschematic-scalar": "Generic.JSON"}},
	      "required": ["id"]},
	    "returns": {"type": "array", "description": "Array of Order", "items": {"type": "object", "description": "Order object"}}
	  }]
	}`
	var decoded ToolManifest
	decoder := json.NewDecoder(strings.NewReader(manifest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	tool := decoded.Tools[0]
	if tool.Parameters.Type != "object" || !tool.Parameters.AdditionalProperties.IsFalse() || !reflect.DeepEqual(tool.Parameters.Required, []string{"id"}) {
		t.Fatalf("parameters decoded as %+v", tool.Parameters)
	}
	if tool.Parameters.Properties["id"].CanonicalScalar != "Identity.UUID" || !tool.Parameters.Properties["id"].Type.Is("string") {
		t.Fatalf("scalar property decoded as %+v", tool.Parameters.Properties["id"])
	}
	if labels := tool.Parameters.Properties["labels"]; labels.AdditionalProperties == nil || labels.AdditionalProperties.Schema == nil {
		t.Fatalf("typed map decoded as %+v", labels)
	}
	if len(tool.Parameters.Properties["payload"].Type) != 6 {
		t.Fatalf("union property decoded as %+v", tool.Parameters.Properties["payload"])
	}
	if tool.MCP == nil || tool.MCP.Handle != "get_order" || tool.MCP.Meta["ui"] == nil || tool.MCP.Icon == nil || tool.MCP.Icon.Name != "receipt" {
		t.Fatalf("mcp decoded as %+v", tool.MCP)
	}
	if tool.Replay == nil || tool.Replay.Mode != "read_only" || len(tool.Guidance.Errors) != 1 {
		t.Fatalf("replay and guidance decoded as %+v %+v", tool.Replay, tool.Guidance)
	}
	if !tool.Returns.Type.Is("array") || tool.Returns.Items == nil || !tool.Returns.Items.Type.Is("object") {
		t.Fatalf("returns decoded as %+v", tool.Returns)
	}

	const binding = `{
	  "$schema": "https://json-schema.org/draft/2020-12/schema", "schemaVersion": "1.0",
	  "generatedAt": "2026-08-27T00:00:00Z", "apiId": "fixture-mcp", "sdkClassName": "FixtureMcpSDK",
	  "tools": [{
	    "toolName": "account.getOrder", "apiId": "fixture-mcp", "namespace": "account", "methodName": "getOrder",
	    "isScoped": true, "scopeParam": "accountId",
	    "arguments": [{"toolParameter": "id", "required": true, "kind": "path", "target": "path.id"}],
	    "methodArgs": [{"position": 0, "kind": "path", "target": "path", "sources": ["id"]}]
	  }]
	}`
	var bindings ToolBindingManifest
	decoder = json.NewDecoder(strings.NewReader(binding))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bindings); err != nil {
		t.Fatal(err)
	}
	plan := bindings.Tools[0]
	if bindings.APIID != "fixture-mcp" || plan.MethodName != "getOrder" || !plan.IsScoped || plan.ScopeParam != "accountId" {
		t.Fatalf("binding decoded as %+v", plan)
	}
	if len(plan.Arguments) != 1 || plan.Arguments[0].Target != "path.id" || len(plan.MethodArgs) != 1 || plan.MethodArgs[0].Sources[0] != "id" {
		t.Fatalf("binding arguments decoded as %+v", plan)
	}
}
